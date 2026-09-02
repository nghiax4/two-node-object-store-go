package main

import (
	"flag"
	"log"
	"net/http"
	"two_node_object_store/internal/api"
)

func main() {
	addr := flag.String("addr", ":8080", "address for the HTTP server to listen on")
	dataDir := flag.String("data-dir", "./data", "directory for object and metadata storage")
	flag.Parse()

	_ = dataDir // wired in Stage 2 once storage exists

	mux := api.NewRouter()

	log.Printf("listening on %s (data dir: %s)", *addr, *dataDir)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
