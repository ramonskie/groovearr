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
	"github.com/ramonskie/groovearr/internal/domain"
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

	// Ensure required directories exist.
	for _, p := range []string{currentCfg.Library.DownloadPath, currentCfg.Library.LibraryPath} {
		if p != "" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				log.Printf("mkdir %s: %v", p, err)
			}
		}
	}

	slskd := soulseek.New(currentCfg.Soulseek, currentCfg.Library.DownloadPath)
	if err := registry.Register(slskd); err != nil {
		log.Fatalf("register soulseek: %v", err)
	}

	deezer := deezerdl.NewDownloadClient(currentCfg.Deezer, currentCfg.Library.DownloadPath)
	if err := registry.Register(deezer, "deezer_dl"); err != nil {
		log.Printf("register deezer download: %v (continuing without deezer)", err)
	}

	// Download orchestrator.
	orch := download.NewOrchestrator(registry, func() config.QualityConfig {
		return cfg.Get().Quality
	})

	// Post-download processor: renames files into organized library structure,
	// then fetches cover art for the album directory, then writes audio tags.
	postProc := download.NewPostProcessor(
		newRenamerHook(cfg),
		library.NewCoverHook(store),
		library.NewTagWriterHook(),
	)

	// HTTP server.
	addr := os.Getenv("GROOVEARR_ADDR")
	if addr == "" {
		addr = ":8008"
	}

	srv := api.NewServer(addr, cfg, orch, store, scanner, postProc)

	log.Printf("groovearr starting")
	log.Printf("  config:   %s", configPath)
	log.Printf("  database: %s", dbPath)
	log.Printf("  download: %s", currentCfg.Library.DownloadPath)
	log.Printf("  library:  %s", currentCfg.Library.LibraryPath)
	log.Printf("  listening on %s", addr)
	if currentCfg.Soulseek.SlskdURL != "" {
		log.Printf("  slskd:    %s", currentCfg.Soulseek.SlskdURL)
	}

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

// newRenamerHook creates a post-download hook that renames files using the configured
// folder template. It reads the latest config on each call so template changes take effect
// without restart. Files are moved from the download staging area to the library root.
func newRenamerHook(cfg *config.Persistence) download.PostDownloadHook {
	return func(ctx context.Context, record domain.DownloadRecord) (string, error) {
		c := cfg.Get()
		template := c.Library.FolderTemplate
		root := c.Library.LibraryPath
		if root == "" {
			root = c.Library.DownloadPath // backward compat
		}
		renamer := library.NewRenamer(template, root)

		// Extract metadata from the filename if the download client didn't provide structured data.
		return renamer.Rename(record.FilePath, library.FileMeta{})
	}
}
