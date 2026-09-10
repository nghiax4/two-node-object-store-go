package storage

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"
)

func BenchmarkPut(b *testing.B) {
	store := New(b.TempDir())
	data := make([]byte, 64*1024*1024)
	if _, err := rand.Read(data); err != nil {
		b.Fatalf("generate random data: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%d", i)
		if _, err := store.Put(key, bytes.NewReader(data)); err != nil {
			b.Fatalf("Put: %v", err)
		}
	}
}

func BenchmarkPutBuffered(b *testing.B) {
	store := New(b.TempDir())
	data := make([]byte, 64*1024*1024)
	if _, err := rand.Read(data); err != nil {
		b.Fatalf("generate random data: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key-%d", i)
		if _, err := store.PutBuffered(key, bytes.NewReader(data)); err != nil {
			b.Fatalf("PutBuffered: %v", err)
		}
	}
}
