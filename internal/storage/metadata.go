package storage

import (
	"encoding/binary"
	"fmt"
	"time"
)

const metaSize = 20 // 8 (size) + 4 (crc32c) + 8 (unix timestamp)

func encodeMeta(size int64, crc32c uint32, updatedAt time.Time) []byte {
	b := make([]byte, metaSize)
	binary.BigEndian.PutUint64(b[0:8], uint64(size))
	binary.BigEndian.PutUint32(b[8:12], crc32c)
	binary.BigEndian.PutUint64(b[12:20], uint64(updatedAt.Unix()))
	return b
}

func decodeMeta(b []byte) (size int64, crc32c uint32, updatedAt time.Time, err error) {
	if len(b) != metaSize {
		return 0, 0, time.Time{}, fmt.Errorf("decode meta: expected %d bytes, got %d", metaSize, len(b))
	}
	size = int64(binary.BigEndian.Uint64(b[0:8]))
	crc32c = binary.BigEndian.Uint32(b[8:12])
	updatedAt = time.Unix(int64(binary.BigEndian.Uint64(b[12:20])), 0)
	return size, crc32c, updatedAt, nil
}
