package storage

import (
	"testing"
	"time"
)

func TestEncodeDecodeMetaRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		size int64
		crc32c uint32
		updatedAt time.Time
	}{
		{"zero values", 0, 0, time.Unix(0, 0)},
		{"typical", 1024, 0xdeadbeef, time.Unix(1757842800, 0)},
		{"maxCRC32", 100 * 1024 * 1024, 0xffffffff, time.Unix(1893456000, 0)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeMeta(tc.size, tc.crc32c, tc.updatedAt)
			if len(encoded) != metaSize {
				t.Fatalf("encodeMeta: len = %d, want %d", len(encoded), metaSize)
			}

			gotSize, gotCRC, gotUpdatedAt, err := decodeMeta(encoded)
			if err != nil {
				t.Fatalf("decodeMeta: %v", err)
			}
			if gotSize != tc.size {
				t.Errorf("size = %d, want %d", gotSize, tc.size)
			}
			if gotCRC != tc.crc32c {
				t.Errorf("crc32c = %08x, want %08x", gotCRC, tc.crc32c)
			}
			if !gotUpdatedAt.Equal(tc.updatedAt) {
				t.Errorf("updatedAt = %v, want %v", gotUpdatedAt, tc.updatedAt)
			}
		})
	}
}

func TestDecodeMetaWrongLength(t *testing.T) {
	cases := []struct {
		name string
		b []byte
	}{
		{"empty", []byte{}},
		{"tooShort", make([]byte, metaSize-1)},
		{"tooLong", make([]byte, metaSize+1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := decodeMeta(tc.b)
			if err == nil {
				t.Fatalf("decodeMeta(%d bytes): expected error, got nil", len(tc.b))
			}
		})
	}
}
