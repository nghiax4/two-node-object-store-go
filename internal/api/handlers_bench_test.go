package api

import (
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"two_node_object_store/internal/storage"
)

const warmObjectSize = 1024 * 1024 * 1024

// Measures GET over the real HTTP router, not Store.Get directly, so the
// sendfile() path is what actually gets measured.
// Warm-cache only - the object stays page-cache-resident for the whole
// run.
func BenchmarkGetWarm(b *testing.B) {
	store, err := storage.New(b.TempDir())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.Cleanup(func() { store.Close() })

	srv := httptest.NewServer(NewRouter(NewServer(store)))
	b.Cleanup(srv.Close)

	url := srv.URL + "/objects/warm-key"

	putReq, err := http.NewRequest(http.MethodPut, url, io.LimitReader(rand.Reader, warmObjectSize))
	if err != nil {
		b.Fatalf("build PUT request: %v", err)
	}
	putReq.ContentLength = warmObjectSize
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		b.Fatalf("PUT: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		b.Fatalf("PUT status = %d, want %d", putResp.StatusCode, http.StatusCreated)
	}

	b.SetBytes(warmObjectSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.Get(url)
		if err != nil {
			b.Fatalf("GET: %v", err)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			b.Fatalf("read body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("GET status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		if n != warmObjectSize {
			b.Fatalf("GET body = %d bytes, want %d", n, warmObjectSize)
		}
	}
}
