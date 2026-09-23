package api

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
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

const (
	coldObjectSize  = 1024 * 1024 * 1024
	coldObjectCount = 24
	coldMinMargin   = 1.5
)

// availableMemoryBytes reads MemAvailable from /proc/meminfo - the same
// figure `free`'s "available" column reports. The cold benchmark's working
// set size is only valid relative to this machine's memory, not as a fixed
// constant, so BenchmarkGetCold checks it at runtime instead of assuming
// the value stays true.
func availableMemoryBytes() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("unexpected MemAvailable line: %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemAvailable: %w", err)
		}
		return kb * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemAvailable not found in /proc/meminfo")
}

// Cyclic GET over a working set larger than available memory, so every GET
// is a cache miss - the disk-bound counterpart to BenchmarkGetWarm.
// Setup PUTs are durable, so no background disk writes are left to slow
// down the timed reads.
func BenchmarkGetCold(b *testing.B) {
	available, err := availableMemoryBytes()
	if err != nil {
		b.Fatalf("read available memory: %v", err)
	}
	workingSet := int64(coldObjectCount) * int64(coldObjectSize)
	if float64(workingSet) < float64(available)*coldMinMargin {
		b.Fatalf("working set (%d bytes) is not at least %.1fx available memory (%d bytes); increase coldObjectCount or free up memory", workingSet, coldMinMargin, available)
	}

	store, err := storage.New(b.TempDir())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.Cleanup(func() { store.Close() })

	srv := httptest.NewServer(NewRouter(NewServer(store)))
	b.Cleanup(srv.Close)

	for i := 0; i < coldObjectCount; i++ {
		url := fmt.Sprintf("%s/objects/cold-key-%d", srv.URL, i)
		putReq, err := http.NewRequest(http.MethodPut, url, io.LimitReader(rand.Reader, coldObjectSize))
		if err != nil {
			b.Fatalf("build PUT request: %v", err)
		}
		putReq.ContentLength = coldObjectSize
		putReq.Header.Set("X-Durability", "durable") // no dirty pages left for the timed loop to compete with
		putResp, err := http.DefaultClient.Do(putReq)
		if err != nil {
			b.Fatalf("PUT: %v", err)
		}
		putResp.Body.Close()
		if putResp.StatusCode != http.StatusCreated {
			b.Fatalf("PUT status = %d, want %d", putResp.StatusCode, http.StatusCreated)
		}
	}

	b.SetBytes(coldObjectSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		url := fmt.Sprintf("%s/objects/cold-key-%d", srv.URL, i%coldObjectCount)
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
		if n != coldObjectSize {
			b.Fatalf("GET body = %d bytes, want %d", n, coldObjectSize)
		}
	}
}
