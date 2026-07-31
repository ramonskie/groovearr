package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ramonskie/groovearr"
	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/quality"
	"github.com/ramonskie/groovearr/internal/sse"
)

// Server holds all dependencies for HTTP handlers.
type Server struct {
	cfg                 *config.Persistence
	registry            *download.Registry
	mdRegistry          *metadata.Registry
	metadataResolver    *metadata.MetadataResolver
	enrichmentHandler   *download.MetadataEnrichmentHandler
	orchestrator        *download.Orchestrator
	discoveryReg        *discovery.Registry
	store               library.Store
	scanner             *library.Scanner
	downloadSvc         *download.Service
	eventBus            events.IEventAggregator
	sseHub              *sse.SSEHub
	matcher             *matching.Engine
	playlistSvc         *playlist.Service
	qualityProfileStore quality.ProfileStore
	httpSrv             *http.Server
	log                 *slog.Logger
	rateLimiter         *ipRateLimiter
	sessions            *sessionStore
}

// PluginRouteRegistrar is called after all standard routes are registered,
// giving plugins a chance to add their own HTTP endpoints.
type PluginRouteRegistrar func(mux *http.ServeMux)

func NewServer(addr string, logger *slog.Logger, cfg *config.Persistence, registry *download.Registry, mdRegistry *metadata.Registry, discoveryReg *discovery.Registry, downloadSvc *download.Service, store library.Store, scanner *library.Scanner, playlistSvc *playlist.Service, qualityProfileStore quality.ProfileStore, eventBus events.IEventAggregator, sseHub *sse.SSEHub, metadataResolver *metadata.MetadataResolver, enrichmentHandler *download.MetadataEnrichmentHandler, orchestrator *download.Orchestrator, pluginRoutes ...PluginRouteRegistrar) *Server {
	s := &Server{
		cfg:                 cfg,
		registry:            registry,
		mdRegistry:          mdRegistry,
		metadataResolver:    metadataResolver,
		enrichmentHandler:   enrichmentHandler,
		orchestrator:        orchestrator,
		discoveryReg:        discoveryReg,
		store:               store,
		scanner:             scanner,
		downloadSvc:         downloadSvc,
		eventBus:            eventBus,
		sseHub:              sseHub,
		matcher:             matching.New(),
		playlistSvc:         playlistSvc,
		qualityProfileStore: qualityProfileStore,
		log:                 logger,
		rateLimiter:         newIPRateLimiter(defaultRateBuckets(), logger),
		sessions:            newSessionStore(),
	}

	mux := http.NewServeMux()

	// Web UI — serve embedded static files with no-cache for development.
	// The embedded files are produced by `make build-ui` (Vite).  If the ui/dist/
	// directory is empty or missing, the Go binary was built without the UI.
	staticContent, err := fs.Sub(groovearr.UIFiles, "ui/dist")
	if err != nil {
		s.log.Error("embedded UI files missing", "error", err, "component", "api")
		os.Exit(1)
	}

	// SPA-aware static file server — serves embedded files, falls back to
	// index.html for client-side routes (e.g. /settings, /playlists).
	fileServer := http.FileServer(http.FS(staticContent))
	mux.Handle("GET /", noCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if the requested path exists as a file.
		fsPath := strings.TrimPrefix(r.URL.Path, "/")
		if fsPath == "" {
			fsPath = "."
		}
		if f, err := staticContent.Open(fsPath); err != nil {
			// Not a file — serve index.html for SPA client-side routing.
			r.URL.Path = "/"
		} else {
			f.Close()
		}
		fileServer.ServeHTTP(w, r)
	})))

	// API routes.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.Handle("POST /api/login", withRateLimit("login", s.rateLimiter, http.HandlerFunc(s.handleLogin)))
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handleUpdateConfig)
	mux.HandleFunc("GET /api/config/sources", s.handleGetSources)
	mux.HandleFunc("POST /api/config/test/{source}", s.handleTestConnection)
	mux.Handle("POST /api/search", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleSearch)))
	mux.Handle("POST /api/albums/search", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleAlbumSearch)))
	mux.Handle("POST /api/albums/download-best", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleAlbumDownloadBest)))
	mux.Handle("POST /api/download", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleDownload)))
	mux.Handle("POST /api/download/match", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleDownloadBest)))
	mux.HandleFunc("GET /api/downloads", s.handleGetDownloads)
	mux.HandleFunc("DELETE /api/downloads/{id}", s.handleCancelDownload)
	mux.Handle("POST /api/downloads/{id}/retry", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleRetryDownload)))
	mux.HandleFunc("GET /api/library/tracks", s.handleLibraryTracks)
	mux.HandleFunc("GET /api/library/artists", s.handleLibraryArtists)
	mux.HandleFunc("GET /api/library/albums", s.handleLibraryAlbums)
	mux.Handle("POST /api/library/scan", withRateLimit("scan", s.rateLimiter, http.HandlerFunc(s.handleLibraryScan)))
	mux.HandleFunc("GET /api/library/artists/{artistID}", s.handleLibraryArtist)
	mux.HandleFunc("GET /api/library/artists/{artistID}/albums", s.handleLibraryArtistAlbums)
	mux.HandleFunc("GET /api/library/artists/{artistID}/tracks", s.handleLibraryArtistTracks)
	mux.HandleFunc("GET /api/covers/{albumID}", s.handleCoverArt)
	mux.HandleFunc("GET /api/artist-image/{artistID}", s.handleArtistImage)
	mux.Handle("GET /api/library/albums/{albumID}/discovery", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleLibraryAlbumDiscovery)))
	mux.Handle("POST /api/library/albums/{albumID}/download-missing", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleLibraryAlbumDownloadMissing)))

	// Playlist routes.
	mux.HandleFunc("GET /api/playlists/sources", s.handlePlaylistSources)
	mux.HandleFunc("GET /api/playlists/sources/{source}", s.handlePlaylistSourceBrowse)
	mux.HandleFunc("GET /api/playlists", s.handleListPlaylists)
	mux.HandleFunc("GET /api/playlists/{id}", s.handleGetPlaylist)
	mux.HandleFunc("PATCH /api/playlists/{id}", s.handleUpdatePlaylist)
	mux.Handle("POST /api/playlists/import", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleImportPlaylist)))
	mux.Handle("POST /api/playlists/{id}/download-missing", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleDownloadMissing)))
	mux.Handle("POST /api/playlists/{id}/sync", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleSyncPlaylist)))
	mux.HandleFunc("DELETE /api/playlists/{id}", s.handleDeletePlaylist)

	// Discovery routes — metadata-first album/track browsing.
	mux.HandleFunc("GET /api/discover/providers", s.handleDiscoverProviders)
	mux.Handle("GET /api/discover/search", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleDiscoverSearch)))
	mux.Handle("GET /api/discover/artists/resolve", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleDiscoverResolveArtist)))
	mux.Handle("GET /api/discover/artists/overview", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleDiscoverArtistOverview)))
	mux.HandleFunc("GET /api/discover/artists/{id}/albums", s.handleDiscoverArtistAlbums)
	mux.HandleFunc("GET /api/discover/albums/{id}/tracks", s.handleDiscoverAlbumTracks)
	mux.Handle("POST /api/discover/albums/{id}/download", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleDiscoverAlbumDownload)))

	// Quality Profiles — presets MUST come before /{id} to avoid matching "presets" as id.
	mux.HandleFunc("GET /api/quality-profiles/presets", s.handleQualityProfilePresets)
	mux.HandleFunc("POST /api/quality-profiles/apply-preset", s.handleApplyQualityProfilePreset)
	mux.HandleFunc("GET /api/quality-profiles", s.handleListQualityProfiles)
	mux.HandleFunc("POST /api/quality-profiles", s.handleCreateQualityProfile)
	mux.HandleFunc("GET /api/quality-profiles/{id}", s.handleGetQualityProfile)
	mux.HandleFunc("PUT /api/quality-profiles/{id}", s.handleUpdateQualityProfile)
	mux.HandleFunc("DELETE /api/quality-profiles/{id}", s.handleDeleteQualityProfile)
	mux.HandleFunc("PUT /api/quality-profiles/{id}/default", s.handleSetDefaultQualityProfile)

	// SSE endpoint for real-time download progress.
	mux.HandleFunc("GET /api/events", s.handleEvents)

	// Debug endpoint — full download state for troubleshooting.
	mux.HandleFunc("GET /api/debug/download/{id}", s.handleDebugDownload)

	// Let plugins register their own routes.
	for _, register := range pluginRoutes {
		register(mux)
	}

	s.httpSrv = &http.Server{
		Addr:         addr,
		Handler:      withLogging(s.log)(withRequestID(withCORS(s.withAuth(mux)))),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	return s
}

// ListenAndServe starts the HTTP server (blocking).
func (s *Server) ListenAndServe() error {
	s.log.Info("listening", "addr", s.httpSrv.Addr, "component", "api")
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.rateLimiter.Shutdown()
	s.sessions.Shutdown()
	return s.httpSrv.Shutdown(ctx)
}

// ─── Middleware ──────────────────────────────────────────────────────

func withLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wr := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(wr, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", wr.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"component", "api",
			)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher so SSE connections work through the logging middleware.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type requestIDKey struct{}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
			if len(id) > 8 {
				id = id[:8]
			}
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RequestIDFromCtx(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Api-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Handlers ────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Get().Mask())
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var partial config.Config
	if err := json.NewDecoder(r.Body).Decode(&partial); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Snapshot old sources before update to skip rebuilding unchanged plugins.
	// Deep-copy — maps are reference types, and cfg.Merge mutates in-place,
	// which would corrupt the old-vs-new comparison.
	cfgSnapshot := s.cfg.Get()
	oldSources := make(map[string]json.RawMessage, len(cfgSnapshot.Sources))
	for k, v := range cfgSnapshot.Sources {
		oldSources[k] = append([]byte(nil), v...)
	}

	err := s.cfg.Update(func(cfg *config.Config) error {
		// Merge partial onto a copy first to validate, then apply to live config.
		merged := *cfg
		merged.Merge(&partial)

		if errs := merged.Validate(); len(errs) > 0 {
			return &validationError{errs}
		}

		cfg.Merge(&partial)
		return nil
	})

	if err != nil {
		if ve, ok := err.(*validationError); ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":  "validation failed",
				"errors": ve.Errors,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Rebuild only plugins whose config changed.
	updated := s.cfg.Get()
	resources := plugin.PluginResources{DownloadPath: updated.Library.DownloadPath, Logger: s.log}
	var rebuilt []string
	for name, newCfg := range updated.Sources {
		oldCfg, existed := oldSources[name]
		if existed && bytes.Equal(oldCfg, newCfg) {
			continue
		}
		if err := s.registry.Rebuild(name, newCfg, resources); err != nil {
			s.log.Error("reload failed", "name", name, "error", err, "component", "api")
			continue
		}
		rebuilt = append(rebuilt, name)
	}

	// Re-check connectivity on rebuilt plugins so badges reflect current
	// state without waiting for the periodic health checker (every 5 min).
	if len(rebuilt) > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, name := range rebuilt {
				if p := s.registry.Get(name); p != nil && p.IsConfigured() {
					if err := p.CheckConnection(ctx); err != nil {
						s.log.Debug("post-rebuild connection check failed", "name", name, "error", err, "component", "api")
					}
				} else if bp := s.registry.Inner().Get(name); bp != nil && bp.IsConfigured() {
					if err := bp.CheckConnection(ctx); err != nil {
						s.log.Debug("post-rebuild connection check failed", "name", name, "error", err, "component", "api")
					}
				}
			}
		}()
	}

	// Sync metadata order with available providers before applying.
	// This ensures newly-configured providers (like Spotify dev mode)
	// appear in the order without manual UI reordering.
	syncedMdOrder := mergeAvailableProviders(updated.MetadataOrder, s.mdRegistry.Available())
	if len(syncedMdOrder) > 0 {
		s.metadataResolver.SetProviderOrder(syncedMdOrder)
		if s.enrichmentHandler != nil {
			s.enrichmentHandler.SetProviderOrder(syncedMdOrder)
		}
		// Persist if the config order is stale (missing available providers).
		if !stringSlicesEqual(syncedMdOrder, updated.MetadataOrder) {
			_ = s.cfg.Update(func(cfg *config.Config) error {
				cfg.MetadataOrder = syncedMdOrder
				return nil
			})
		}
	}

	// Re-apply download source order, synced with connected providers.
	// Stale entries for disconnected providers (like Deezer without ARL)
	// are dropped from the config order on save.
	if s.orchestrator != nil {
		syncedDlOrder := connectedNames(s.registry.Configured(), updated.DownloadOrder)
		s.orchestrator.SetDownloadOrder(syncedDlOrder)
		if !stringSlicesEqual(syncedDlOrder, updated.DownloadOrder) {
			_ = s.cfg.Update(func(cfg *config.Config) error {
				cfg.DownloadOrder = syncedDlOrder
				return nil
			})
		}
	}

	// Re-register playlist sources from rebuilt plugins.
	if s.playlistSvc != nil {
		s.playlistSvc.RefreshSources(s.registry)
	}

	// Ensure required directories exist.
	for _, p := range []string{updated.Library.DownloadPath, updated.Library.LibraryPath} {
		if p != "" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				s.log.Warn("mkdir failed", "path", p, "error", err, "component", "api")
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// validationError carries config validation failures.
type validationError struct {
	Errors []string
}

func (e *validationError) Error() string { return "validation failed" }

func (s *Server) handleGetSources(w http.ResponseWriter, r *http.Request) {
	inner := s.registry.Inner()
	var sources []map[string]any
	seen := make(map[string]bool)

	// Enumerate plugins grouped by capability. Order determines section order
	// in the settings UI. Plugins listing multiple capabilities appear once.
	for _, cap := range []string{"download", "download_client", "metadata", "discovery", "album_search"} {
		for _, p := range inner.WithCapability(cap) {
			if seen[p.Name()] {
				continue
			}
			seen[p.Name()] = true
			schema := resolveSchema(inner, p.Name())
			enabled := true
			if enabler, ok := p.(plugin.Enabler); ok {
				enabled = enabler.IsEnabled()
			}
			sources = append(sources, sourceEntry(p.Name(), p.DisplayName(), p.IsConfigured(), p.Connected(), enabled, p.CapabilityStatus(), schema))
		}
	}

	writeJSON(w, http.StatusOK, sources)
}

// resolveSchema looks up the factory for a source name in the plugin registry
// and returns its ConfigSchemaProvider if the factory implements that interface.
func resolveSchema(reg *plugin.Registry, name string) plugin.ConfigSchemaProvider {
	if reg == nil {
		return nil
	}
	f := reg.Factory(name)
	if f == nil {
		return nil
	}
	if sp, ok := f.(plugin.ConfigSchemaProvider); ok {
		return sp
	}
	return nil
}

func sourceEntry(name, displayName string, configured, connected, enabled bool, caps map[string]string, schema plugin.ConfigSchemaProvider) map[string]any {
	status := "not_configured"
	if configured {
		status = "configured"
		if connected {
			status = "connected"
		}
	}
	entry := map[string]any{
		"name":         name,
		"display_name": displayName,
		"configured":   configured,
		"enabled":      enabled,
		"status":       status,
	}
	if len(caps) > 0 {
		entry["capabilities"] = caps
	}
	if schema != nil {
		entry["icon"] = schema.Icon()
		if fields := schema.ConfigSchema(); fields != nil {
			entry["config_schema"] = fields
		}
		if oa := schema.OAuthConfig(); oa != nil {
			entry["oauth"] = oa
		}
		if slots := schema.UISlots(); slots != nil {
			entry["ui_slots"] = slots
		}
	}
	return entry
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")

	// Check all registries.
	var p plugin.BasePlugin
	if dp := s.registry.Get(source); dp != nil {
		p = dp
	} else if mp := s.mdRegistry.Get(source); mp != nil {
		p = mp
	} else if s.discoveryReg != nil {
		for _, dp := range s.discoveryReg.Any() {
			if dp.Name() == source {
				p = dp
				break
			}
		}
	}
	// Fallback: inner plugin registry for capabilities without typed wrappers
	// (album_search, etc.). Type-asserted registries above are preferred paths.
	if p == nil {
		p = s.registry.Inner().Get(source)
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "source not found"})
		return
	}
	if !p.IsConfigured() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source not configured", "status": "not_configured"})
		return
	}

	err := p.CheckConnection(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"status": "configured", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "connected"})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query  string `json:"query"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query is required"})
		return
	}

	ctx := r.Context()

	// Resolve source: specific plugin, "hybrid" (all configured), or "" (first configured).
	var tracks []domain.TrackResult
	var albums []domain.AlbumResult
	var searchErr error

	if req.Source != "" && req.Source != "hybrid" {
		p := s.registry.Get(req.Source)
		if p == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("source %q not found", req.Source)})
			return
		}
		if !p.IsConfigured() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("source %q not configured", req.Source)})
			return
		}
		tracks, albums, searchErr = p.Search(ctx, req.Query)
	} else {
		plugins := s.registry.Configured()
		if len(plugins) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no download sources configured"})
			return
		}
		if req.Source == "hybrid" {
			for _, p := range plugins {
				t, a, err := p.Search(ctx, req.Query)
				if err != nil {
					s.log.Error("search failed", "provider", p.Name(), "error", err, "component", "api")
					continue
				}
				tracks = append(tracks, t...)
				albums = append(albums, a...)
			}
		} else {
			tracks, albums, searchErr = plugins[0].Search(ctx, req.Query)
		}
	}

	if searchErr != nil {
		writeError(w, http.StatusInternalServerError, searchErr)
		return
	}
	if tracks == nil {
		tracks = []domain.TrackResult{}
	}
	if albums == nil {
		albums = []domain.AlbumResult{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tracks": tracks,
		"albums": albums,
	})
}

func (s *Server) handleAlbumSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("query is required"))
		return
	}

	releases, err := s.orchestrator.SearchAlbums(r.Context(), req.Query)
	if err != nil {
		s.log.Warn("album search failed", "error", err, "component", "api")
		writeJSON(w, http.StatusOK, map[string]any{"releases": []any{}})
		return
	}
	if releases == nil {
		releases = []domain.AlbumRelease{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": releases})
}

// handleAlbumDownloadBest searches album sources, picks the best release,
// resolves tracks via MusicBrainz, and queues the album download.
// Request: {"artist":"Metallica","album":"Master of Puppets","download_client":"qbittorrent"}
func (s *Server) handleAlbumDownloadBest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Artist         string `json:"artist"`
		Album          string `json:"album"`
		DownloadClient string `json:"download_client"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Artist == "" || req.Album == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("artist and album are required"))
		return
	}
	if req.DownloadClient == "" {
		req.DownloadClient = s.cfg.Get().DownloadClient
	}
	if req.DownloadClient == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("no download_client configured"))
		return
	}

	query := req.Artist + " " + req.Album

	// 1. Search album sources for releases.
	releases, err := s.orchestrator.SearchAlbums(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("album search: %w", err))
		return
	}
	if len(releases) == 0 {
		writeError(w, http.StatusNotFound, fmt.Errorf("no album releases found for %q", query))
		return
	}

	// 2. Best release is always the first (sorted by seeders descending).
	best := releases[0]

	// 3. Queue the album download. Track resolution happens later,
	// after download, when AlbumImportHandler knows the actual file count.
	downloadID, err := s.downloadSvc.QueueAlbum(r.Context(), best, nil, req.DownloadClient)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("queue album: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"download_id": downloadID,
		"artist":      best.Artist,
		"album":       best.Album,
	})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source      string `json:"source"`
		Username    string `json:"username"`
		Filename    string `json:"filename"`
		Size        int64  `json:"size"`
		Artist      string `json:"artist,omitempty"`
		Album       string `json:"album,omitempty"`
		Title       string `json:"title,omitempty"`
		TrackNumber int    `json:"track_number,omitempty"`
		DiscNumber  int    `json:"disc_number,omitempty"`
		Year        int    `json:"year,omitempty"`
		Bitrate     int    `json:"bitrate,omitempty"`
		Quality     string `json:"quality,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()

	// Enrich missing metadata fields with resolver.
	artist := req.Artist
	album := req.Album
	year := req.Year
	var coverURL string
	if s.metadataResolver != nil {
		enrichCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		enriched, err := s.metadataResolver.EnrichMetadata(enrichCtx, artist, req.Title, album, year)
		cancel()
		if err == nil && enriched != nil {
			if enriched.CoverURL != "" {
				coverURL = enriched.CoverURL
			}
			if album == "" && enriched.Album != "" {
				album = enriched.Album
			}
			if year == 0 && enriched.Year > 0 {
				year = enriched.Year
			}
		}
	}

	id, err := s.downloadSvc.Queue(ctx, req.Source, req.Username, req.Filename, req.Size, download.Meta{
		Artist:      artist,
		Album:       album,
		Title:       req.Title,
		TrackNumber: req.TrackNumber,
		DiscNumber:  req.DiscNumber,
		Year:        year,
		Bitrate:     req.Bitrate,
		Format:      req.Quality,
		CoverURL:    coverURL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"download_id": id})
}

// handleDownloadBest searches across configured sources for a matching track
// and queues the best candidate via the download service.
func (s *Server) handleDownloadBest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title         string `json:"title"`
		Artist        string `json:"artist"`
		Album         string `json:"album,omitempty"` // optional album name for multi-query search
		Duration      int64  `json:"duration"`        // milliseconds (optional, 0 = neutral)
		ExcludeSource string `json:"exclude_source"`  // source to skip (the one that just failed)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}

	ctx := r.Context()
	orch := s.orchestrator
	if orch == nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("orchestrator not initialized"))
		return
	}

	defaultProfile, profileErr := s.qualityProfileStore.LoadProfileByID(ctx, nil)
	if profileErr != nil {
		s.log.Warn("failed to load default quality profile, downloads proceed unfiltered", "error", profileErr, "component", "api")
	}
	best, err := orch.FindBestMatch(ctx, req.Title, req.Artist, req.Album, req.Duration, req.ExcludeSource, defaultProfile)
	if err != nil {
		writeJSON(w, http.StatusNotFound, err)
		return
	}

	username := best.Track.Username

	// Collect metadata from search result before enrichment.
	artist := best.Track.Artist
	title := best.Track.Title
	album := best.Track.Album
	year := 0

	if best.Track.Metadata != nil {
		if artist == "" && best.Track.Metadata.Artist != "" {
			artist = best.Track.Metadata.Artist
		}
		if title == "" && best.Track.Metadata.Title != "" {
			title = best.Track.Metadata.Title
		}
	}

	// Enrich with metadata resolver for missing album/cover art.
	var coverURL string
	if s.metadataResolver != nil {
		enrichCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		enriched, err := s.metadataResolver.EnrichMetadata(enrichCtx, artist, title, album, year)
		cancel()
		if err == nil && enriched != nil {
			if enriched.CoverURL != "" {
				coverURL = enriched.CoverURL
			}
			if album == "" && enriched.Album != "" {
				album = enriched.Album
			}
			if year == 0 && enriched.Year > 0 {
				year = enriched.Year
			}
		}
	}

	id, err := s.downloadSvc.Queue(ctx, best.SourceName, username, best.Track.Filename, best.Track.Size, download.Meta{
		Artist:      artist,
		Album:       album,
		Title:       title,
		TrackNumber: best.Track.TrackNumber,
		Year:        year,
		Bitrate:     best.Track.Bitrate,
		Format:      best.Track.Quality,
		CoverURL:    coverURL,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("queue download: %v", err)})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"download_id": id,
		"source":      best.SourceName,
		"confidence":  best.Score,
	})
}

func (s *Server) handleGetDownloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stateParam := r.URL.Query().Get("state")

	var downloads []download.Record
	var err error

	switch stateParam {
	case "":
		downloads, err = s.downloadSvc.List(ctx)
	case "active":
		downloads, err = s.downloadSvc.ListActive(ctx)
	default:
		downloads, err = s.downloadSvc.ListByState(ctx, download.State(stateParam))
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if downloads == nil {
		downloads = []download.Record{}
	}
	writeJSON(w, http.StatusOK, downloads)
}

func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.downloadSvc.Cancel(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// handleRetryDownload manually retries a failed or failedPending download.
func (s *Server) handleRetryDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Check if the download exists and determine how to retry it.
	rec, err := s.downloadSvc.GetStatus(r.Context(), id)
	if err != nil || rec == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("download %q not found", id))
		return
	}

	switch rec.State {
	case download.StateFailed:
		if err := s.downloadSvc.Retry(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "retrying"})
	case download.StateFailedPending:
		// Re-trigger search resolution immediately. Reset retry count so
		// manual retry is not blocked by the auto-retry limit.
		rec.RetryCount = 0
		rec.RetryAfter = time.Now().UTC().Format(time.RFC3339)
		if err := s.downloadSvc.UpdateDownload(r.Context(), rec); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// The retry worker will pick it up on next tick.
		writeJSON(w, http.StatusOK, map[string]string{"status": "queued for retry"})
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("download %q in state %q is not retryable", id, rec.State))
	}
}

// handleEvents serves SSE stream for real-time download events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	s.sseHub.ServeHTTP(w, r)
}

// ─── Library handlers ────────────────────────────────────────────────

func (s *Server) handleLibraryTracks(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	tracks, err := s.store.SearchTracks(ctx, q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if tracks == nil {
		tracks = []domain.Track{}
	}

	// Exclude tracks that live under the playlist path — playlist files
	// are managed by buildPlaylistFolder and shouldn't appear in the library view.
	cfg := s.cfg.Get()
	if cfg.Library.PlaylistPath != "" {
		absPlaylist, _ := filepath.Abs(cfg.Library.PlaylistPath)
		tracks = filterByPath(tracks, absPlaylist)
	}

	_ = offset
	writeJSON(w, http.StatusOK, tracks)
}

// filterByPath removes tracks whose FilePath starts with excludeRoot.
func filterByPath(tracks []domain.Track, excludeRoot string) []domain.Track {
	out := tracks[:0]
	for _, t := range tracks {
		if t.FilePath == "" || !strings.HasPrefix(t.FilePath, excludeRoot) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) handleLibraryArtists(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	var artists []domain.Artist
	var err error
	if q != "" {
		artists, err = s.store.SearchArtists(ctx, q, limit)
	} else {
		artists, err = s.store.ListArtists(ctx, offset, limit)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artists == nil {
		artists = []domain.Artist{}
	}

	// Transform local image paths to API URLs for the frontend.
	for i := range artists {
		if artists[i].ThumbURL == "artist.jpg" {
			artists[i].ThumbURL = fmt.Sprintf("/api/artist-image/%d", artists[i].ID)
		}
	}

	writeJSON(w, http.StatusOK, artists)
}

func (s *Server) handleLibraryAlbums(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	albums, err := s.store.SearchAlbums(ctx, q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.Album{}
	}
	_ = offset
	writeJSON(w, http.StatusOK, albums)
}

// sqlDBProvider is satisfied by types that expose a *sql.DB (e.g. *sqlite.Store).
type sqlDBProvider interface {
	DB() *sql.DB
}

// DiscoveryTrackEntry is a track from discovery merged with library download status.
type discoveryTrackEntry struct {
	Title          string `json:"title"`
	TrackNumber    int    `json:"track_number"`
	DurationMs     int64  `json:"duration_ms"`
	Downloaded     bool   `json:"downloaded"`
	LibraryTrackID int64  `json:"library_track_id,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	FileSize       int64  `json:"file_size,omitempty"`
	Bitrate        int    `json:"bitrate,omitempty"`
	Format         string `json:"format,omitempty"`
}

