package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ramonskie/groovearr/internal/api"
	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/download"
	deezerdl "github.com/ramonskie/groovearr/internal/download/deezer"
	"github.com/ramonskie/groovearr/internal/download/soulseek"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/library/sqlite"
)

func main() {
	// Load config.
	configPath := os.Getenv("GROOVEARR_CONFIG")
	if configPath == "" {
		configPath = "./config.json"
	}

	cfg, err := config.LoadOrCreate(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Library store (SQLite).
	dbPath := filepath.Join(filepath.Dir(configPath), "library.db")
	store, err := sqlite.New(dbPath)
	if err != nil {
		log.Fatalf("library db: %v", err)
	}
	defer store.Close()

	scanner := library.NewScanner(store)

	// Build plugin registry.
	registry := download.NewRegistry()

	currentCfg := cfg.Get()
	slskd := soulseek.New(currentCfg.Soulseek)
	if err := registry.Register(slskd); err != nil {
		log.Fatalf("register soulseek: %v", err)
	}

	deezer := deezerdl.NewDownloadClient(currentCfg.Deezer, currentCfg.Soulseek.DownloadPath)
	if err := registry.Register(deezer, "deezer_dl"); err != nil {
		log.Printf("register deezer download: %v (continuing without deezer)", err)
	}

	// Download orchestrator.
	orch := download.NewOrchestrator(registry)

	// HTTP server.
	addr := os.Getenv("GROOVEARR_ADDR")
	if addr == "" {
		addr = ":8008"
	}

	srv := api.NewServer(addr, cfg, orch, store, scanner)

	// Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		srv.Shutdown(context.Background())
	}()

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
