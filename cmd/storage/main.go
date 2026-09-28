package main

import (
	"flag"
	"log"
	"net/http"
	"net/url"

	"two_node_object_store/internal/api"
	"two_node_object_store/internal/storage"
)

func main() {
	addr := flag.String("addr", ":8080", "address for the HTTP server to listen on")
	dataDir := flag.String("data-dir", "./data", "directory for object and metadata storage")
	roleName := flag.String("role", "", "node role: primary or replica (required)")
	peer := flag.String("peer", "", "replica base URL, e.g. http://10.0.1.23:8080 (primary only)")
	flag.Parse()

	var role api.Role
	switch *roleName {
	case "primary":
		role = api.Primary
		u, err := url.Parse(*peer)
		if err != nil || u.Scheme != "http" || u.Host == "" {
			log.Fatalf("primary needs -peer as http://host:port, got %q", *peer)
		}
	case "replica":
		role = api.Replica
		if *peer != "" {
			log.Fatalf("replica does not take -peer, got %q", *peer)
		}
	default:
		log.Fatalf("-role must be primary or replica, got %q", *roleName)
	}

	store, err := storage.New(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()
	server := api.NewServer(store)
	mux := api.NewRouter(server, role)

	log.Printf("listening on %s as %s (data dir: %s)", *addr, *roleName, *dataDir)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
