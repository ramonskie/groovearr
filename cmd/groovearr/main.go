package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ramonskie/groovearr/internal/api"
	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/logger"
	deezer "github.com/ramonskie/groovearr/internal/providers/deezer"
	"github.com/ramonskie/groovearr/internal/providers/soulseek"
	coverartarchive "github.com/ramonskie/groovearr/internal/providers/coverartarchive"
	"github.com/ramonskie/groovearr/internal/providers/discogs"
	"github.com/ramonskie/groovearr/internal/providers/lastfm"
	musicbrainz "github.com/ramonskie/groovearr/internal/providers/musicbrainz"
	"github.com/ramonskie/groovearr/internal/providers/spotify"
	dlsqlite "github.com/ramonskie/groovearr/internal/download/sqlite"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/quality"
	"github.com/ramonskie/groovearr/internal/sse"
)

func main() {
	// Load config.
	configPath := os.Getenv("GROOVEARR_CONFIG")
	if configPath == "" {
		configPath = "./config.json"
	}

	mainLog := logger.NewDefault()

	cfg, err := config.LoadOrCreate(configPath)
	if err != nil {
		mainLog.Error("config init failed", "error", err, "component", "main")
		os.Exit(1)
	}

	cfg.SetLogger(mainLog)

	// Library store (SQLite).
	dbPath := filepath.Join(filepath.Dir(configPath), "library.db")
	libStore, err := sqlite.New(dbPath, mainLog)
	if err != nil {
		mainLog.Error("library db init failed", "error", err, "component", "main")
		os.Exit(1)
	}
	defer libStore.Close()

	scanner := library.NewScanner(libStore, mainLog)

	// Download store (SQLite — shares the library db connection).
	dlStore := dlsqlite.New(libStore.DB(), mainLog)

	// Plugin registry — one shared instance for all capability domains.
	// Factories declare capabilities (e.g. "download", "metadata", "playlist");
	// typed wrappers filter by capability for domain-specific access.
	pluginReg := plugin.NewRegistry()

	currentCfg := cfg.Get()

	// Ensure required directories exist.
	for _, p := range []string{currentCfg.Library.DownloadPath, currentCfg.Library.LibraryPath, currentCfg.Library.PlaylistPath} {
		if p != "" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				mainLog.Warn("mkdir failed", "path", p, "error", err, "component", "main")
			}
		}
	}

	// Register all plugin factories.
	pluginReg.RegisterFactory(soulseek.Factory)
	pluginReg.RegisterFactory(deezer.Factory)
	pluginReg.RegisterFactory(musicbrainz.Factory)
	pluginReg.RegisterFactory(coverartarchive.Factory)
	pluginReg.RegisterFactory(spotify.Factory)
	pluginReg.RegisterFactory(discogs.Factory)
	pluginReg.RegisterFactory(lastfm.Factory)

	// Initialize all plugins from config.
	resources := plugin.PluginResources{DownloadPath: currentCfg.Library.DownloadPath, Logger: mainLog}
	if err := pluginReg.InitAll(currentCfg.Sources, resources); err != nil {
		mainLog.Error("init plugins failed", "error", err, "component", "main")
	}
	// Create plugins not in config file using their defaults.
	pluginReg.InitRemaining(resources)

	// Persist factory defaults back to disk so users can see all available settings.
	mergedSources := pluginReg.FillDefaults(currentCfg.Sources)
	if err := cfg.Update(func(c *config.Config) error {
		c.Sources = mergedSources
		return nil
	}); err != nil {
		mainLog.Warn("failed to persist source defaults to config", "error", err, "component", "main")
	}

	// Typed registries share the same inner plugin.Registry.
	registry := download.NewRegistryFrom(pluginReg)
	mdRegistry := metadata.NewRegistryFrom(pluginReg)
	discoveryReg := discovery.NewRegistry(pluginReg)

	// Metadata resolver enriches track metadata (cover art, album info) at queue time.
	metadataResolver := metadata.NewMetadataResolver(mdRegistry, mainLog)
	metadataResolver.SetProviderOrder(currentCfg.MetadataOrder)

	// Plugin health checker — periodically verifies connectivity of all plugins.
	// Runs at startup and every 5 minutes thereafter.
	healthChecker := plugin.NewHealthChecker(pluginReg, 5*time.Minute, mainLog)
	healthChecker.Start(context.Background())

	// Event bus — decouples workers, importers, and SSE notifier.
	eventBus := events.NewInMemoryEventBus(mainLog)

	// Monitoring service — scans DB for queued downloads and drives state machine.
	monitor := download.NewMonitoringService(dlStore, registry, eventBus, mainLog)

	// Download service — queues downloads. Dispatch handled by MonitoringService.
	downloadSvc := download.NewDownloadService(dlStore, eventBus, mainLog)
	downloadSvc.SetRegistry(registry)

	// Quality profile store (SQLite) — created early so DownloadService
	// can use it for search resolution during retries.
	qualityProfileStore := quality.NewSQLiteProfileStore(libStore.DB())
	downloadSvc.SetQualityProfileStore(qualityProfileStore)
	monitor.SetQualityProfileStore(qualityProfileStore)

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
	renamer := library.NewRenamer(folderTemplate, libraryRoot, mainLog)

	enrichmentHandler := download.NewMetadataEnrichmentHandler(mdRegistry, discoveryReg, libStore, mainLog)
	enrichmentHandler.SetProviderOrder(currentCfg.MetadataOrder)

	// SSE hub — broadcasts real-time download progress to connected clients.
	sseHub := sse.NewSSEHub(mainLog)
	bgCtx, bgCancel := context.WithCancel(context.Background())
	sseHub.StartHeartbeat(bgCtx)

	// SSE notifier subscribes to event bus topics and translates them into SSE
	// broadcasts. Also implemented as an ImportHandler for import-completed notifications.
	sseNotifier := sse.NewSSENotifier(sseHub, eventBus, mainLog)

	// Completed download service subscribes to TopicDownloadCompleted on the
	// event bus and runs import handlers sequentially on each download.
	download.NewCompletedDownloadService(
		dlStore,
		eventBus,
		mainLog,
		download.NewFileRenamerHandler(renamer, dlStore, mainLog),
		download.NewCoverArtHandler(libStore, mainLog),
		download.NewTagWriterHandler(mainLog),
		download.NewLibraryImporterHandler(libStore, mainLog),
		enrichmentHandler,
		download.NewPlaylistLinkerHandler(libStore, mainLog),
		sseNotifier,
	)

	// Playlist service — auto-register plugins that provide playlist sources.
	playlistReg := playlist.NewRegistry()
		for _, p := range registry.All() {
			if psp, ok := p.(playlist.PlaylistSourceProvider); ok {
				if p.IsConfigured() {
					if ps := psp.PlaylistSource(); ps != nil {
						playlistReg.Register(ps)
					}
				}
			}
		}
	playlistSvc := playlist.NewService(playlistReg, libStore, registry, downloadSvc, func() config.Config {
		return cfg.Get()
	}, qualityProfileStore, metadataResolver, mainLog)

	// Start playlist retry worker — periodically scans for retryable playlist
	// downloads (e.g., expired tokens) and re-attempts resolution.
	playlistSvc.StartRetryWorker(bgCtx, 1*time.Minute)

	// Start monitoring service — recovers orphans, resolves pending sources,
	// and starts the main polling loop for downloads at 1-second intervals.
	monitor.Start(bgCtx)

	// Download orchestrator for search and download-best selection.
	orch := download.NewOrchestrator(registry, mainLog)
	orch.SetDownloadOrder(currentCfg.DownloadOrder)

	// HTTP server.
	addr := os.Getenv("GROOVEARR_ADDR")
	if addr == "" {
		addr = ":8008"
	}

	srv := api.NewServer(addr, mainLog, cfg, registry, mdRegistry, discoveryReg, downloadSvc, libStore, scanner, playlistSvc, qualityProfileStore, eventBus, sseHub, metadataResolver, enrichmentHandler, orch,
		func(mux *http.ServeMux) {
			spotify.RegisterOAuthRoutes(mux, cfg, mainLog, func(name string, rawCfg json.RawMessage) error {
				res := plugin.PluginResources{DownloadPath: cfg.Get().Library.DownloadPath, Logger: mainLog}
				if err := registry.Rebuild(name, rawCfg, res); err != nil {
					return err
				}
				// Refresh playlist sources so the playlist service picks up the rebuilt
				// plugin with its new OAuth tokens.
				if playlistSvc != nil {
					playlistSvc.RefreshSources(registry)
				}
				return nil
			}, func(name string) {
				if p := registry.Get(name); p != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					p.CheckConnection(ctx)
				}
			})
		},
	)

	mainLog.Info("groovearr starting",
		"config", configPath,
		"database", dbPath,
		"download", currentCfg.Library.DownloadPath,
		"library", currentCfg.Library.LibraryPath,
		"addr", addr,
		"component", "main",
	)
	for _, name := range registry.Names() {
		if p := registry.Get(name); p != nil {
			mainLog.Info("source configured", "name", name, "display", p.DisplayName(), "component", "main")
		}
	}
	for _, name := range mdRegistry.Names() {
		if p := mdRegistry.Get(name); p != nil && p.IsConfigured() {
			mainLog.Info("metadata configured", "name", name, "display", p.DisplayName(), "component", "main")
		}
	}
	for _, src := range playlistReg.Configured() {
		mainLog.Info("playlist configured", "name", src.Name(), "display", src.DisplayName(), "component", "main")
	}
	if len(playlistReg.Configured()) == 0 && len(registry.Names()) > 0 {
		mainLog.Info("playlist: no sources configured", "hint", "add ARL token to sources.deezer", "component", "main")
	}

	// Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		mainLog.Info("shutting down", "component", "main")
		mainLog.Info("background workers stopping", "component", "main")
		bgCancel()
		mainLog.Info("monitoring service shutting down", "component", "main")
		monitor.Shutdown()
		mainLog.Info("server stopped", "component", "main")
		srv.Shutdown(context.Background())
	}()

	if err := srv.ListenAndServe(); err != nil {
		mainLog.Error("server failed", "error", err, "component", "main")
		os.Exit(1)
	}
}
