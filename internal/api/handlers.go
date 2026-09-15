package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"two_node_object_store/internal/storage"
)

type Server struct {
	store *storage.Store
}

func NewServer(store *storage.Store) *Server {
	return &Server{store: store}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}

	result, err := s.store.Put(key, r.Body)
	if err != nil {
		http.Error(w, "put failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("X-Checksum-CRC32C", fmt.Sprintf("%08x", result.CRC32C))
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handlePutReadAll(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}

	result, err := s.store.PutReadAll(key, r.Body)
	if err != nil {
		http.Error(w, "put failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("X-Checksum-CRC32C", fmt.Sprintf("%08x", result.CRC32C))
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}

	result, err := s.store.Get(key)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "get failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer result.Body.Close()

	w.Header().Set("X-Checksum-CRC32C", fmt.Sprintf("%08x", result.CRC32C))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", result.Size))
	io.Copy(w, result.Body)
}