// albumDiscoveryResponse is the JSON payload for the discovery endpoint.
type albumDiscoveryResponse struct {
	Provider        string                `json:"provider"`
	ProviderAlbumID string                `json:"provider_album_id"`
	Tracks          []discoveryTrackEntry `json:"tracks"`
}

// handleLibraryAlbumDiscovery searches discovery providers for an album's full
// track list, caches the result, and returns tracks merged with library download status.
func (s *Server) handleLibraryAlbumDiscovery(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()

	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if album == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	// Try the cache first (requires concrete DB access).
	var cachedProvider, cachedAlbumID, cachedTracksJSON, cachedAt string
	if dbp, ok := s.store.(sqlDBProvider); ok {
		err := dbp.DB().QueryRowContext(ctx,
			`SELECT provider_name, provider_album_id, tracks_json, cached_at FROM album_discovery_cache WHERE album_id = ?`, albumID,
		).Scan(&cachedProvider, &cachedAlbumID, &cachedTracksJSON, &cachedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("album discovery cache read failed", "album_id", albumID, "error", err, "component", "api")
		}
	}

	var discoveryTracks []discovery.TrackInfo
	if cachedTracksJSON != "" {
		// Check cache TTL (24h).
		if cachedAt != "" {
			if t, parseErr := time.Parse(time.RFC3339, cachedAt); parseErr == nil {
				if time.Since(t) > 24*time.Hour {
					cachedTracksJSON = "" // expired
				}
			} else {
				cachedTracksJSON = "" // unparseable timestamp = treat as expired
			}
		}
	}
	if cachedTracksJSON != "" {
		// Cache hit — unmarshal cached tracks.
		if uerr := json.Unmarshal([]byte(cachedTracksJSON), &discoveryTracks); uerr != nil {
			s.log.Warn("album discovery cache corrupt, refetching", "album_id", albumID, "error", uerr, "component", "api")
			discoveryTracks = nil
			cachedProvider = ""
			cachedAlbumID = ""
		}
	}

	// Cache miss — search discovery providers.
	if discoveryTracks == nil && s.discoveryReg != nil {
		providers := s.discoveryReg.Any()
		query := artist.Name + " " + album.Title
		for _, p := range providers {
			albums, serr := p.SearchAlbums(ctx, query, 5)
			if serr != nil || len(albums) == 0 {
				continue
			}
			// Find the album that best matches our artist + title.
			var matched *discovery.AlbumResult
			for i := range albums {
				a := &albums[i]
				if strings.EqualFold(a.ArtistName, artist.Name) && strings.EqualFold(a.Title, album.Title) {
					matched = a
					break
				}
			}
			// Fallback: use first result if title matches but artist differs slightly.
			if matched == nil {
				for i := range albums {
					a := &albums[i]
					if strings.EqualFold(a.Title, album.Title) {
						matched = a
						break
					}
				}
			}
			if matched == nil {
				continue
			}

			tracks, terr := p.GetAlbumTracks(ctx, matched.ProviderID)
			if terr != nil || len(tracks) == 0 {
				continue
			}

			discoveryTracks = tracks
			cachedProvider = matched.ProviderName
			cachedAlbumID = matched.ProviderID

			// Persist to cache.
			if dbp, ok := s.store.(sqlDBProvider); ok {
				tracksJSON, jerr := json.Marshal(tracks)
				if jerr == nil {
					_, err := dbp.DB().ExecContext(ctx,
						`INSERT OR REPLACE INTO album_discovery_cache (album_id, provider_name, provider_album_id, tracks_json, cached_at)
						 VALUES (?, ?, ?, ?, ?)`,
						albumID, matched.ProviderName, matched.ProviderID, string(tracksJSON),
						time.Now().UTC().Format(time.RFC3339),
					)
					if err != nil {
						s.log.Warn("album discovery cache write failed", "album_id", albumID, "error", err, "component", "api")
					}
				} else {
					s.log.Warn("album discovery cache marshal failed", "album_id", albumID, "error", jerr, "component", "api")
				}
			}
			break
		}
	}

	if discoveryTracks == nil {
		// No discovery data — return just library tracks as "downloaded".
		libTracks, err := s.store.GetTracksByAlbum(ctx, albumID)
		if err != nil {
			s.log.Warn("get tracks by album failed", "album_id", albumID, "error", err, "component", "api")
		}
		entries := make([]discoveryTrackEntry, 0)
		for _, t := range libTracks {
			entries = append(entries, discoveryTrackEntry{
				Title:          t.Title,
				TrackNumber:    t.TrackNumber,
				DurationMs:     int64(t.Duration),
				Downloaded:     true,
				LibraryTrackID: t.ID,
				FilePath:       t.FilePath,
				FileSize:       t.FileSize,
				Bitrate:        t.Bitrate,
				Format:         formatFromPath(t.FilePath),
			})
		}
		writeJSON(w, http.StatusOK, albumDiscoveryResponse{Tracks: entries})
		return
	}

	// Build library track index for merge.
	libTracks, err := s.store.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		s.log.Warn("get tracks by album failed", "album_id", albumID, "error", err, "component", "api")
	}
	byTitle := make(map[string]*domain.Track, len(libTracks))
	byISRC := make(map[string]*domain.Track, len(libTracks))
	for i := range libTracks {
		t := &libTracks[i]
		byTitle[normalizeKey(t.Title)] = t
		if t.ISRC != "" {
			byISRC[t.ISRC] = t
		}
	}
	// Also index all artist tracks for cross-album matching (title + ISRC).
	artistTracks, err := s.store.GetTracksByArtist(ctx, album.ArtistID)
	if err != nil {
		s.log.Warn("get tracks by artist failed", "artist_id", album.ArtistID, "error", err, "component", "api")
	}
	for i := range artistTracks {
		t := &artistTracks[i]
		// Only add to title index if not already present (album tracks take precedence).
		titleKey := normalizeKey(t.Title)
		if _, exists := byTitle[titleKey]; !exists {
			byTitle[titleKey] = t
		}
		if t.ISRC != "" {
			if _, exists := byISRC[t.ISRC]; !exists {
				byISRC[t.ISRC] = t
			}
		}
	}

	// Merge discovery tracks with library status.
	var entries []discoveryTrackEntry
	for _, dt := range discoveryTracks {
		entry := discoveryTrackEntry{
			Title:       dt.Title,
			TrackNumber: dt.TrackNumber,
			DurationMs:  dt.DurationMs,
		}

		// Match by ISRC first (most reliable).
		if libTrack, ok := byISRC[dt.ISRC]; ok && dt.ISRC != "" {
			entry.Downloaded = true
			entry.LibraryTrackID = libTrack.ID
			entry.FilePath = libTrack.FilePath
			entry.FileSize = libTrack.FileSize
			entry.Bitrate = libTrack.Bitrate
			entry.Format = formatFromPath(libTrack.FilePath)
		} else if libTrack, ok := byTitle[normalizeKey(dt.Title)]; ok {
			// Fallback to normalized title matching.
			entry.Downloaded = true
			entry.LibraryTrackID = libTrack.ID
			entry.FilePath = libTrack.FilePath
			entry.FileSize = libTrack.FileSize
			entry.Bitrate = libTrack.Bitrate
			entry.Format = formatFromPath(libTrack.FilePath)
		}

		entries = append(entries, entry)
	}

	resp := albumDiscoveryResponse{
		Provider:        cachedProvider,
		ProviderAlbumID: cachedAlbumID,
		Tracks:          entries,
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLibraryAlbumDownloadMissing queues all undownloaded tracks for an album
// as pending downloads. Source resolution happens in the background via the
// monitor's resolvePendingSources — same pattern as playlist download-missing.
func (s *Server) handleLibraryAlbumDownloadMissing(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()

	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get album: %w", err))
		return
	}
	if album == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get artist: %w", err))
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	// Get discovery tracks to find which ones are missing.
	discovery, err := getLibraryAlbumDiscoveryData(ctx, s, albumID, artist.Name, album.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to load discovery data: %w", err))
		return
	}

	queued := 0
	var errors []string
	for _, dt := range discovery {
		if dt.Downloaded {
			continue
		}
		_, err := s.downloadSvc.QueuePending(ctx, download.Meta{
			Artist:      artist.Name,
			Album:       album.Title,
			Title:       dt.Title,
			TrackNumber: dt.TrackNumber,
		})
		if err != nil {
			s.log.Warn("queue pending failed", "artist", artist.Name, "title", dt.Title, "error", err, "component", "api")
			errors = append(errors, fmt.Sprintf("%s: %v", dt.Title, err))
			continue
		}
		queued++
	}

	resp := map[string]any{"queued": queued}
	if len(errors) > 0 {
		resp["errors"] = errors
	}
	writeJSON(w, http.StatusOK, resp)
}

