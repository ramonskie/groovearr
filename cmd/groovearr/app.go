package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ramonskie/groovearr/internal/api"
	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	dlsqlite "github.com/ramonskie/groovearr/internal/download/sqlite"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/logger"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/plugin"
	coverartarchive "github.com/ramonskie/groovearr/internal/providers/coverartarchive"
	deezer "github.com/ramonskie/groovearr/internal/providers/deezer"
	"github.com/ramonskie/groovearr/internal/providers/discogs"
	"github.com/ramonskie/groovearr/internal/providers/lastfm"
	musicbrainz "github.com/ramonskie/groovearr/internal/providers/musicbrainz"
	"github.com/ramonskie/groovearr/internal/providers/prowlarr"
	"github.com/ramonskie/groovearr/internal/providers/qbittorrent"
	"github.com/ramonskie/groovearr/internal/providers/soulseek"
	"github.com/ramonskie/groovearr/internal/providers/spotify"
	"github.com/ramonskie/groovearr/internal/quality"
	"github.com/ramonskie/groovearr/internal/sse"
)

// App holds all initialized application components.
type App struct {
	log      *slog.Logger
	cfg      *config.Persistence
	libStore *sqlite.Store

	monitor *download.MonitoringService
	srv     *api.Server
	bgCtx   context.Context
	bgCancel context.CancelFunc

	// Fields needed for startup logging.
	configPath        string
	dbPath            string
	registry          *download.Registry
	mdRegistry        *metadata.Registry
	downloadClientReg *download.DownloadClientRegistry
	playlistReg       *playlist.Registry
}

