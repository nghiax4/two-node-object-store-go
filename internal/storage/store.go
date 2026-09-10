package storage

import (
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"two_node_object_store/internal/checksum"
)

type Store struct {
	dataDir string
}

func New(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

type PutResult struct {
	Size   int64
	CRC32C uint32
}

func (s *Store) Put(key string, body io.Reader) (PutResult, error) {
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

	return PutResult{Size: size, CRC32C: hasher.Sum32()}, nil
}

func (s *Store) Get(key string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(objectPath(s.dataDir, key))
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}