// discoveryTrackData is the internal representation used by handleLibraryAlbumDownloadMissing.
type discoveryTrackData struct {
	Title       string
	TrackNumber int
	Downloaded  bool
}

// getLibraryAlbumDiscoveryData returns discovery tracks for an album, reusing
// the same logic as handleLibraryAlbumDiscovery but returning a simple struct.
func getLibraryAlbumDiscoveryData(ctx context.Context, s *Server, albumID int64, artistName, albumTitle string) ([]discoveryTrackData, error) {
	// Try cache first.
	if dbp, ok := s.store.(sqlDBProvider); ok {
		var tracksJSON, cachedAt string
		err := dbp.DB().QueryRowContext(ctx,
			`SELECT tracks_json, cached_at FROM album_discovery_cache WHERE album_id = ?`, albumID,
		).Scan(&tracksJSON, &cachedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("album discovery cache read failed", "album_id", albumID, "error", err, "component", "api")
		}
		if err == nil && tracksJSON != "" {
			// Check 24h TTL.
			if t, parseErr := time.Parse(time.RFC3339, cachedAt); parseErr == nil {
				if time.Since(t) > 24*time.Hour {
					tracksJSON = "" // expired
				}
			}
		}
		if tracksJSON != "" {
			var cached []struct {
				Title       string `json:"title"`
				TrackNumber int    `json:"track_number"`
			}
			if json.Unmarshal([]byte(tracksJSON), &cached) == nil {
				// Get library tracks for merge.
				libTracks, _ := s.store.GetTracksByAlbum(ctx, albumID)
				byTitle := make(map[string]bool, len(libTracks))
				for _, t := range libTracks {
					byTitle[normalizeKey(t.Title)] = true
				}
				var out []discoveryTrackData
				for _, c := range cached {
					out = append(out, discoveryTrackData{
						Title:       c.Title,
						TrackNumber: c.TrackNumber,
						Downloaded:  byTitle[normalizeKey(c.Title)],
					})
				}
				return out, nil
			}
		}
	}

	// Cache miss — do the full discovery.
	if s.discoveryReg == nil {
		return nil, fmt.Errorf("no discovery providers available")
	}
	providers := s.discoveryReg.Any()
	query := artistName + " " + albumTitle
	for _, p := range providers {
		albums, serr := p.SearchAlbums(ctx, query, 5)
		if serr != nil || len(albums) == 0 {
			continue
		}
		var matched *discovery.AlbumResult
		for i := range albums {
			a := &albums[i]
			if strings.EqualFold(a.ArtistName, artistName) && strings.EqualFold(a.Title, albumTitle) {
				matched = a
				break
			}
		}
		if matched == nil {
			// Fallback: title-only match when artist name differs slightly.
			for i := range albums {
				a := &albums[i]
				if strings.EqualFold(a.Title, albumTitle) {
					matched = a
					break
				}
			}
		}
		if matched == nil {
			continue
		}
		tracks, terr := p.GetAlbumTracks(ctx, matched.ProviderID)
		if terr != nil || len(tracks) == 0 {
			continue
		}
		libTracks, _ := s.store.GetTracksByAlbum(ctx, albumID)
		byTitle := make(map[string]bool, len(libTracks))
		for _, t := range libTracks {
			byTitle[normalizeKey(t.Title)] = true
		}
		var out []discoveryTrackData
		for _, t := range tracks {
			out = append(out, discoveryTrackData{
				Title:       t.Title,
				TrackNumber: t.TrackNumber,
				Downloaded:  byTitle[normalizeKey(t.Title)],
			})
		}

		// Persist to cache so future calls hit cache.
		if dbp, ok := s.store.(sqlDBProvider); ok {
			tracksJSON, jerr := json.Marshal(tracks)
			if jerr == nil {
				_, _ = dbp.DB().ExecContext(ctx,
					`INSERT OR REPLACE INTO album_discovery_cache (album_id, provider_name, provider_album_id, tracks_json, cached_at)
					 VALUES (?, ?, ?, ?, ?)`,
					albumID, matched.ProviderName, matched.ProviderID, string(tracksJSON),
					time.Now().UTC().Format(time.RFC3339),
				)
			}
		}

		return out, nil
	}
	return nil, fmt.Errorf("no discovery data found for album %d", albumID)
}

