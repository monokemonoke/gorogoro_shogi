package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"

	"gorogoro/server"
)

//go:embed web/*
var webFS embed.FS

func main() {
	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("failed to load web assets: %v", err)
	}

	srv := server.New(http.FS(webRoot))

	addr := ":8080"
	log.Printf("Serving Gorogoro Shogi UI at http://localhost%s\n", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
