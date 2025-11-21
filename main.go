package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"

	"gorogoro/server"
)

//go:embed web/*
var webFS embed.FS

func main() {
	dataDir := flag.String("data-dir", "data", "directory for persistent engine data")
	flag.Parse()

	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("failed to load web assets: %v", err)
	}

	srv := server.New(http.FS(webRoot), server.Config{DataDir: *dataDir})

	addr := ":8080"
	log.Printf("Serving Gorogoro Shogi UI at http://localhost%s\n", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