func (s *Server) handleLibraryScan(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()

	// Scan the library root only — download path is staging, not scanned.
	paths := []string{cfg.Library.LibraryPath}
	if paths[0] == "" {
		paths[0] = "./music"
	}

	ctx := r.Context()
	var agg library.ScanStats
	for _, p := range paths {
		stats, err := s.scanner.ScanPath(ctx, p)
		if err != nil {
			s.log.Error("scan error", "path", p, "error", err, "component", "scanner")
			agg.Errors++
			continue
		}
		agg.Imported += stats.Imported
		agg.Scanned += stats.Scanned
		agg.Skipped += stats.Skipped
		agg.Errors += stats.Errors
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scanned":  agg.Scanned,
		"imported": agg.Imported,
		"skipped":  agg.Skipped,
		"errors":   agg.Errors,
		"paths":    paths,
	})
}

func (s *Server) handleLibraryArtist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	artist, err := s.store.GetArtist(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}
	if artist.ThumbURL == "artist.jpg" {
		artist.ThumbURL = fmt.Sprintf("/api/artist-image/%d", artist.ID)
	}
	writeJSON(w, http.StatusOK, artist)
}

func (s *Server) handleLibraryArtistAlbums(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	albums, err := s.store.GetAlbumsByArtist(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.Album{}
	}
	writeJSON(w, http.StatusOK, albums)
}

func (s *Server) handleLibraryArtistTracks(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	tracks, err := s.store.GetTracksByArtist(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if tracks == nil {
		tracks = []domain.Track{}
	}
	writeJSON(w, http.StatusOK, tracks)
}

// handleCoverArt serves the cover.jpg image for an album.
func (s *Server) handleCoverArt(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()
	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	// Try to find the album directory from existing tracks.
	cfg := s.cfg.Get()
	var albumDir string
	tracks, _ := s.store.GetTracksByAlbum(ctx, albumID)
	if len(tracks) > 0 && tracks[0].FilePath != "" {
		albumDir = filepath.Dir(tracks[0].FilePath)
	} else {
		// Fall back to constructing the path from the folder template.
		resolver := library.NewPathResolver(cfg.Library.FolderTemplate)
		resolved := resolver.Resolve(library.ResolveArgs{
			Artist:    artist.Name,
			Album:     album.Title,
			Year:      album.Year,
			TrackNum:  1,
			Title:     "dummy",
			Ext:       "mp3",
			AlbumType: "Album",
		})
		if resolved == "" {
			writeError(w, http.StatusNotFound, fmt.Errorf("cannot resolve album path"))
			return
		}
		albumDir = filepath.Join(cfg.Library.LibraryPath, resolved)
	}

	coverPath := filepath.Join(albumDir, "cover.jpg")
	f, err := os.Open(coverPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Errorf("cover not found"))
		} else {
			s.log.Error("cover open failed", "path", coverPath, "error", err, "component", "api")
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.log.Error("cover stat failed", "path", coverPath, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	http.ServeContent(w, r, "cover.jpg", fi.ModTime(), f)
}

// handleArtistImage serves the artist.jpg image from the artist's library directory.
func (s *Server) handleArtistImage(w http.ResponseWriter, r *http.Request) {
	artistIDStr := r.PathValue("artistID")
	artistID, err := strconv.ParseInt(artistIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}

	ctx := r.Context()
	artist, err := s.store.GetArtist(ctx, artistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	cfg := s.cfg.Get()
	var artistDir string

	// Try to find the artist directory from existing tracks.
	tracks, _ := s.store.GetTracksByArtist(ctx, artistID)
	if len(tracks) > 0 && tracks[0].FilePath != "" {
		// Tracks are stored as {artist}/{album}/track.ext, so parent-of-parent = artist dir.
		artistDir = filepath.Dir(filepath.Dir(tracks[0].FilePath))
	} else {
		// Fallback: construct from library root + artist name.
		artistDir = filepath.Join(cfg.Library.LibraryPath, artist.Name)
	}

	// Defense-in-depth: ensure resolved path stays within library root.
	if cleanDir, cleanRoot := filepath.Clean(artistDir), filepath.Clean(cfg.Library.LibraryPath); !strings.HasPrefix(cleanDir, cleanRoot+string(os.PathSeparator)) && cleanDir != cleanRoot {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	imagePath := filepath.Join(artistDir, "artist.jpg")
	f, err := os.Open(imagePath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Errorf("artist image not found"))
		} else {
			s.log.Error("artist image open failed", "path", imagePath, "error", err, "component", "api")
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.log.Error("artist image stat failed", "path", imagePath, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	http.ServeContent(w, r, "artist.jpg", fi.ModTime(), f)
}

// ─── Playlist handlers ────────────────────────────────────────────────

func (s *Server) handlePlaylistSources(w http.ResponseWriter, r *http.Request) {
	if s.playlistSvc == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	srcs := s.playlistSvc.Sources()
	var out []map[string]string
	for _, src := range srcs {
		out = append(out, map[string]string{
			"name":    src.Name(),
			"display": src.DisplayName(),
		})
	}
	if out == nil {
		out = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePlaylistSourceBrowse(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	if s.playlistSvc == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	ctx := r.Context()
	items, err := s.playlistSvc.BrowseSource(ctx, source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if items == nil {
		items = []playlist.SourcePlaylistItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleListPlaylists(w http.ResponseWriter, r *http.Request) {
	if s.playlistSvc == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	ctx := r.Context()
	playlists, err := s.store.ListPlaylists(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if playlists == nil {
		playlists = []domain.Playlist{}
	}
	writeJSON(w, http.StatusOK, playlists)
}

func (s *Server) handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	ctx := r.Context()
	p, err := s.store.GetPlaylist(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("playlist not found"))
		return
	}
	tracks, _ := s.store.GetPlaylistTracks(ctx, id)
	if tracks == nil {
		tracks = []domain.PlaylistTrack{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"playlist": p,
		"tracks":   tracks,
	})
}

func (s *Server) handleImportPlaylist(w http.ResponseWriter, r *http.Request) {
	if s.playlistSvc == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("playlist service not available"))
		return
	}
	var req struct {
		Source     string `json:"source"`
		PlaylistID string `json:"playlist_id"`
		SyncMode   string `json:"sync_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Source == "" || req.PlaylistID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("source and playlist_id required"))
		return
	}
	if req.SyncMode != "" && req.SyncMode != "mirror" && req.SyncMode != "append" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("sync_mode must be 'mirror' or 'append'"))
		return
	}

	result, err := s.playlistSvc.ImportPlaylist(r.Context(), req.Source, req.PlaylistID, req.SyncMode)
	if err != nil {
		s.log.Error("playlist import failed", "source", req.Source, "playlist_id", req.PlaylistID, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"playlist":  result.Playlist,
		"tracks":    result.Tracks,
		"linked":    result.Linked,
		"unmatched": result.Unmatched,
	})
}

func (s *Server) handleDownloadMissing(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	if s.playlistSvc == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("playlist service not available"))
		return
	}
	queued, err := s.playlistSvc.DownloadMissing(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued})
}

func (s *Server) handleSyncPlaylist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	if s.playlistSvc == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("playlist service not available"))
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := s.playlistSvc.SyncPlaylist(ctx, id); err != nil {
			s.log.Error("playlist sync failed", "playlist_id", id, "error", err, "component", "api")
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
}

func (s *Server) handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	var req struct {
		AutoSync *bool   `json:"auto_sync"`
		SyncMode *string `json:"sync_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.SyncMode != nil && *req.SyncMode != "mirror" && *req.SyncMode != "append" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("sync_mode must be 'mirror' or 'append'"))
		return
	}

	ctx := r.Context()
	p, err := s.store.GetPlaylist(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("playlist not found"))
		return
	}
	if req.AutoSync != nil {
		p.AutoSync = *req.AutoSync
	}
	if req.SyncMode != nil {
		p.SyncMode = domain.SyncMode(*req.SyncMode)
	}
	// Skip upsert if nothing changed — avoids needless UpdatedAt bump.
	if req.AutoSync == nil && req.SyncMode == nil {
		writeJSON(w, http.StatusOK, p)
		return
	}
	if _, err := s.store.UpsertPlaylist(ctx, p); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Re-read to get fresh UpdatedAt set by the store.
	if updated, err := s.store.GetPlaylist(ctx, id); err == nil && updated != nil {
		p = updated
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	ctx := r.Context()
	if err := s.store.DeletePlaylist(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ─── Debug ────────────────────────────────────────────────────────────

// handleDebugDownload returns the full download record state including
// library track and playlist link info for troubleshooting.
func (s *Server) handleDebugDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	dl, err := s.downloadSvc.GetStatus(r.Context(), id)
	if err != nil || dl == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("download %q not found", id))
		return
	}

	info := map[string]any{
		"download":         dl,
		"state":            dl.State,
		"terminal":         dl.State.Terminal(),
		"playlist_id":      dl.PlaylistID,
		"library_track_id": dl.LibraryTrackID,
	}

	// Fetch linked library track if present.
	if dl.LibraryTrackID != 0 {
		if track, err := s.store.GetTrack(r.Context(), dl.LibraryTrackID); err == nil && track != nil {
			info["library_track"] = track
		}
	}

	// Fetch playlist link if present.
	if dl.PlaylistID != "" {
		if pid, err := strconv.ParseInt(dl.PlaylistID, 10, 64); err == nil {
			if pl, err := s.store.GetPlaylist(r.Context(), pid); err == nil && pl != nil {
				info["playlist"] = pl
			}
			if pts, err := s.store.GetPlaylistTracks(r.Context(), pid); err == nil {
				info["playlist_tracks"] = pts
			}
		}
	}

	writeJSON(w, http.StatusOK, info)
}

// ─── Quality Profile handlers ────────────────────────────────────────

// handleListQualityProfiles returns all quality profiles, default first.
func (s *Server) handleListQualityProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.qualityProfileStore.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if profiles == nil {
		profiles = []quality.QualityProfile{}
	}
	writeJSON(w, http.StatusOK, profiles)
}

// handleGetQualityProfile returns a single profile by ID.
func (s *Server) handleGetQualityProfile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid id: %s", idStr))
		return
	}
	profile, err := s.qualityProfileStore.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if profile == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("profile %d not found", id))
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

// handleCreateQualityProfile creates a new quality profile.
func (s *Server) handleCreateQualityProfile(w http.ResponseWriter, r *http.Request) {
	var p quality.QualityProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if p.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	id, err := s.qualityProfileStore.Create(r.Context(), &p)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "unique") {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	p.ID = id
	writeJSON(w, http.StatusCreated, p)
}

// handleUpdateQualityProfile updates an existing quality profile.
func (s *Server) handleUpdateQualityProfile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid id: %s", idStr))
		return
	}
	var p quality.QualityProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Load the existing profile to avoid zeroing out fields not sent in the request.
	existing, err := s.qualityProfileStore.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("profile %d not found", id))
		return
	}

	// Merge: apply only non-zero fields from the update payload.
	if p.Name != "" {
		existing.Name = p.Name
	}
	if p.Description != "" {
		existing.Description = p.Description
	}
	if len(p.RankedTargets) > 0 {
		existing.RankedTargets = p.RankedTargets
	}
	// Booleans can't be distinguished from zero value in JSON (false == omitted).
	// The frontend sends all boolean fields explicitly, so safe to overwrite.
	existing.FallbackEnabled = p.FallbackEnabled
	existing.RankCandidatesByQuality = p.RankCandidatesByQuality
	existing.ReplaceLowerQuality = p.ReplaceLowerQuality
	if p.SearchMode != "" {
		existing.SearchMode = p.SearchMode
	}
	if p.UpgradePolicy != "" {
		existing.UpgradePolicy = p.UpgradePolicy
	}
	existing.UpgradeCutoffIndex = p.UpgradeCutoffIndex

	if err := s.qualityProfileStore.Update(r.Context(), existing); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "unique") {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	updated, err := s.qualityProfileStore.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleDeleteQualityProfile deletes a profile and nullifies references.
func (s *Server) handleDeleteQualityProfile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid id: %s", idStr))
		return
	}
	if err := s.qualityProfileStore.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSetDefaultQualityProfile makes a profile the app-wide default.
