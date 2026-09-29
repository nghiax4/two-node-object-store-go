package replication_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"hash/crc32"
	"io"
	"net/http/httptest"
	"os"
	"testing"

	"two_node_object_store/internal/api"
	"two_node_object_store/internal/checksum"
	"two_node_object_store/internal/replication"
	"two_node_object_store/internal/storage"
)

func newStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// newReplica serves as a replica node over real HTTP on localhost.
func newReplica(t *testing.T) (*storage.Store, *httptest.Server) {
	t.Helper()
	store := newStore(t)
	srv := httptest.NewServer(api.NewRouter(api.NewServer(store), api.Replica))
	t.Cleanup(srv.Close)
	return store, srv
}

func put(t *testing.T, store *storage.Store, key string, data []byte) {
	t.Helper()
	if _, err := store.Put(key, bytes.NewReader(data), storage.Durable); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// readObject returns an object's bytes and the CRC32C its metadata records.
func readObject(t *testing.T, store *storage.Store, key string) ([]byte, uint32) {
	t.Helper()
	obj, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer obj.Body.Close()
	data, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	return data, obj.CRC32C
}

func TestSendCopiesObject(t *testing.T) {
	primary := newStore(t)
	replicaStore, srv := newReplica(t)
	sender := replication.NewSender(primary, srv.URL)

	data := make([]byte, 100*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("generate random data: %v", err)
	}
	put(t, primary, "foo", data)

	if err := sender.Send(context.Background(), "foo"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, gotCRC := readObject(t, replicaStore, "foo")
	if !bytes.Equal(got, data) {
		t.Errorf("replica bytes differ from the primary's")
	}
	if want := crc32.Checksum(data, checksum.Table); gotCRC != want {
		t.Errorf("replica CRC32C = %08x, want %08x", gotCRC, want)
	}
}

func TestSendSendsCurrentVersion(t *testing.T) {
	primary := newStore(t)
	replicaStore, srv := newReplica(t)
	sender := replication.NewSender(primary, srv.URL)

	for _, version := range []string{"v1", "v2"} {
		put(t, primary, "foo", []byte(version))
		if err := sender.Send(context.Background(), "foo"); err != nil {
			t.Fatalf("Send %s: %v", version, err)
		}
	}

	if got, _ := readObject(t, replicaStore, "foo"); string(got) != "v2" {
		t.Errorf("replica has %q, want %q", got, "v2")
	}
}

func TestSendMissingKey(t *testing.T) {
	primary := newStore(t)
	_, srv := newReplica(t)
	sender := replication.NewSender(primary, srv.URL)

	err := sender.Send(context.Background(), "never-put")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Send error = %v, want os.ErrNotExist", err)
	}
}

func TestSendReplicaDown(t *testing.T) {
	primary := newStore(t)
	_, srv := newReplica(t)
	sender := replication.NewSender(primary, srv.URL)
	put(t, primary, "foo", []byte("hello"))

	srv.Close()
	if err := sender.Send(context.Background(), "foo"); err == nil {
		t.Fatal("Send succeeded with the replica down, want an error")
	}
}
