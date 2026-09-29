package storage

import (
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"time"

	"two_node_object_store/internal/checksum"

	"go.etcd.io/bbolt"
)

var (
	objectsBucket     = []byte("objects")
	replicationBucket = []byte("replication")
)

// ErrChecksumMismatch means the received bytes didn't match the size or
// CRC32C the sender said they would have.
var ErrChecksumMismatch = errors.New("size or checksum mismatch")

type Store struct {
	dataDir string
	db      *bbolt.DB
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "metadata.db")
	db, err := bbolt.Open(dbPath, 0o600, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open metadata db: %w", err)
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(objectsBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(replicationBucket); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create buckets: %w", err)
	}

	store := &Store{dataDir: dataDir, db: db}
	if err := store.cleanupTmpDir(); err != nil {
		db.Close()
		return nil, fmt.Errorf("startup reconciliation: %w", err)
	}
	if err := store.reconcileObjects(); err != nil {
		db.Close()
		return nil, fmt.Errorf("startup reconciliation: %w", err)
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// commitMeta writes the object's metadata and, if enqueue is true, a
// replication queue entry for it, in one transaction: both are saved or
// neither is.
func (s *Store) commitMeta(key string, size int64, crc32c uint32, enqueue bool) error {
	meta := encodeMeta(size, crc32c, time.Now())
	return s.db.Update(func(tx *bbolt.Tx) error {
		if err := tx.Bucket(objectsBucket).Put([]byte(key), meta); err != nil {
			return err
		}
		if enqueue {
			if err := enqueueTx(tx, key); err != nil {
				return err
			}
		}
		return nil
	})
}

type PutResult struct {
	Size   int64
	CRC32C uint32
}

type Durability int

const (
	Buffered Durability = iota
	Durable
)

func (s *Store) Put(key string, body io.Reader, durability Durability) (PutResult, error) {
	return s.put(key, body, durability, nil, true)
}

// PutVerified is Put, but it only commits the object if the received
// bytes match want's size and CRC32C. Used by the replica when the
// primary sends it an object. It never adds a replication queue entry:
// the replica doesn't send copies on.
func (s *Store) PutVerified(key string, body io.Reader, durability Durability, want PutResult) (PutResult, error) {
	return s.put(key, body, durability, &want, false)
}

// put is the shared write path. want == nil skips the size/CRC32C check.
// enqueue adds a replication queue entry in the same transaction as the
// metadata.
func (s *Store) put(key string, body io.Reader, durability Durability, want *PutResult, enqueue bool) (PutResult, error) {
	tmpDir := filepath.Join(s.dataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return PutResult{}, fmt.Errorf("create tmp dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(tmpDir, "obj-*.tmp")
	if err != nil {
		return PutResult{}, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	hasher := crc32.New(checksum.Table)
	buf := make([]byte, 32*1024) // "32*1024" is the number of bytes
	size, err := io.CopyBuffer(io.MultiWriter(tmpFile, hasher), body, buf)
	if err != nil {
		tmpFile.Close()
		return PutResult{}, fmt.Errorf("write temp file: %w", err)
	}
	crc32c := hasher.Sum32()
	if want != nil && (size != want.Size || crc32c != want.CRC32C) {
		tmpFile.Close()
		return PutResult{}, fmt.Errorf("%w: got size %d crc %08x, want size %d crc %08x", ErrChecksumMismatch, size, crc32c, want.Size, want.CRC32C)
	}
	if durability == Durable {
		if err := tmpFile.Sync(); err != nil {
			tmpFile.Close()
			return PutResult{}, fmt.Errorf("fsync temp file: %w", err)
		}
	}
	if err := tmpFile.Close(); err != nil {
		return PutResult{}, fmt.Errorf("close temp file: %w", err)
	}

	finalPath := objectPath(s.dataDir, key)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return PutResult{}, fmt.Errorf("create object dir: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return PutResult{}, fmt.Errorf("rename into place: %w", err)
	}

	if durability == Durable {
		// rename() only makes the new name durable in the directory's
		// own data once this fsync happens; see
		// https://dev.to/syed_anzar/your-filesystem-is-lying-to-you-why-fsync-doesnt-guarantee-durability-1ik7
		dir, err := os.Open(filepath.Dir(finalPath))
		if err != nil {
			return PutResult{}, fmt.Errorf("open object dir: %w", err)
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return PutResult{}, fmt.Errorf("fsync object dir: %w", syncErr)
		}
		if closeErr != nil {
			return PutResult{}, fmt.Errorf("close object dir: %w", closeErr)
		}
	}

	if err := s.commitMeta(key, size, crc32c, enqueue); err != nil {
		return PutResult{}, fmt.Errorf("commit metadata: %w", err)
	}

	return PutResult{Size: size, CRC32C: crc32c}, nil
}

// PutReadAll is a naive write path (loads the full body into memory before writing),
// kept as a benchmark baseline against Put.
func (s *Store) PutReadAll(key string, body io.Reader) (PutResult, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return PutResult{}, fmt.Errorf("read body: %w", err)
	}

	tmpDir := filepath.Join(s.dataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return PutResult{}, fmt.Errorf("create tmp dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(tmpDir, "obj-*.tmp")
	if err != nil {
		return PutResult{}, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return PutResult{}, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return PutResult{}, fmt.Errorf("close temp file: %w", err)
	}

	finalPath := objectPath(s.dataDir, key)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return PutResult{}, fmt.Errorf("create object dir: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return PutResult{}, fmt.Errorf("rename into place: %w", err)
	}

	size := int64(len(data))
	crc32c := crc32.Checksum(data, checksum.Table)
	if err := s.commitMeta(key, size, crc32c, true); err != nil {
		return PutResult{}, fmt.Errorf("commit metadata: %w", err)
	}

	return PutResult{Size: size, CRC32C: crc32c}, nil
}

type GetResult struct {
	Body   *os.File
	Size   int64
	CRC32C uint32
}

func (s *Store) Get(key string) (GetResult, error) {
	var metaBytes []byte
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(objectsBucket).Get([]byte(key))
		if b == nil {
			return os.ErrNotExist
		}
		metaBytes = append([]byte(nil), b...)
		return nil
	})
	if err != nil {
		return GetResult{}, err
	}

	size, crc32c, _, err := decodeMeta(metaBytes)
	if err != nil {
		return GetResult{}, fmt.Errorf("decode metadata: %w", err)
	}

	f, err := os.Open(objectPath(s.dataDir, key))
	if err != nil {
		return GetResult{}, err
	}

	return GetResult{Body: f, Size: size, CRC32C: crc32c}, nil
}
