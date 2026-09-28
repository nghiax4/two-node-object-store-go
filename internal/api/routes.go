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
	if role == Primary {
		mux.HandleFunc("PUT /objects/{key}", s.handlePut)
		mux.HandleFunc("PUT /objects/{key}/readall", s.handlePutReadAll)
	}
	return mux
}
