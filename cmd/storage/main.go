package main

import (
	"flag"
	"log"
	"net/http"

	"two_node_object_store/internal/api"
	"two_node_object_store/internal/storage"
)

func main() {
	addr := flag.String("addr", ":8080", "address for the HTTP server to listen on")
	dataDir := flag.String("data-dir", "./data", "directory for object and metadata storage")
	flag.Parse()

	store, err := storage.New(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()
	server := api.NewServer(store)
	mux := api.NewRouter(server)

	log.Printf("listening on %s (data dir: %s)", *addr, *dataDir)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
