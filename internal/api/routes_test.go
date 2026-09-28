package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"two_node_object_store/internal/storage"
)

// newTestNode gives each node its own store, the same as two real
// servers with separate data dirs
func newTestNode(t *testing.T, role Role) (*storage.Store, http.Handler) {
	t.Helper()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, NewRouter(NewServer(store), role)
}

func TestRouterRoles(t *testing.T) {
	_, primary := newTestNode(t, Primary)
	replicaStore, replica := newTestNode(t, Replica)

	// Stands in for replication: the replica serves an object it already has.
	if _, err := replicaStore.Put("foo", strings.NewReader("hello"), storage.Durable); err != nil {
		t.Fatalf("seed replica: %v", err)
	}

	cases := []struct {
		name   string
		router http.Handler
		method string
		path   string
		body   string
		want   int
	}{
		{"primaryAcceptsPut", primary, http.MethodPut, "/objects/foo", "hello", http.StatusCreated},
		{"replicaRejectsPut", replica, http.MethodPut, "/objects/foo", "hello", http.StatusMethodNotAllowed},
		{"replicaServesGet", replica, http.MethodGet, "/objects/foo", "", http.StatusOK},
		{"replicaServesHealthz", replica, http.MethodGet, "/healthz", "", http.StatusOK},
		{"primaryHasNoInternalPut", primary, http.MethodPut, "/internal/objects/foo", "hello", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			tc.router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s %s: status = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
		})
	}
}
