package replication

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"two_node_object_store/internal/storage"
)

// Sender copies objects from this node's store to the replica.
type Sender struct {
	store  *storage.Store
	peer   string
	client *http.Client
}

func NewSender(store *storage.Store, peer string) *Sender {
	return &Sender{store: store, peer: peer, client: &http.Client{}}
}

// Send PUTs the current version of key to the replica. If the object
// changes between Get's metadata read and its file open, the size/CRC32C
// won't match the bytes, the replica rejects the send, and a retry fixes
// it.
func (s *Sender) Send(ctx context.Context, key string) error {
	obj, err := s.store.Get(key)
	if err != nil {
		return fmt.Errorf("get %q: %w", key, err)
	}
	defer obj.Body.Close()

	u := s.peer + "/internal/objects/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, obj.Body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.ContentLength = obj.Size
	req.Header.Set("X-Object-Size", strconv.FormatInt(obj.Size, 10))
	req.Header.Set("X-Checksum-CRC32C", fmt.Sprintf("%08x", obj.CRC32C))

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send %q: %w", key, err)
	}
	defer resp.Body.Close()

	// Read the whole body, even on success, so the connection can be reused.
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("send %q: replica returned %s: %s", key, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
