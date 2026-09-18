package storage

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"go.etcd.io/bbolt"
)

func (s *Store) reconcileObjects() error {
	expected, err := s.pruneDanglingMetadata()
	if err != nil {
		return fmt.Errorf("prune dangling metadata: %w", err)
	}
	if err := s.removeOrphanFiles(expected); err != nil {
		return fmt.Errorf("remove orphan files: %w", err)
	}
	return nil
}

func (s *Store) pruneDanglingMetadata() (map[string]struct{}, error) {
	expected := make(map[string]struct{})

	err := s.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket(objectsBucket).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			path := objectPath(s.dataDir, string(k))
			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					log.Printf("reconcile: warning: metadata for key %q has no backing file; removing entry", k)
					if err := c.Delete(); err != nil {
						return fmt.Errorf("delete dangling metadata for key %q: %w", k, err)
					}
					continue
				}
				return fmt.Errorf("stat object file for key %q: %w", k, err)
			}
			expected[path] = struct{}{}
		}
		return nil
	})

	return expected, err
}

func (s *Store) removeOrphanFiles(expected map[string]struct{}) error {
	objectsDir := filepath.Join(s.dataDir, "objects")
	if _, err := os.Stat(objectsDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat objects dir: %w", err)
	}

	return filepath.WalkDir(objectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if _, ok := expected[path]; ok {
			return nil
		}
		log.Printf("reconcile: warning: file %q has no backing metadata; removing", path)
		return os.Remove(path)
	})
}

// cleanupTmpDir removes any leftover temp files from an unclean shutdown.
// Put's own temp files are already removed on every normal return path
// (success or error — see the deferred os.Remove in Put/PutReadAll);
// anything still here only survived a hard crash mid-write.
func (s *Store) cleanupTmpDir() error {
	tmpDir := filepath.Join(s.dataDir, "tmp")

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read tmp dir: %w", err)
	}

	for _, entry := range entries {
		path := filepath.Join(tmpDir, entry.Name())
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove leftover temp file %s: %w", path, err)
		}
	}

	return nil
}
