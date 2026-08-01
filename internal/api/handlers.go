package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ramonskie/groovearr"
	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
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
	bgCtx               context.Context
	bgCancel            context.CancelFunc
}

// PluginRouteRegistrar is called after all standard routes are registered,
// giving plugins a chance to add their own HTTP endpoints.
type PluginRouteRegistrar func(mux *http.ServeMux)

func NewServer(addr string, bgCtx context.Context, logger *slog.Logger, cfg *config.Persistence, registry *download.Registry, mdRegistry *metadata.Registry, discoveryReg *discovery.Registry, downloadSvc *download.Service, store library.Store, scanner *library.Scanner, playlistSvc *playlist.Service, qualityProfileStore quality.ProfileStore, eventBus events.IEventAggregator, sseHub *sse.SSEHub, metadataResolver *metadata.MetadataResolver, enrichmentHandler *download.MetadataEnrichmentHandler, orchestrator *download.Orchestrator, pluginRoutes ...PluginRouteRegistrar) *Server {
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
	s.bgCtx, s.bgCancel = context.WithCancel(bgCtx)

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
	s.bgCancel()
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

	s.reconcileAfterConfigUpdate(oldSources)
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// reconcileAfterConfigUpdate rebuilds changed plugins, syncs provider orders,
// refreshes playlist sources, and ensures required directories exist.
func (s *Server) reconcileAfterConfigUpdate(oldSources map[string]json.RawMessage) {
	updated := s.cfg.Get()
	resources := plugin.PluginResources{DownloadPath: updated.Library.DownloadPath, Logger: s.log}

	// Rebuild only plugins whose config changed.
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
			ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Second)
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
		if s.enrichmentHandler != nil {
			s.enrichmentHandler.SetProviderOrder(syncedMdOrder)
		}
		if s.metadataResolver != nil {
			s.metadataResolver.SetProviderOrder(syncedMdOrder)
		}
		if !stringSlicesEqual(syncedMdOrder, updated.MetadataOrder) {
			if err := s.cfg.Update(func(cfg *config.Config) error {
				cfg.MetadataOrder = syncedMdOrder
				return nil
			}); err != nil {
				s.log.Warn("failed to persist synced metadata order", "error", err, "component", "api")
			}
		}
	}

	// Re-apply download source order, synced with connected providers.
	if s.orchestrator != nil {
		syncedDlOrder := connectedNames(s.registry.Configured(), updated.DownloadOrder)
		s.orchestrator.SetDownloadOrder(syncedDlOrder)
		if !stringSlicesEqual(syncedDlOrder, updated.DownloadOrder) {
			if err := s.cfg.Update(func(cfg *config.Config) error {
				cfg.DownloadOrder = syncedDlOrder
				return nil
			}); err != nil {
				s.log.Warn("failed to persist synced download order", "error", err, "component", "api")
			}
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
}

// validationError carries config validation failures.
type validationError struct {
	Errors []string
}

func (e *validationError) Error() string { return "validation failed" }

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
