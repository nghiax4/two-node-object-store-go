package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

func objectPath(dataDir, key string) string {
	sum := sha256.Sum256([]byte(key))
	hexSum := hex.EncodeToString(sum[:])
	return filepath.Join(dataDir, "objects", hexSum[:2], hexSum[2:])
}
