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
	deezer "github.com/ramonskie/groovearr/internal/providers/deezer"
	"github.com/ramonskie/groovearr/internal/providers/soulseek"
	coverartarchive "github.com/ramonskie/groovearr/internal/providers/coverartarchive"
	musicbrainz "github.com/ramonskie/groovearr/internal/providers/musicbrainz"
	dlsqlite "github.com/ramonskie/groovearr/internal/download/sqlite"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/sse"
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
	libStore, err := sqlite.New(dbPath)
	if err != nil {
		log.Fatalf("library db: %v", err)
	}
	defer libStore.Close()

	scanner := library.NewScanner(libStore)

	// Download store (SQLite — shares the library db connection).
	dlStore := dlsqlite.New(libStore.DB())

	// Build plugin registry.
	registry := download.NewRegistry()

	currentCfg := cfg.Get()

	// Ensure required directories exist.
	for _, p := range []string{currentCfg.Library.DownloadPath, currentCfg.Library.LibraryPath, currentCfg.Library.PlaylistPath} {
		if p != "" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				log.Printf("mkdir %s: %v", p, err)
			}
		}
	}

	// Register plugin factories.
	registry.RegisterFactory(soulseek.Factory)
	registry.RegisterFactory(deezer.Factory)

	// Initialize all plugins from config.
	resources := plugin.PluginResources{DownloadPath: currentCfg.Library.DownloadPath}
	if err := registry.InitAll(currentCfg.Sources, resources); err != nil {
		log.Printf("init plugins: %v", err)
	}

	// Event bus — decouples workers, importers, and SSE notifier.
	eventBus := events.NewInMemoryEventBus()

	// Download service — queues downloads and dispatches to workers.
	downloadSvc := download.NewDownloadService(dlStore, eventBus)

	// Worker pool — picks up queued downloads and drives state machine.
	workerPool := download.NewWorkerPool(0, registry, dlStore, eventBus)

	// Wire the pipeline: DownloadService → WorkerPool.
	downloadSvc.SetWorkerPool(workerPool)

	// Build the import handler chain for completed downloads.
	renamerCfg := func() (template, root string) {
		c := cfg.Get()
		template = c.Library.FolderTemplate
		root = c.Library.LibraryPath
		if root == "" {
			root = c.Library.DownloadPath
		}
		return
	}

	folderTemplate, libraryRoot := renamerCfg()
	renamer := library.NewRenamer(folderTemplate, libraryRoot)

	// SSE hub — broadcasts real-time download progress to connected clients.
	sseHub := sse.NewSSEHub()
	hbCtx, hbCancel := context.WithCancel(context.Background())
	sseHub.StartHeartbeat(hbCtx)

	// SSE notifier subscribes to event bus topics and translates them into SSE
	// broadcasts. Also implemented as an ImportHandler for import-completed notifications.
	sseNotifier := sse.NewSSENotifier(sseHub, eventBus)

	// Metadata provider registry — enriches library with ISRC, genres,
	// cover art, and external IDs from free metadata services.
	mdRegistry := metadata.NewRegistry()
	mdRegistry.RegisterFactory(musicbrainz.Factory)
	mdRegistry.RegisterFactory(coverartarchive.Factory)
	if err := mdRegistry.InitAll(currentCfg.Sources, resources); err != nil {
		log.Printf("warning: some metadata providers failed to initialize: %v", err)
	}
	for _, name := range mdRegistry.Names() {
		p := mdRegistry.Get(name)
		if p != nil && p.IsConfigured() {
			log.Printf("  metadata:  %s", name)
		}
	}

	// Completed download service subscribes to TopicDownloadCompleted on the
	// event bus and runs import handlers sequentially on each download.
	download.NewCompletedDownloadService(
		dlStore,
		eventBus,
		download.NewFileRenamerHandler(renamer, dlStore),
		download.NewCoverArtHandler(libStore),
		download.NewTagWriterHandler(),
		download.NewLibraryImporterHandler(libStore),
		download.NewMetadataEnrichmentHandler(mdRegistry, libStore),
		download.NewPlaylistLinkerHandler(libStore),
		sseNotifier,
	)

	// Playlist service — auto-register plugins that provide playlist sources.
	playlistReg := playlist.NewRegistry()
	for _, p := range registry.All() {
		if psp, ok := p.(playlist.PlaylistSourceProvider); ok {
			if p.IsConfigured() {
				playlistReg.Register(psp.PlaylistSource())
			}
		}
	}
	playlistSvc := playlist.NewService(playlistReg, libStore, registry, downloadSvc, func() config.Config {
		return cfg.Get()
	})

	// HTTP server.
	addr := os.Getenv("GROOVEARR_ADDR")
	if addr == "" {
		addr = ":8008"
	}

	srv := api.NewServer(addr, cfg, registry, downloadSvc, libStore, scanner, playlistSvc, eventBus, sseHub)

	log.Printf("groovearr starting")
	log.Printf("  config:   %s", configPath)
	log.Printf("  database: %s", dbPath)
	log.Printf("  download: %s", currentCfg.Library.DownloadPath)
	log.Printf("  library:  %s", currentCfg.Library.LibraryPath)
	log.Printf("  listening on %s", addr)
	for _, name := range registry.Names() {
		if p := registry.Get(name); p != nil {
			log.Printf("  source:   %s", p.DisplayName())
		}
	}
	for _, src := range playlistReg.Configured() {
		log.Printf("  playlist: %s (%s)", src.Name(), src.DisplayName())
	}
	if len(playlistReg.Configured()) == 0 && len(registry.Names()) > 0 {
		log.Printf("  playlist: no sources configured (add ARL token to sources.deezer)")
	}

	// Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		hbCancel()
		workerPool.Shutdown()
		srv.Shutdown(context.Background())
	}()

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
