package storage

import (
	"bytes"
	"hash/crc32"
	"slices"
	"testing"

	"two_node_object_store/internal/checksum"
)

// drainQueue empties the replication queue through OldestQueued and
// Dequeue, and returns the keys in the order it saw them. It gives up
// after maxEntries so a broken Dequeue fails the test instead of hanging.
func drainQueue(t *testing.T, store *Store) []string {
	t.Helper()
	const maxEntries = 100
	var keys []string
	for range maxEntries {
		entry, ok, err := store.OldestQueued()
		if err != nil {
			t.Fatalf("OldestQueued: %v", err)
		}
		if !ok {
			return keys
		}
		keys = append(keys, entry.Key)
		if err := store.Dequeue(entry.Seq); err != nil {
			t.Fatalf("Dequeue(%d): %v", entry.Seq, err)
		}
	}
	t.Fatalf("queue still not empty after %d entries (so far: %v)", maxEntries, keys)
	return nil
}

func TestStorePutEnqueuesInOrder(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	keys := []string{"a", "b", "a"}
	for _, key := range keys {
		if _, err := store.Put(key, bytes.NewReader([]byte("hello")), Durable); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	if got := drainQueue(t, store); !slices.Equal(got, keys) {
		t.Errorf("queue = %v, want %v", got, keys)
	}
}

func TestStoreQueueSurvivesRestart(t *testing.T) {
	dataDir := t.TempDir()

	store, err := New(dataDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	keys := []string{"a", "b"}
	for _, key := range keys {
		if _, err := store.Put(key, bytes.NewReader([]byte("hello")), Durable); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2, err := New(dataDir)
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	defer store2.Close()

	if got := drainQueue(t, store2); !slices.Equal(got, keys) {
		t.Errorf("queue after restart = %v, want %v", got, keys)
	}
}

func TestStorePutVerifiedDoesNotEnqueue(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	// The correct size and CRC, so the object really is saved. With a wrong
	// one, PutVerified would fail before commitMeta and the queue would be
	// empty for the wrong reason.
	data := []byte("hello")
	want := PutResult{Size: int64(len(data)), CRC32C: crc32.Checksum(data, checksum.Table)}
	if _, err := store.PutVerified("from-primary", bytes.NewReader(data), Durable, want); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}

	if got := drainQueue(t, store); len(got) != 0 {
		t.Errorf("queue = %v, want empty", got)
	}
}

// signalWaiting reports whether a signal is waiting on store.Queued(),
// and it takes it if so.
func signalWaiting(store *Store) bool {
	select {
	case <-store.Queued():
		return true
	default:
		return false
	}
}

func TestStorePutSignalsQueued(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	// Two Puts with nobody reading the channel. If the send blocked, the
	// second Put would hang there on the full channel.
	keys := []string{"a", "b"}
	for _, key := range keys {
		if _, err := store.Put(key, bytes.NewReader([]byte("hello")), Durable); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	// Two Puts, but only one signal: the channel holds at most one.
	if !signalWaiting(store) {
		t.Errorf("no signal waiting after Put, want one")
	}
	if signalWaiting(store) {
		t.Errorf("second signal waiting, want at most one")
	}
}

func TestStorePutVerifiedDoesNotSignal(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	// Correct size and CRC, for the same reason as in
	// TestStorePutVerifiedDoesNotEnqueue.
	data := []byte("hello")
	want := PutResult{Size: int64(len(data)), CRC32C: crc32.Checksum(data, checksum.Table)}
	if _, err := store.PutVerified("from-primary", bytes.NewReader(data), Durable, want); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}

	if signalWaiting(store) {
		t.Errorf("signal waiting after PutVerified, want none")
	}
}
