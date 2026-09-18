package storage

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
	"two_node_object_store/internal/checksum"
)

func TestStorePutGetRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"small", 128},
		{"exactly32KiB", 32 * 1024},
		{"largerThan32KiB", 100 * 1024},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := New(t.TempDir())
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			data := make([]byte, tc.size)
			if _, err := rand.Read(data); err != nil {
				t.Fatalf("generate random data: %v", err)
			}

			wantCRC := crc32.Checksum(data, checksum.Table)

			result, err := store.Put(tc.name, bytes.NewReader(data), Buffered)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if result.Size != int64(tc.size) {
				t.Errorf("Size = %d, want %d", result.Size, tc.size)
			}
			if result.CRC32C != wantCRC {
				t.Errorf("CRC32C = %08x, want %08x", result.CRC32C, wantCRC)
			}

			got_, err := store.Get(tc.name)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer got_.Body.Close()

			if got_.Size != int64(tc.size) {
				t.Errorf("meta size = %d, want %d", got_.Size, tc.size)
			}
			if got_.CRC32C != wantCRC {
				t.Errorf("meta CRC32C = %08x, want %08x", got_.CRC32C, wantCRC)
			}

			got, err := io.ReadAll(got_.Body)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Errorf("roundtrip bytes mismatch (len got=%d, want=%d)", len(got), len(data))
			}
		})
	}
}

func TestStoreGetMissingKey(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = store.Get("does-not-exist")
	if err == nil {
		t.Fatal("Get on missing key: expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Get on missing key: err = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
}

func TestPutCommitsMetadata(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	before := time.Now()
	result, err := store.Put("key1", bytes.NewReader([]byte("hello world")), Buffered)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	after := time.Now()

	var gotSize int64
	var gotCRC uint32
	var gotUpdatedAt time.Time
	err = store.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(objectsBucket).Get([]byte("key1"))
		if v == nil {
			return fmt.Errorf("no metadata found for key1")
		}
		var decodeErr error
		gotSize, gotCRC, gotUpdatedAt, decodeErr = decodeMeta(v)
		return decodeErr
	})
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}

	if gotSize != result.Size {
		t.Errorf("size = %d, want %d", gotSize, result.Size)
	}
	if gotCRC != result.CRC32C {
		t.Errorf("crc32c = %08x, want %08x", gotCRC, result.CRC32C)
	}
	if gotUpdatedAt.Before(before.Add(-time.Second)) || gotUpdatedAt.After(after.Add(time.Second)) {
		t.Errorf("updatedAt = %v, want between %v and %v", gotUpdatedAt, before, after)
	}
}

func TestStorePutDurableRoundTrip(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data := make([]byte, 128)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("generate random data: %v", err)
	}
	wantCRC := crc32.Checksum(data, checksum.Table)

	result, err := store.Put("durable-key", bytes.NewReader(data), Durable)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if result.CRC32C != wantCRC {
		t.Errorf("CRC32C = %08x, want %08x", result.CRC32C, wantCRC)
	}

	got, err := store.Get("durable-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer got.Body.Close()

	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(body, data) {
		t.Errorf("roundtrip bytes mismatch (len got=%d, want=%d)", len(body), len(data))
	}
}

func TestStoreNewCleansTmpDir(t *testing.T) {
	dataDir := t.TempDir()

	tmpDir := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("create tmp dir: %v", err)
	}
	strayPath := filepath.Join(tmpDir, "obj-leftover.tmp")
	if err := os.WriteFile(strayPath, []byte("leftover"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	store, err := New(dataDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("tmp dir not empty after New: %v", entries)
	}
}

func TestStoreReconcileRemovesDanglingMetadata(t *testing.T) {
	dataDir := t.TempDir()

	store, err := New(dataDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	key := "dangling-key"
	// Durability mode is irrelevant here: reconciliation only reacts to
	// file/metadata state after Put returns, not how Put got there.
	if _, err := store.Put(key, bytes.NewReader([]byte("hello")), Buffered); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := os.Remove(objectPath(dataDir, key)); err != nil {
		t.Fatalf("remove object file: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2, err := New(dataDir)
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	defer store2.Close()

	if _, err := store2.Get(key); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Get after reconciliation: got err=%v, want ErrNotExist", err)
	}
}

func TestStoreReconcileRemovesOrphanFile(t *testing.T) {
	dataDir := t.TempDir()

	key := "orphan-key"
	path := objectPath(dataDir, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create object dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("orphan"), 0o644); err != nil {
		t.Fatalf("write orphan file: %v", err)
	}

	store, err := New(dataDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected orphan file removed, stat err = %v", err)
	}
}
