package api

import (
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"two_node_object_store/internal/checksum"
)

// TestInternalPut checks both the status code and, with a GET afterwards,
// that a rejected PUT saved nothing
func TestInternalPut(t *testing.T) {
	_, replica := newTestNode(t, Replica)

	body := "hello"
	size := fmt.Sprint(len(body))
	crc := fmt.Sprintf("%08x", crc32.Checksum([]byte(body), checksum.Table))

	cases := []struct {
		name    string
		size    string
		crc     string
		wantPut int
		wantGet int
	}{
		{"match", size, crc, http.StatusCreated, http.StatusOK},
		{"wrongCRC", size, "00000000", http.StatusUnprocessableEntity, http.StatusNotFound},
		{"wrongSize", fmt.Sprint(len(body) - 1), crc, http.StatusUnprocessableEntity, http.StatusNotFound},
		{"missingSize", "", crc, http.StatusBadRequest, http.StatusNotFound},
		{"missingCRC", size, "", http.StatusBadRequest, http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			putReq := httptest.NewRequest(http.MethodPut, "/internal/objects/"+tc.name, strings.NewReader(body))
			putReq.Header.Set("X-Object-Size", tc.size)
			putReq.Header.Set("X-Checksum-CRC32C", tc.crc)
			putRec := httptest.NewRecorder()
			replica.ServeHTTP(putRec, putReq)
			if putRec.Code != tc.wantPut {
				t.Fatalf("PUT status = %d, want %d (body: %q)", putRec.Code, tc.wantPut, putRec.Body.String())
			}

			getRec := httptest.NewRecorder()
			replica.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/objects/"+tc.name, nil))
			if getRec.Code != tc.wantGet {
				t.Fatalf("GET status = %d, want %d", getRec.Code, tc.wantGet)
			}
			if tc.wantGet == http.StatusOK && getRec.Body.String() != body {
				t.Errorf("GET body = %q, want %q", getRec.Body.String(), body)
			}
		})
	}

}
