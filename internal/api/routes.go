package api

import "net/http"

func NewRouter(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("PUT /objects/{key}", s.handlePut)
	mux.HandleFunc("PUT /objects/{key}/buffered", s.handlePutBuffered)
	mux.HandleFunc("GET /objects/{key}", s.handleGet)
	return mux
}