func (s *Server) handleSetDefaultQualityProfile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid id: %s", idStr))
		return
	}
	if err := s.qualityProfileStore.SetDefault(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "default_set"})
}

// qualityPresets holds built-in quality profile configurations.
var qualityPresets = map[string]quality.QualityProfile{
	"audiophile": {
		Name:                    "Audiophile",
		Description:             "FLAC 24-bit preferred, no lossy fallback",
		SearchMode:              quality.SearchBestQuality,
		RankCandidatesByQuality: true,
		RankedTargets: quality.RankedTargets{
			{Label: "FLAC 24-bit/192kHz", Format: "flac", MinBitDepth: 24, MinSampleRate: 192000},
			{Label: "FLAC 24-bit/96kHz", Format: "flac", MinBitDepth: 24, MinSampleRate: 96000},
			{Label: "FLAC 24-bit/48kHz", Format: "flac", MinBitDepth: 24, MinSampleRate: 48000},
			{Label: "FLAC 16-bit", Format: "flac", MinBitDepth: 16},
		},
		FallbackEnabled: false,
		UpgradePolicy:   quality.UpgradeUntilTop,
	},
	"balanced": {
		Name:                    "Balanced",
		Description:             "FLAC preferred, MP3 320 fallback",
		SearchMode:              quality.SearchPriority,
		RankCandidatesByQuality: false,
		RankedTargets: quality.RankedTargets{
			{Label: "FLAC 24-bit/96kHz", Format: "flac", MinBitDepth: 24, MinSampleRate: 96000},
			{Label: "FLAC 16-bit", Format: "flac", MinBitDepth: 16},
			{Label: "MP3 320kbps", Format: "mp3", MinBitrate: 320},
		},
		FallbackEnabled:    true,
		UpgradePolicy:      quality.UpgradeUntilCutoff,
		UpgradeCutoffIndex: 1,
	},
	"space_saver": {
		Name:                    "Space Saver",
		Description:             "MP3 320 preferred, no FLAC",
		SearchMode:              quality.SearchPriority,
		RankCandidatesByQuality: false,
		RankedTargets: quality.RankedTargets{
			{Label: "MP3 320kbps", Format: "mp3", MinBitrate: 320},
			{Label: "MP3 192kbps", Format: "mp3", MinBitrate: 192},
		},
		FallbackEnabled: true,
		UpgradePolicy:   quality.UpgradeAcceptable,
	},
}

// handleQualityProfilePresets returns built-in quality profile presets.
func (s *Server) handleQualityProfilePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, qualityPresets)
}

