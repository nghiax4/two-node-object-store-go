package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

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

	durability := storage.Durable
	switch h := r.Header.Get("X-Durability"); h {
	case "", "durable":
		// default
	case "buffered":
		durability = storage.Buffered
	default:
		http.Error(w, "invalid X-Durability: "+h, http.StatusBadRequest)
		return
	}

	result, err := s.store.Put(key, r.Body, durability)
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

// handleInternalPut receives an object from the primary. It only commits
// the object if the bytes match the size and CRC32C the primary sent.
func (s *Server) handleInternalPut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}

	size, err := strconv.ParseInt(r.Header.Get("X-Object-Size"), 10, 64)
	if err != nil {
		http.Error(w, "invalid X-Object-Size: "+err.Error(), http.StatusBadRequest)
		return
	}
	crc, err := strconv.ParseUint(r.Header.Get("X-Checksum-CRC32C"), 16, 32)
	if err != nil {
		http.Error(w, "invalid X-Checksum-CRC32C: "+err.Error(), http.StatusBadRequest)
		return
	}
	want := storage.PutResult{Size: size, CRC32C: uint32(crc)}

	result, err := s.store.PutVerified(key, r.Body, storage.Durable, want)
	if err != nil {
		if errors.Is(err, storage.ErrChecksumMismatch) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
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