// NewApp initializes all application components from the given config path.
func NewApp(configPath string) (*App, error) {
	log := logger.NewDefault()

	// Load config.
	cfg, err := config.LoadOrCreate(configPath)
	if err != nil {
		log.Error("config init failed", "error", err, "component", "main")
		return nil, err
	}
	cfg.SetLogger(log)

	// Library store (SQLite).
	dbPath := filepath.Join(filepath.Dir(configPath), "library.db")
	libStore, err := sqlite.New(dbPath, log)
	if err != nil {
		log.Error("library db init failed", "error", err, "component", "main")
		return nil, err
	}
	// libStore.Close() is called in App.Run() after the server stops.

	scanner := library.NewScanner(libStore, log)

	// Download store (SQLite — shares the library db connection).
	dlStore := dlsqlite.New(libStore.DB(), log)

	// Plugin registry.
	pluginReg := plugin.NewRegistry()

	currentCfg := cfg.Get()

	// Ensure required directories exist.
	for _, p := range []string{currentCfg.Library.DownloadPath, currentCfg.Library.LibraryPath, currentCfg.Library.PlaylistPath} {
		if p != "" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				log.Warn("mkdir failed", "path", p, "error", err, "component", "main")
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
	pluginReg.RegisterFactory(prowlarr.Factory)
	pluginReg.RegisterFactory(qbittorrent.Factory)

	// Initialize all plugins from config.
	resources := plugin.PluginResources{DownloadPath: currentCfg.Library.DownloadPath, Logger: log}
	if err := pluginReg.InitAll(currentCfg.Sources, resources); err != nil {
		log.Error("init plugins failed", "error", err, "component", "main")
	}
	pluginReg.InitRemaining(resources)

	// Persist factory defaults to disk.
	mergedSources := pluginReg.FillDefaults(currentCfg.Sources)
	if err := cfg.Update(func(c *config.Config) error {
		c.Sources = mergedSources
		return nil
	}); err != nil {
		log.Warn("failed to persist source defaults to config", "error", err, "component", "main")
	}

	// Typed registries.
	registry := download.NewRegistryFrom(pluginReg)
	mdRegistry := metadata.NewRegistryFrom(pluginReg)
	discoveryReg := discovery.NewRegistry(pluginReg)

	// Metadata resolver.
	metadataResolver := metadata.NewMetadataResolver(mdRegistry, log)
	metadataResolver.SetProviderOrder(currentCfg.MetadataOrder)

	// Plugin health checker.
	healthChecker := plugin.NewHealthChecker(pluginReg, 5*time.Minute, log)
	healthChecker.Start(context.Background())

	// Event bus.
	eventBus := events.NewInMemoryEventBus(log)

	// Download client registry.
	downloadClientReg := download.NewDownloadClientRegistry(pluginReg)

	// Monitoring service.
	monitor := download.NewMonitoringService(dlStore, registry, downloadClientReg, currentCfg.Library.DownloadPath, eventBus, log)

	// Download service.
	downloadSvc := download.NewService(dlStore, eventBus, log)
	downloadSvc.SetRegistry(registry)
	downloadSvc.SetDownloadClientRegistry(downloadClientReg)

	// Quality profile store.
	qualityProfileStore := quality.NewSQLiteProfileStore(libStore.DB())
	downloadSvc.SetQualityProfileStore(qualityProfileStore)
	monitor.SetQualityProfileStore(qualityProfileStore)
	monitor.SetDownloadPathFunc(func() string { return cfg.Get().Library.DownloadPath })

	// Renamer.
	folderTemplate, libraryRoot := readRenamerConfig(cfg)
	renamer := library.NewRenamer(folderTemplate, libraryRoot, log)

	// SSE hub + notifier.
	sseHub := sse.NewSSEHub(log)
	bgCtx, bgCancel := context.WithCancel(context.Background())
	sseHub.StartHeartbeat(bgCtx)
	sseNotifier := sse.NewSSENotifier(sseHub, eventBus, log)

	// Import handler chain.
	enrichmentHandler := download.NewMetadataEnrichmentHandler(mdRegistry, discoveryReg, libStore, log)
	enrichmentHandler.SetProviderOrder(currentCfg.MetadataOrder)

	importChain := []download.ImportHandler{
		download.NewFileRenamerHandler(renamer, dlStore, log),
		download.NewCoverArtHandler(libStore, log),
		download.NewTagWriterHandler(log),
		download.NewLibraryImporterHandler(libStore, log),
		enrichmentHandler,
		download.NewPlaylistLinkerHandler(libStore, log),
		sseNotifier,
	}

	trackResolver := newTrackResolver(pluginReg)
	albumImportHandler := download.NewAlbumImportHandler(importChain, trackResolver, dlStore, libStore, libStore.DB(), log)

	download.NewCompletedDownloadService(dlStore, eventBus, log, albumImportHandler, importChain...)

	// Playlist service.
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
	}, qualityProfileStore, metadataResolver, log)

	playlistSvc.StartRetryWorker(bgCtx, 1*time.Minute)
	if syncMins := currentCfg.Library.PlaylistAutoSyncMins; syncMins != nil && *syncMins >= 5 {
		playlistSvc.StartAutoSyncWorker(bgCtx, time.Duration(*syncMins)*time.Minute)
	} else {
		log.Info("playlist auto-sync disabled (set library.playlist_auto_sync_mins to 5+ to enable)", "component", "main")
	}

	monitor.Start(bgCtx)

	// Download orchestrator.
	orch := download.NewOrchestrator(registry, log)
	orch.SetDownloadOrder(currentCfg.DownloadOrder)
	orch.SetAlbumSources(currentCfg.AlbumSources)

	// HTTP server.
	addr := os.Getenv("GROOVEARR_ADDR")
	if addr == "" {
		addr = ":8008"
	}

	srv := api.NewServer(addr, bgCtx, log, cfg, registry, mdRegistry, discoveryReg, downloadSvc, libStore, scanner, playlistSvc, qualityProfileStore, eventBus, sseHub, metadataResolver, enrichmentHandler, orch,
		func(mux *http.ServeMux) {
			spotify.RegisterOAuthRoutes(mux, cfg, log, func(name string, rawCfg json.RawMessage) error {
				res := plugin.PluginResources{DownloadPath: cfg.Get().Library.DownloadPath, Logger: log}
				if err := registry.Rebuild(name, rawCfg, res); err != nil {
					return err
				}
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

	// Startup logging.
	log.Info("groovearr starting",
		"config", configPath,
		"database", dbPath,
		"download", currentCfg.Library.DownloadPath,
		"library", currentCfg.Library.LibraryPath,
		"addr", addr,
		"component", "main",
	)
	for _, name := range registry.Names() {
		if p := registry.Get(name); p != nil {
			log.Info("source configured", "name", name, "display", p.DisplayName(), "component", "main")
		}
	}
	for _, name := range mdRegistry.Names() {
		if p := mdRegistry.Get(name); p != nil && p.IsConfigured() {
			log.Info("metadata configured", "name", name, "display", p.DisplayName(), "component", "main")
		}
	}
	if dc := downloadClientReg.Get(currentCfg.DownloadClient); dc != nil {
		log.Info("download client configured", "name", currentCfg.DownloadClient, "display", dc.DisplayName(), "component", "main")
	}
	if currentCfg.DownloadClient != "" && downloadClientReg.Get(currentCfg.DownloadClient) == nil {
		log.Warn("download client not found", "name", currentCfg.DownloadClient, "component", "main")
	}
	for _, src := range playlistReg.Configured() {
		log.Info("playlist configured", "name", src.Name(), "display", src.DisplayName(), "component", "main")
	}
	if len(playlistReg.Configured()) == 0 && len(registry.Names()) > 0 {
		log.Info("playlist: no sources configured", "hint", "add ARL token to sources.deezer", "component", "main")
	}

	return &App{
		log:               log,
		cfg:               cfg,
		libStore:          libStore,
		monitor:           monitor,
		srv:               srv,
		bgCtx:             bgCtx,
		bgCancel:          bgCancel,
		configPath:        configPath,
		dbPath:            dbPath,
		registry:          registry,
		mdRegistry:        mdRegistry,
		downloadClientReg: downloadClientReg,
		playlistReg:       playlistReg,
	}, nil
}

// Run starts the HTTP server and blocks until a shutdown signal is received.
func (app *App) Run() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		app.log.Info("shutting down", "component", "main")
		app.log.Info("background workers stopping", "component", "main")
		app.bgCancel()
		app.log.Info("monitoring service shutting down", "component", "main")
		app.monitor.Shutdown()
		app.log.Info("server stopped", "component", "main")
		app.srv.Shutdown(context.Background())
	}()

	if err := app.srv.ListenAndServe(); err != nil {
		app.log.Error("server failed", "error", err, "component", "main")
		app.libStore.Close()
		os.Exit(1)
	}
	app.libStore.Close()
}

// readRenamerConfig reads the folder template and library root from config.
func readRenamerConfig(cfg *config.Persistence) (template, root string) {
	c := cfg.Get()
	template = c.Library.FolderTemplate
	root = c.Library.LibraryPath
	if root == "" {
		root = c.Library.DownloadPath
	}
	return
}

// newTrackResolver creates the track resolver closure used by AlbumImportHandler.
func newTrackResolver(pluginReg *plugin.Registry) func(ctx context.Context, sourceName, artist, album string, fileCount int, torrentTitle string) ([]domain.ExpectedTrack, string, error) {
	return func(ctx context.Context, sourceName, artist, album string, fileCount int, torrentTitle string) ([]domain.ExpectedTrack, string, error) {
		bp := pluginReg.Get(sourceName)
		if bp == nil {
			return nil, "", fmt.Errorf("album provider %q not found", sourceName)
		}
		ap, ok := bp.(download.AlbumProvider)
		if !ok {
			return nil, "", fmt.Errorf("source %q is not an AlbumProvider", sourceName)
		}
		release := domain.AlbumRelease{SourceName: sourceName, Artist: artist, Album: album}
		return ap.ResolveTracksForCount(ctx, release, fileCount, torrentTitle)
	}
}