// handleApplyQualityProfilePreset applies a named preset to the current default profile.
func (s *Server) handleApplyQualityProfilePreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Preset string `json:"preset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	preset, ok := qualityPresets[req.Preset]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown preset: %s", req.Preset))
		return
	}

	// Load the current default profile.
	defaultProfile, err := s.qualityProfileStore.LoadProfileByID(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Apply preset fields to the default profile.
	defaultProfile.Name = preset.Name
	defaultProfile.Description = preset.Description
	defaultProfile.RankedTargets = preset.RankedTargets
	defaultProfile.FallbackEnabled = preset.FallbackEnabled
	defaultProfile.SearchMode = preset.SearchMode
	defaultProfile.RankCandidatesByQuality = preset.RankCandidatesByQuality
	defaultProfile.ReplaceLowerQuality = preset.ReplaceLowerQuality
	defaultProfile.UpgradePolicy = preset.UpgradePolicy
	defaultProfile.UpgradeCutoffIndex = preset.UpgradeCutoffIndex

	if err := s.qualityProfileStore.Update(r.Context(), defaultProfile); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "preset_applied", "preset": req.Preset})
}

// ─── Helpers ─────────────────────────────────────────────────────────

// parsePagination extracts q, offset, and limit from query parameters.
// Defaults: q="", offset=0, limit=200.
func parsePagination(r *http.Request) (q string, offset, limit int) {
	q = r.URL.Query().Get("q")
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	return
}

// writeJSON is a helper for JSON responses.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError sends a JSON error response.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// ─── Discovery handlers ─────────────────────────────────────────────

func (s *Server) handleDiscoverProviders(w http.ResponseWriter, r *http.Request) {
	if s.discoveryReg == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	providers := s.discoveryReg.Any()
	s.log.Info("discover providers", "count", len(providers), "component", "discover")
	out := make([]map[string]any, len(providers))
	for i, p := range providers {
		out[i] = map[string]any{
			"name":         p.Name(),
			"display_name": p.DisplayName(),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDiscoverSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	searchType := r.URL.Query().Get("type") // "artist", "album", or empty for both
	s.log.Info("discover search", "query", q, "type", searchType, "component", "discover")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query parameter 'q' is required"})
		return
	}
	if s.discoveryReg == nil || len(s.discoveryReg.Any()) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "no discovery providers configured",
		})
		return
	}

	providers := s.discoveryReg.Configured()
	if len(providers) == 0 {
		providers = s.discoveryReg.Any() // fallback: use any configured provider
	}
	if len(providers) == 0 {
		s.log.Warn("discover search: no discovery providers found",
			"component", "discover")
		writeJSON(w, http.StatusOK, map[string]any{
			"artists": []discovery.ArtistSummary{},
			"albums":  []discovery.AlbumResult{},
		})
		return
	}

	// Query all configured providers in parallel with a total timeout.
	// merge and deduplicate — entries with images replace those without.
	const searchTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	type providerResult struct {
		artists []discovery.ArtistSummary
		albums  []discovery.AlbumResult
		err     error
	}

	results := make([]providerResult, len(providers))

	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(idx int, provider discovery.Provider) {
			defer wg.Done()
			if searchType == "" || searchType == "artist" {
				a, err := provider.SearchArtists(ctx, q, 10)
				if err != nil {
					s.log.Warn("discover search artists error",
						"provider", provider.Name(), "error", err, "component", "discover")
					results[idx].err = err
				}
				results[idx].artists = a
			}
			if searchType == "" || searchType == "album" {
				a, err := provider.SearchAlbums(ctx, q, 10)
				if err != nil {
					s.log.Warn("discover search albums error",
						"provider", provider.Name(), "error", err, "component", "discover")
					if results[idx].err == nil {
						results[idx].err = err
					}
				}
				results[idx].albums = a
			}
		}(i, p)
	}
	wg.Wait()

	// Merge artists: collect from all providers in order, deduplicate by
	// normalized name. Prefer results with images over those without.
	// Earlier providers win when image quality is equal.
	var allArtists []discovery.ArtistSummary
	for idx := range providers {
		if results[idx].err != nil {
			s.log.Debug("discover provider had errors, using partial results",
				"provider", providers[idx].Name(), "component", "discover")
		}
		for _, a := range results[idx].artists {
			allArtists = append(allArtists, a)
		}
	}
	artistMap := make(map[string]discovery.ArtistSummary)
	var mergedArtists []discovery.ArtistSummary
	for _, a := range allArtists {
		key := normalizeKey(a.Name)
		if existing, ok := artistMap[key]; ok {
			// Prefer entry with image over entry without.
			if existing.ImageURL == "" && a.ImageURL != "" {
				artistMap[key] = a
				// Replace in ordered list.
				for i := range mergedArtists {
					if normalizeKey(mergedArtists[i].Name) == key {
						mergedArtists[i] = a
						break
					}
				}
			}
		} else {
			artistMap[key] = a
			mergedArtists = append(mergedArtists, a)
		}
	}

	// Merge albums: collect from all providers in order, deduplicate.
	// Prefer results with cover art over those without.
	var allAlbums []discovery.AlbumResult
	for idx := range providers {
		for _, a := range results[idx].albums {
			allAlbums = append(allAlbums, a)
		}
	}
	albumMap := make(map[string]discovery.AlbumResult)
	var mergedAlbums []discovery.AlbumResult
	for _, a := range allAlbums {
		key := normalizeKey(a.ArtistName + "|" + a.Title)
		if existing, ok := albumMap[key]; ok {
			if existing.CoverURL == "" && a.CoverURL != "" {
				albumMap[key] = a
				for i := range mergedAlbums {
					if normalizeKey(mergedAlbums[i].ArtistName+"|"+mergedAlbums[i].Title) == key {
						mergedAlbums[i] = a
						break
					}
				}
			}
		} else {
			albumMap[key] = a
			mergedAlbums = append(mergedAlbums, a)
		}
	}

	s.log.Info("discover search results",
		"artists", len(mergedArtists), "albums", len(mergedAlbums),
		"providers", len(providers), "component", "discover")
	writeJSON(w, http.StatusOK, map[string]any{
		"artists": mergedArtists,
		"albums":  mergedAlbums,
	})
}

// handleDiscoverResolveArtist searches all providers for an artist by name
// and returns the best exact match. Used by the library UI to link to discovery.
func (s *Server) handleDiscoverResolveArtist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	s.log.Info("discover resolve artist", "query", q, "component", "discover")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query parameter 'q' is required"})
		return
	}
	if s.discoveryReg == nil || len(s.discoveryReg.Any()) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "no discovery providers configured",
		})
		return
	}

	providers := s.discoveryReg.Configured()
	if len(providers) == 0 {
		providers = s.discoveryReg.Any()
	}
	if len(providers) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artist not found"})
		return
	}

	const searchTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	type providerResult struct {
		artists []discovery.ArtistSummary
		err     error
	}
	results := make([]providerResult, len(providers))

	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(idx int, provider discovery.Provider) {
			defer wg.Done()
			a, err := provider.SearchArtists(ctx, q, 10)
			if err != nil {
				s.log.Warn("discover resolve artist error",
					"provider", provider.Name(), "error", err, "component", "discover")
				results[idx].err = err
			}
			results[idx].artists = a
		}(i, p)
	}
	wg.Wait()

	// Skip further work if the client disconnected during search.
	if r.Context().Err() != nil {
		return
	}

	// Collect all artists, prefer entries with images.
	queryKey := normalizeKey(q)
	var best *discovery.ArtistSummary
	for idx := range providers {
		for i := range results[idx].artists {
			a := &results[idx].artists[i]
			if normalizeKey(a.Name) != queryKey {
				continue
			}
			if best == nil || (best.ImageURL == "" && a.ImageURL != "") {
				best = a
			}
		}
	}

	if best == nil {
		s.log.Info("discover resolve artist: not found", "query", q, "component", "discover")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artist not found"})
		return
	}

	s.log.Info("discover resolve artist: found", "query", q,
		"provider", best.ProviderName, "provider_id", best.ProviderID, "component", "discover")
	writeJSON(w, http.StatusOK, best)
}

// artistOverviewResponse is returned by GET /api/discover/artists/overview.
type artistOverviewResponse struct {
	Artist      discovery.ArtistSummary `json:"artist"`
	TopTracks   []discovery.TrackInfo   `json:"top_tracks"`
	Discography map[string]int          `json:"discography"` // e.g. {"album":12,"single":5}
}

// handleDiscoverArtistOverview resolves an artist by name, fetches top tracks
// and discography stats, and caches the result for 24h (same pattern as
// album_discovery_cache).
func (s *Server) handleDiscoverArtistOverview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	s.log.Info("discover artist overview", "query", q, "component", "discover")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query parameter 'q' is required"})
		return
	}

	queryKey := normalizeKey(q)

	// ── Check cache ──
	var cachedName, cachedProvider, cachedArtistID, cachedImageURL, cachedGenresJSON, cachedTopTracksJSON, cachedDiscographyJSON, cachedAt string
	if dbp, ok := s.store.(sqlDBProvider); ok {
		err := dbp.DB().QueryRowContext(r.Context(),
			`SELECT artist_name, provider_name, provider_artist_id, image_url, genres_json, top_tracks_json, discography_json, cached_at
			 FROM artist_overview_cache WHERE normalized_name = ?`, queryKey,
		).Scan(&cachedName, &cachedProvider, &cachedArtistID, &cachedImageURL, &cachedGenresJSON, &cachedTopTracksJSON, &cachedDiscographyJSON, &cachedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("artist overview cache read failed", "query", q, "error", err, "component", "discover")
		}
	}

	if cachedDiscographyJSON != "" && cachedDiscographyJSON != "null" && cachedAt != "" {
		if t, parseErr := time.Parse(time.RFC3339, cachedAt); parseErr == nil {
			if time.Since(t) <= 24*time.Hour {
				// Cache hit — return cached data.
				var topTracks []discovery.TrackInfo
				if cachedTopTracksJSON != "" && cachedTopTracksJSON != "null" {
					if uerr := json.Unmarshal([]byte(cachedTopTracksJSON), &topTracks); uerr != nil {
						s.log.Warn("artist overview cache top_tracks corrupt", "query", q, "error", uerr, "component", "discover")
						topTracks = nil
					}
				}
				var discography map[string]int
				if uerr := json.Unmarshal([]byte(cachedDiscographyJSON), &discography); uerr != nil {
					s.log.Warn("artist overview cache discography corrupt", "query", q, "error", uerr, "component", "discover")
					discography = nil
				}
				if discography != nil {
					var cachedGenres []string
					if cachedGenresJSON != "" && cachedGenresJSON != "null" {
						_ = json.Unmarshal([]byte(cachedGenresJSON), &cachedGenres)
					}
					out := artistOverviewResponse{
						Artist: discovery.ArtistSummary{
							ProviderID:   cachedArtistID,
							ProviderName: cachedProvider,
							Name:         cachedName,
							ImageURL:     cachedImageURL,
							Genres:       cachedGenres,
						},
						TopTracks:   topTracks,
						Discography: discography,
					}
					if len(out.TopTracks) == 0 {
						out.TopTracks = []discovery.TrackInfo{}
					}
					writeJSON(w, http.StatusOK, out)
					return
				}
			}
		}
	}

	// ── Cache miss — resolve artist and fetch fresh data ──
	if s.discoveryReg == nil || len(s.discoveryReg.Any()) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no discovery providers configured"})
		return
	}

	// 1. Resolve artist (same logic as handleDiscoverResolveArtist).
	providers := s.discoveryReg.Configured()
	if len(providers) == 0 {
		providers = s.discoveryReg.Any()
	}

	const searchTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	type providerResult struct {
		artists []discovery.ArtistSummary
		err     error
	}
	results := make([]providerResult, len(providers))

	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(idx int, provider discovery.Provider) {
			defer wg.Done()
			a, err := provider.SearchArtists(ctx, q, 10)
			if err != nil {
				s.log.Warn("discover artist overview: search failed",
					"provider", provider.Name(), "error", err, "component", "discover")
			}
			results[idx].artists = a
		}(i, p)
	}
	wg.Wait()

	var resolved *discovery.ArtistSummary
	for idx := range providers {
		for i := range results[idx].artists {
			a := &results[idx].artists[i]
			if normalizeKey(a.Name) == queryKey {
				if resolved == nil || (resolved.ImageURL == "" && a.ImageURL != "") {
					resolved = a
				}
			}
		}
	}

	if resolved == nil {
		s.log.Info("discover artist overview: artist not found", "query", q, "component", "discover")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artist not found"})
		return
	}

	if r.Context().Err() != nil {
		return
	}

	// 2. Fetch discography (albums by type) with a fresh context.
	// Use the registry to get the provider matching the resolved artist.
	fetchProvider := s.discoveryReg.Get(resolved.ProviderName)
	if fetchProvider == nil {
		// Provider was removed since resolution — skip discography fetch
		// rather than risking a cross-provider call with wrong ID format.
		s.log.Warn("discover artist overview: resolved provider not found in registry",
			"provider", resolved.ProviderName, "component", "discover")
	}
	var albums []discovery.AlbumResult
	if fetchProvider != nil {
		fetchCtx, fetchCancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer fetchCancel()
		var err error
		albums, err = fetchProvider.GetArtistAlbums(fetchCtx, resolved.ProviderID, 50)
		if err != nil {
			s.log.Warn("discover artist overview: get albums failed", "query", q, "provider", resolved.ProviderName, "error", err, "component", "discover")
		}
	}
	discography := map[string]int{}
	for _, a := range albums {
		t := a.Type
		if t == "" {
			t = "album"
		}
		discography[t]++
	}

	// 3. Fetch top tracks (optional) — prefer the resolved provider, then scan others.
	var topTracks []discovery.TrackInfo
	tryTopTracks := func(p discovery.Provider, artistID string) bool {
		ttp, ok := p.(discovery.TopTrackProvider)
		if !ok {
			return false
		}
		fetchCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		tt, ttErr := ttp.GetArtistTopTracks(fetchCtx, artistID, 10)
		if ttErr != nil {
			s.log.Debug("discover artist overview: get top tracks failed", "query", q, "provider", p.Name(), "error", ttErr, "component", "discover")
			return false
		}
		topTracks = tt
		return true
	}

	// Try the already-resolved provider first (no extra search needed).
	if fetchProvider != nil && tryTopTracks(fetchProvider, resolved.ProviderID) {
		// got top tracks from the same provider
	} else {
		// Scan other providers (requires re-searching for their artist ID).
		for _, p := range providers {
			if p.Name() == resolved.ProviderName {
				continue // already tried
			}
			searchCtx, searchCancel := context.WithTimeout(r.Context(), 5*time.Second)
			artists, serr := p.SearchArtists(searchCtx, q, 5)
			searchCancel()
			if serr != nil || len(artists) == 0 {
				continue
			}
			var ttArtistID string
			for i := range artists {
				if normalizeKey(artists[i].Name) == queryKey {
					ttArtistID = artists[i].ProviderID
					break
				}
			}
			if ttArtistID == "" {
				continue
			}
			if tryTopTracks(p, ttArtistID) {
				break
			}
		}
	}

	// 4. Persist to cache (cache empty results too — avoids repeated lookups).
	if dbp, ok := s.store.(sqlDBProvider); ok {
		topJSON, _ := json.Marshal(topTracks)
		discJSON, _ := json.Marshal(discography)
		topStr := string(topJSON)
		if topStr == "null" || topStr == "" {
			topStr = "[]"
		}
		genresJSON, _ := json.Marshal(resolved.Genres)
		if string(genresJSON) == "null" {
			genresJSON = []byte("[]")
		}
		_, cerr := dbp.DB().ExecContext(r.Context(),
			`INSERT OR REPLACE INTO artist_overview_cache
			 (normalized_name, artist_name, provider_name, provider_artist_id, image_url, genres_json, top_tracks_json, discography_json, cached_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			queryKey, resolved.Name, resolved.ProviderName, resolved.ProviderID,
			resolved.ImageURL, string(genresJSON),
			topStr, string(discJSON),
			time.Now().UTC().Format(time.RFC3339),
		)
		if cerr != nil {
			s.log.Warn("artist overview cache write failed", "query", q, "error", cerr, "component", "discover")
		}
	}

	if topTracks == nil {
		topTracks = []discovery.TrackInfo{}
	}

	s.log.Info("discover artist overview: complete", "query", q,
		"provider", resolved.ProviderName, "albums", len(albums),
		"top_tracks", len(topTracks), "component", "discover")
	writeJSON(w, http.StatusOK, artistOverviewResponse{
		Artist: discovery.ArtistSummary{
			ProviderID:   resolved.ProviderID,
			ProviderName: resolved.ProviderName,
			Name:         resolved.Name,
			ImageURL:     resolved.ImageURL,
			Genres:       resolved.Genres,
		},
		TopTracks:   topTracks,
		Discography: discography,
	})
}

