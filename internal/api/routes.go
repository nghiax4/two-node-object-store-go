package api

import "net/http"

type Role int

const (
	Primary Role = iota
	Replica
)

func NewRouter(s *Server, role Role) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /objects/{key}", s.handleGet)
	switch role {
	case Primary:
		mux.HandleFunc("PUT /objects/{key}", s.handlePut)
		mux.HandleFunc("PUT /objects/{key}/readall", s.handlePutReadAll)
	case Replica:
		mux.HandleFunc("PUT /internal/objects/{key}", s.handleInternalPut)
	}
	return mux
}
