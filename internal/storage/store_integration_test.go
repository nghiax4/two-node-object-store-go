package storage

import (
	"crypto/rand"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"testing"

	"two_node_object_store/internal/checksum"
)

var putGetSizes = []struct {
	name string
	size int64
}{
	{"1KiB", 1 << 10},
	{"1MiB", 1 << 20},
	{"100MiB", 100 << 20},
	{"1GiB", 1 << 30},
}

func TestPutGetSurvivesRestartAcrossSizes(t *testing.T) {
	dataDir := t.TempDir()
	scratchDir := t.TempDir()

	store, err := New(dataDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type written struct {
		key    string
		size   int64
		crc32c uint32
	}
	var entries []written

	for _, tc := range putGetSizes {
		t.Run(tc.name+"/put_get", func(t *testing.T) {
			srcPath, wantCRC, err := writeRandomFile(scratchDir, tc.name, tc.size)
			if err != nil {
				t.Fatalf("generate source file: %v", err)
			}

			src, err := os.Open(srcPath)
			if err != nil {
				t.Fatalf("open source file: %v", err)
			}
			defer src.Close()

			result, err := store.Put(tc.name, src, Buffered)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if result.Size != tc.size {
				t.Errorf("Size = %d, want %d", result.Size, tc.size)
			}
			if result.CRC32C != wantCRC {
				t.Errorf("CRC32C = %08x, want %08x", result.CRC32C, wantCRC)
			}

			verifyStoredObject(t, store, tc.name, tc.size, wantCRC)

			entries = append(entries, written{key: tc.name, size: tc.size, crc32c: wantCRC})
		})
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	defer restarted.Close()

	for _, e := range entries {
		t.Run(e.key+"/after_restart", func(t *testing.T) {
			verifyStoredObject(t, restarted, e.key, e.size, e.crc32c)
		})
	}
}

// verifyStoredObject confirms a key resolves through Get with the expected
// metadata.
func verifyStoredObject(t *testing.T, store *Store, key string, wantSize int64, wantCRC uint32) {
	t.Helper()

	got, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	defer got.Body.Close()

	if got.Size != wantSize {
		t.Errorf("Get(%q).Size = %d, want %d", key, got.Size, wantSize)
	}
	if got.CRC32C != wantCRC {
		t.Errorf("Get(%q).CRC32C = %08x, want %08x", key, got.CRC32C, wantCRC)
	}

	hasher := crc32.New(checksum.Table)
	n, err := io.Copy(hasher, got.Body)
	if err != nil {
		t.Fatalf("read back %q: %v", key, err)
	}
	if n != wantSize {
		t.Errorf("read back %q: got %d bytes, want %d", key, n, wantSize)
	}
	if gotCRC := hasher.Sum32(); gotCRC != wantCRC {
		t.Errorf("read back %q: CRC32C = %08x, want %08x", key, gotCRC, wantCRC)
	}
}

// writeRandomFile writes size random bytes to a new file under dir (named
// after name), streaming through a 32 KiB buffer rather than generating the
// whole payload in memory first.
// Returns the file's path and its CRC32C.
func writeRandomFile(dir, name string, size int64) (string, uint32, error) {
	path := filepath.Join(dir, name+".src")
	f, err := os.Create(path)
	if err != nil {
		return "", 0, fmt.Errorf("create source file: %w", err)
	}
	defer f.Close()

	hasher := crc32.New(checksum.Table)
	mw := io.MultiWriter(f, hasher)
	buf := make([]byte, 32*1024)

	var written int64
	for written < size {
		n := int64(len(buf))
		if remaining := size - written; remaining < n {
			n = remaining
		}
		if _, err := rand.Read(buf[:n]); err != nil {
			return "", 0, fmt.Errorf("generate random bytes: %w", err)
		}
		if _, err := mw.Write(buf[:n]); err != nil {
			return "", 0, fmt.Errorf("write source file: %w", err)
		}
		written += n
	}

	if err := f.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync source file: %w", err)
	}

	return path, hasher.Sum32(), nil
}