// normalizeKey strips accents, lowercases, and removes non-alphanumeric for dedup.
func normalizeKey(s string) string {
	s = strings.ToLower(s)
	// Simple accent removal for common Latin accents.
	replacer := strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ñ", "n", "ç", "c",
	)
	s = replacer.Replace(s)
	// Remove remaining non-alphanumeric.
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Server) handleDiscoverArtistAlbums(w http.ResponseWriter, r *http.Request) {
	artistID := r.PathValue("id")
	if artistID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artist id is required"})
		return
	}
	if s.discoveryReg == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	ctx := r.Context()
	providerName := r.URL.Query().Get("provider")

	// Try the specified provider first.
	if providerName != "" {
		if p := s.discoveryReg.Get(providerName); p != nil {
			if albums, err := p.GetArtistAlbums(ctx, artistID, 50); err == nil && albums != nil {
				writeJSON(w, http.StatusOK, albums)
				return
			}
		}
	}

	// Fallback: try all providers (legacy behavior).
	allProviders := s.discoveryReg.Any()
	if len(allProviders) == 0 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	for _, p := range allProviders {
		if providerName != "" && p.Name() == providerName {
			continue // already tried
		}
		albums, err := p.GetArtistAlbums(ctx, artistID, 50)
		if err == nil && albums != nil {
			writeJSON(w, http.StatusOK, albums)
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("artist %q not found on any provider", artistID))
}

func (s *Server) handleDiscoverAlbumTracks(w http.ResponseWriter, r *http.Request) {
	albumID := r.PathValue("id")
	if albumID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "album id is required"})
		return
	}
	if s.discoveryReg == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	ctx := r.Context()
	providerName := r.URL.Query().Get("provider")

	// Try the specified provider first.
	if providerName != "" {
		if p := s.discoveryReg.Get(providerName); p != nil {
			if tracks, err := p.GetAlbumTracks(ctx, albumID); err == nil && tracks != nil {
				writeJSON(w, http.StatusOK, tracks)
				return
			}
		}
	}

	// Fallback: try all providers (legacy behavior).
	allProviders := s.discoveryReg.Any()
	if len(allProviders) == 0 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	for _, p := range allProviders {
		if providerName != "" && p.Name() == providerName {
			continue
		}
		tracks, err := p.GetAlbumTracks(ctx, albumID)
		if err == nil && tracks != nil {
			writeJSON(w, http.StatusOK, tracks)
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("album %q not found on any provider", albumID))
}

func (s *Server) handleDiscoverAlbumDownload(w http.ResponseWriter, r *http.Request) {
	albumID := r.PathValue("id")
	if albumID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "album id is required"})
		return
	}

	// Parse optional artist/album names from request body for album search mode.
	var req struct {
		ArtistName string `json:"artist_name"`
		AlbumName  string `json:"album_name"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			s.log.Warn("failed to decode album download body",
				"error", err, "component", "api")
		}
	}

	ctx := r.Context()

	// ── Fetch tracks from discovery provider (always needed for fallback) ──

	if s.discoveryReg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "track", "queued": 0, "errors": []string{"no discovery providers configured"}})
		return
	}

	providers := s.discoveryReg.Configured()
	if len(providers) == 0 {
		providers = s.discoveryReg.Any()
	}
	if len(providers) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "track", "queued": 0, "errors": []string{"no discovery providers configured"}})
		return
	}

	var tracks []discovery.TrackInfo
	for _, p := range providers {
		t, err := p.GetAlbumTracks(ctx, albumID)
		if err == nil && t != nil {
			tracks = t
			break
		}
	}
	if tracks == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album %q not found on any provider", albumID))
		return
	}

	// Resolve artist/album names: prefer request body, fall back to track data.
	// This makes the handler work regardless of whether the UI provided names
	// (e.g., library download-missing callers may not have URL params).
	artistName := req.ArtistName
	albumName := req.AlbumName
	if artistName == "" && len(tracks) > 0 {
		artistName = tracks[0].ArtistName
	}
	if albumName == "" && len(tracks) > 0 {
		albumName = tracks[0].AlbumTitle
	}

	// ── Try album-level download if album sources are configured ──

	if artistName != "" && albumName != "" && s.orchestrator != nil {
		cfg := s.cfg.Get()
		if len(cfg.AlbumSources) > 0 && cfg.DownloadClient != "" {
			query := artistName + " " + albumName
			releases, searchErr := s.orchestrator.SearchAlbums(ctx, query)
			if searchErr == nil && len(releases) > 0 {
				best := releases[0]
				downloadID, queueErr := s.downloadSvc.QueueAlbum(ctx, best, nil, cfg.DownloadClient)
				if queueErr != nil {
					writeError(w, http.StatusInternalServerError, fmt.Errorf("queue album: %w", queueErr))
					return
				}
				s.log.Info("album download queued via album source",
					"download_id", downloadID,
					"artist", best.Artist,
					"album", best.Album,
					"component", "api",
				)
				writeJSON(w, http.StatusOK, map[string]any{
					"mode":        "album",
					"download_id": downloadID,
					"artist":      best.Artist,
					"album":       best.Album,
				})
				return
			}
			if searchErr != nil {
				s.log.Warn("album search failed, falling back to per-track",
					"artist", artistName, "album", albumName,
					"error", searchErr, "component", "api")
			} else {
				s.log.Info("no album releases found, falling back to per-track",
					"artist", artistName, "album", albumName, "component", "api")
			}
		}
	}

	// ── Per-track fallback ──

	var queued int
	var errors []string
	for _, t := range tracks {
		if t.ArtistName == "" || t.Title == "" {
			continue
		}
		_, dlErr := s.downloadSvc.QueuePending(ctx, download.Meta{
			Artist:      t.ArtistName,
			Album:       t.AlbumTitle,
			Title:       t.Title,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.DiscNumber,
		})
		if dlErr != nil {
			errors = append(errors, fmt.Sprintf("%s - %s: queue: %v", t.ArtistName, t.Title, dlErr))
			continue
		}
		queued++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":   "track",
		"queued": queued,
		"total":  len(tracks),
		"errors": errors,
	})
}

// noCache wraps a handler to prevent browser caching (useful for development).
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

// formatFromPath extracts the audio format from a file extension.
func formatFromPath(path string) string {
	ext := strings.ToUpper(filepath.Ext(path))
	switch ext {
	case ".FLAC":
		return "FLAC"
	case ".MP3":
		return "MP3"
	case ".M4A":
		return "M4A"
	case ".ALAC":
		return "ALAC"
	case ".AAC":
		return "AAC"
	case ".OGG":
		return "OGG"
	case ".WAV":
		return "WAV"
	case ".AIF", ".AIFF":
		return "AIFF"
	case ".WMA":
		return "WMA"
	case ".OPUS":
		return "OPUS"
	default:
		if ext != "" {
			return strings.TrimPrefix(ext, ".")
		}
		return ""
	}
}

// mergeAvailableProviders builds a provider order by keeping existing entries
// that are still available and appending newly-available providers at the end.
func mergeAvailableProviders(order []string, available []metadata.Provider) []string {
	availNames := make(map[string]bool, len(available))
	for _, p := range available {
		availNames[p.Name()] = true
	}
	var merged []string
	seen := make(map[string]bool, len(order)+len(available))
	for _, name := range order {
		if availNames[name] && !seen[name] {
			merged = append(merged, name)
			seen[name] = true
		}
	}
	for _, p := range available {
		if !seen[p.Name()] {
			merged = append(merged, p.Name())
			seen[p.Name()] = true
		}
	}
	return merged
}

// stringSlicesEqual reports whether two string slices have the same elements
// in the same order.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// connectedNames extracts names from connected download plugins and appends
// them after existing order entries for connected providers. Stale entries
// (disconnected providers) are dropped.
func connectedNames(plugins []download.Plugin, order []string) []string {
	// Only include plugins that can actually download (MonitoredProvider),
	// not metadata/search-only plugins.
	valid := make(map[string]bool, len(plugins))
	for _, p := range plugins {
		if _, ok := p.(download.MonitoredProvider); ok {
			valid[p.Name()] = true
		}
	}
	var merged []string
	seen := make(map[string]bool)
	for _, name := range order {
		if valid[name] && !seen[name] {
			merged = append(merged, name)
			seen[name] = true
		}
	}
	for _, p := range plugins {
		if _, ok := p.(download.MonitoredProvider); ok && !seen[p.Name()] {
			merged = append(merged, p.Name())
			seen[p.Name()] = true
		}
	}
	return merged
}
