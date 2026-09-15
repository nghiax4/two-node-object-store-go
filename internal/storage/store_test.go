package storage

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
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

			result, err := store.Put(tc.name, bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if result.Size != int64(tc.size) {
				t.Errorf("Size = %d, want %d", result.Size, tc.size)
			}
			if result.CRC32C != wantCRC {
				t.Errorf("CRC32C = %08x, want %08x", result.CRC32C, wantCRC)
			}

			f, info, err := store.Get(tc.name)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer f.Close()

			if info.Size() != int64(tc.size) {
				t.Errorf("stat size = %d, want %d", info.Size(), tc.size)
			}

			got, err := io.ReadAll(f)
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

	_, _, err = store.Get("does-not-exist")
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
	result, err := store.Put("key1", bytes.NewReader([]byte("hello world")))
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
