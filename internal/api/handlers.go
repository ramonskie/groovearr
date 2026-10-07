package api

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/logger"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/quality"
	"github.com/ramonskie/groovearr/internal/sse"
	"github.com/ramonskie/groovearr/internal/user"
)

// Server holds all dependencies for HTTP handlers.
type Server struct {
	cfg                 *config.Persistence
	registry            *download.Registry
	mdRegistry          *metadata.Registry
	metadataResolver    *metadata.MetadataResolver
	enrichmentHandler   *download.MetadataEnrichmentHandler
	providerCooldown    *metadata.ProviderCooldown
	orchestrator        *download.Orchestrator
	discoveryReg        *discovery.Registry
	store               library.Store
	userStore           user.Store
	scanner             *library.Scanner
	downloadSvc         *download.Service
	eventBus            events.IEventAggregator
	sseHub              *sse.SSEHub
	matcher             *matching.Engine
	playlistSvc         *playlist.Service
	trackingSvc         trackingService
	qualityProfileStore quality.ProfileStore
	jobs                *jobs.Manager
	runners             *jobs.Runners
	jobStatePath        string
	jobStateMu          sync.Mutex
	httpSrv             *http.Server
	log                 *slog.Logger
	logPath             string
	logRotator          *logger.Rotator
	accessLog           *logger.Rotator
	rateLimiter         *ipRateLimiter
	sessions            *sessionStore
	healthChecker       *plugin.HealthChecker
	// albumZipSem caps simultaneous album-zip streams so a few large requests
	// cannot exhaust the process (see handlers_library_download.go). Buffered
	// channel used as a counting semaphore; lazily initialized so a bare
	// Server{} in tests is safe.
	albumZipSem  chan struct{}
	albumZipOnce sync.Once
	bgCtx        context.Context
	bgCancel     context.CancelFunc
}

// PluginRouteRegistrar is called after all standard routes are registered,
// giving plugins a chance to add their own HTTP endpoints. It receives an
// authorization-aware registrar rather than the raw mux, so every plugin route
// must declare its access level (see plugin.RouteRegistrar).
type PluginRouteRegistrar func(r plugin.RouteRegistrar)

// pluginRouteAdapter implements plugin.RouteRegistrar on top of the app mux.
// Admin routes are wrapped with s.adminOnly; User routes stay behind the global
// withAuth already applied around the whole mux. It is the only way plugin
// registrars can add routes, so no plugin can bypass authorization.
type pluginRouteAdapter struct {
	mux       *http.ServeMux
	adminOnly func(http.HandlerFunc) http.HandlerFunc
}

// Admin registers an admin-only route, failing closed to 403 for non-admins.
func (a pluginRouteAdapter) Admin(method, path string, h http.HandlerFunc) {
	a.mux.Handle(method+" "+path, a.adminOnly(h))
}

// User registers a route reachable by any authenticated caller.
func (a pluginRouteAdapter) User(method, path string, h http.HandlerFunc) {
	a.mux.Handle(method+" "+path, h)
}

func NewServer(addr string, bgCtx context.Context, logger *slog.Logger, cfg *config.Persistence, registry *download.Registry, mdRegistry *metadata.Registry, discoveryReg *discovery.Registry, downloadSvc *download.Service, store library.Store, userStore user.Store, scanner *library.Scanner, playlistSvc *playlist.Service, trackingSvc trackingService, qualityProfileStore quality.ProfileStore, eventBus events.IEventAggregator, sseHub *sse.SSEHub, metadataResolver *metadata.MetadataResolver, enrichmentHandler *download.MetadataEnrichmentHandler, orchestrator *download.Orchestrator, healthChecker *plugin.HealthChecker, logRotator *logger.Rotator, accessLog *logger.Rotator, logPath string, jobStatePath string, pluginRoutes ...PluginRouteRegistrar) *Server {
	s := &Server{
		cfg:                 cfg,
		registry:            registry,
		mdRegistry:          mdRegistry,
		metadataResolver:    metadataResolver,
		enrichmentHandler:   enrichmentHandler,
		providerCooldown:    metadata.NewProviderCooldown(),
		orchestrator:        orchestrator,
		discoveryReg:        discoveryReg,
		store:               store,
		userStore:           userStore,
		scanner:             scanner,
		downloadSvc:         downloadSvc,
		eventBus:            eventBus,
		sseHub:              sseHub,
		matcher:             matching.New(),
		playlistSvc:         playlistSvc,
		trackingSvc:         trackingSvc,
		qualityProfileStore: qualityProfileStore,
		healthChecker:       healthChecker,
		log:                 logger,
		logRotator:          logRotator,
		accessLog:           accessLog,
		logPath:             logPath,
		jobStatePath:        jobStatePath,
		rateLimiter:         newIPRateLimiter(defaultRateBuckets(), logger),
		sessions:            newSessionStore(),
		albumZipSem:         make(chan struct{}, albumZipMaxConcurrent),
	}
	s.bgCtx, s.bgCancel = context.WithCancel(bgCtx)
	s.jobs = jobs.NewManager(sseHub, s.bgCtx, logger)
	s.runners = jobs.NewRunners(jobs.RunnerDeps{
		Log:        logger,
		Store:      store,
		Config:     func() config.Config { return cfg.Get() },
		Scanner:    scanner,
		Enrichment: enrichmentHandler,
		Metadata:   mdRegistry,
		Playlist:   playlistSvc,
		Tracking:   trackingSvc,
		RateLimit:  s.providerCooldown,
	})
	s.restoreInterruptedJob()

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
	s.registerAPIRoutes(mux)

	// Let plugins register their own routes through the authorization-aware
	// adapter — plugins never see the raw mux.
	registrar := pluginRouteAdapter{mux: mux, adminOnly: s.adminOnly}
	for _, register := range pluginRoutes {
		register(registrar)
	}

	// Pass plain nil (not a typed-nil *Rotator) when the access log is
	// disabled: an interface wrapping a typed-nil pointer is non-nil, so the
	// middleware's `accessLog == nil` guard wouldn't catch it and Write would
	// panic on the nil receiver.
	var accessLogWriter io.Writer
	if s.accessLog != nil {
		accessLogWriter = s.accessLog
	}
	s.httpSrv = &http.Server{
		Addr:         addr,
		Handler:      withAccessLog(accessLogWriter)(withRequestID(withCORS(s.withAuth(mux)))),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	return s
}

// registerAPIRoutes registers the /api route table on mux. The settings
// surface (config, rate limits, jobs, tracking, quality profiles, logs, debug,
// and user management) is gated with s.adminOnly; every other route stays open
// to all authenticated callers. Authentication itself is performed once by
// withAuth around the whole mux — adminOnly is authorization only and fails
// closed when no identity is present.
func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)

	// ── Settings surface: rate limits (admin-only) ─────────────────────
	mux.HandleFunc("GET /api/rate-limits", s.adminOnly(s.handleRateLimits))
	mux.HandleFunc("DELETE /api/rate-limits/{provider}", s.adminOnly(s.handleClearRateLimit))

	// Setup status is the first-run probe and stays open. There are no setup
	// mutations registered today; any future /api/setup/* mutation is part of
	// the settings surface and must be wrapped with s.adminOnly.
	mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	mux.Handle("POST /api/login", withRateLimit("login", s.rateLimiter, http.HandlerFunc(s.handleLogin)))
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	// Identity for the SPA auth check — any authenticated user, admin or not.
	// Registered behind withAuth (the whole mux is wrapped); never exposes the
	// API key and is not gated adminOnly.
	mux.HandleFunc("GET /api/me", s.handleMe)

	// ── Settings surface: config (admin-only) ──────────────────────────
	mux.HandleFunc("GET /api/config", s.adminOnly(s.handleGetConfig))
	mux.HandleFunc("PUT /api/config", s.adminOnly(s.handleUpdateConfig))
	mux.HandleFunc("GET /api/config/sources", s.adminOnly(s.handleGetSources))
	mux.HandleFunc("POST /api/config/test/{source}", s.adminOnly(s.handleTestConnection))

	// ── Settings surface: user management (admin-only) ─────────────────
	// Admin-only account CRUD. Passwords are always returned as an explicit
	// userResponse DTO (never the stored hash); last-admin and self-delete
	// guards are enforced in the store's transaction and here respectively.
	mux.HandleFunc("GET /api/users", s.adminOnly(s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.adminOnly(s.handleCreateUser))
	mux.HandleFunc("PATCH /api/users/{id}", s.adminOnly(s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.adminOnly(s.handleDeleteUser))

	// ── Shared surface: search, albums, shared download queue ──────────
	mux.Handle("POST /api/search", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleSearch)))
	mux.Handle("POST /api/albums/search", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleAlbumSearch)))
	mux.Handle("POST /api/albums/download-best", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleAlbumDownloadBest)))
	mux.Handle("POST /api/download", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleDownload)))
	mux.Handle("POST /api/download/match", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleDownloadBest)))
	mux.HandleFunc("GET /api/downloads", s.handleGetDownloads)
	mux.HandleFunc("DELETE /api/downloads/{id}", s.handleCancelDownload)
	mux.Handle("POST /api/downloads/{id}/retry", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleRetryDownload)))

	// ── Shared surface: library, covers, artist images ─────────────────
	mux.HandleFunc("GET /api/library/tracks", s.handleLibraryTracks)
	mux.Handle("GET /api/library/tracks/{trackID}/download", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleLibraryTrackDownload)))
	mux.Handle("GET /api/library/albums/{albumID}/download", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleLibraryAlbumDownload)))
	mux.HandleFunc("GET /api/library/artists", s.handleLibraryArtists)
	mux.HandleFunc("GET /api/library/artists/duplicates", s.handleLibraryArtistDuplicates)
	mux.Handle("POST /api/library/artists/{artistID}/merge", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleLibraryArtistMerge)))
	mux.HandleFunc("GET /api/library/albums", s.handleLibraryAlbums)
	mux.HandleFunc("GET /api/library/artists/{artistID}", s.handleLibraryArtist)
	mux.HandleFunc("GET /api/library/artists/{artistID}/albums", s.handleLibraryArtistAlbums)
	mux.HandleFunc("GET /api/library/artists/{artistID}/tracks", s.handleLibraryArtistTracks)
	mux.HandleFunc("GET /api/covers/{albumID}", s.handleCoverArt)
	mux.HandleFunc("GET /api/artist-image/{artistID}", s.handleArtistImage)
	mux.Handle("GET /api/library/albums/{albumID}/discovery", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleLibraryAlbumDiscovery)))
	mux.Handle("POST /api/library/albums/{albumID}/download-missing", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleLibraryAlbumDownloadMissing)))

	// ── Settings surface: background jobs (admin-only) ─────────────────
	// Scan/enrich are rate-limited per client IP: both walk the whole library
	// and enrich additionally hits external metadata providers.
	mux.HandleFunc("GET /api/jobs", s.adminOnly(s.handleGetJob))
	mux.HandleFunc("GET /api/jobs/activity", s.adminOnly(s.handleJobActivity))
	mux.Handle("POST /api/jobs/scan", withRateLimit("scan", s.rateLimiter, s.adminOnly(s.handleJobScan)))
	mux.Handle("POST /api/jobs/enrich", withRateLimit("enrich", s.rateLimiter, s.adminOnly(s.handleJobEnrich)))
	mux.Handle("POST /api/jobs/duplicates", withRateLimit("duplicates", s.rateLimiter, s.adminOnly(s.handleJobDuplicates)))
	mux.Handle("POST /api/jobs/organize", withRateLimit("scan", s.rateLimiter, s.adminOnly(s.handleJobOrganize)))
	mux.HandleFunc("GET /api/jobs/organize/report", s.adminOnly(s.handleOrganizeReport))
	mux.HandleFunc("POST /api/jobs/cancel", s.adminOnly(s.handleJobCancel))

	// ── Shared surface: playlists ──────────────────────────────────────
	mux.HandleFunc("GET /api/playlists/sources", s.handlePlaylistSources)
	mux.HandleFunc("GET /api/playlists/sources/{source}", s.handlePlaylistSourceBrowse)
	mux.HandleFunc("GET /api/playlists", s.handleListPlaylists)
	mux.HandleFunc("GET /api/playlists/{id}", s.handleGetPlaylist)
	mux.HandleFunc("PATCH /api/playlists/{id}", s.handleUpdatePlaylist)
	mux.Handle("POST /api/playlists/import", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleImportPlaylist)))
	mux.Handle("POST /api/playlists/{id}/download-missing", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleDownloadMissing)))
	mux.Handle("POST /api/playlists/{id}/sync", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleSyncPlaylist)))
	mux.HandleFunc("DELETE /api/playlists/{id}", s.handleDeletePlaylist)

	// ── Settings surface: artist tracking (admin-only) ─────────────────
	// Tracked artists, their discographies, and the global wanted view.
	// Refresh/search-missing go through the job Manager.
	mux.HandleFunc("GET /api/tracking/artists", s.adminOnly(s.handleListTrackedArtists))
	mux.HandleFunc("POST /api/tracking/artists", s.adminOnly(s.handleAddTrackedArtist))
	mux.HandleFunc("GET /api/tracking/artists/{artistID}", s.adminOnly(s.handleGetTrackedArtist))
	mux.HandleFunc("PATCH /api/tracking/artists/{artistID}", s.adminOnly(s.handleUpdateTrackedArtist))
	mux.HandleFunc("DELETE /api/tracking/artists/{artistID}", s.adminOnly(s.handleDeleteTrackedArtist))
	mux.HandleFunc("GET /api/tracking/artists/{artistID}/albums", s.adminOnly(s.handleListTrackedAlbums))
	mux.HandleFunc("POST /api/tracking/artists/{artistID}/refresh", s.adminOnly(s.handleRefreshTrackedArtist))
	mux.HandleFunc("POST /api/tracking/artists/{artistID}/search-missing", s.adminOnly(s.handleSearchMissingArtist))
	mux.HandleFunc("POST /api/tracking/refresh", s.adminOnly(s.handleRefreshAllTracked))
	mux.HandleFunc("GET /api/tracking/wanted", s.adminOnly(s.handleListAllWanted))
	mux.HandleFunc("PATCH /api/tracking/albums/{albumID}", s.adminOnly(s.handleUpdateTrackedAlbum))

	// ── Shared surface: discovery ──────────────────────────────────────
	mux.HandleFunc("GET /api/discover/providers", s.handleDiscoverProviders)
	mux.Handle("GET /api/discover/search", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleDiscoverSearch)))
	mux.Handle("GET /api/discover/artists/resolve", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleDiscoverResolveArtist)))
	mux.Handle("GET /api/discover/artists/overview", withRateLimit("search", s.rateLimiter, http.HandlerFunc(s.handleDiscoverArtistOverview)))
	mux.HandleFunc("GET /api/discover/artists/{id}/albums", s.handleDiscoverArtistAlbums)
	mux.HandleFunc("GET /api/discover/albums/{id}/tracks", s.handleDiscoverAlbumTracks)
	mux.Handle("POST /api/discover/albums/{id}/download", withRateLimit("download", s.rateLimiter, http.HandlerFunc(s.handleDiscoverAlbumDownload)))

	// ── Settings surface: quality profiles (admin-only) ────────────────
	// Presets MUST come before /{id} to avoid matching "presets" as id.
	mux.HandleFunc("GET /api/quality-profiles/presets", s.adminOnly(s.handleQualityProfilePresets))
	mux.HandleFunc("POST /api/quality-profiles/apply-preset", s.adminOnly(s.handleApplyQualityProfilePreset))
	mux.HandleFunc("GET /api/quality-profiles", s.adminOnly(s.handleListQualityProfiles))
	mux.HandleFunc("POST /api/quality-profiles", s.adminOnly(s.handleCreateQualityProfile))
	mux.HandleFunc("GET /api/quality-profiles/{id}", s.adminOnly(s.handleGetQualityProfile))
	mux.HandleFunc("PUT /api/quality-profiles/{id}", s.adminOnly(s.handleUpdateQualityProfile))
	mux.HandleFunc("DELETE /api/quality-profiles/{id}", s.adminOnly(s.handleDeleteQualityProfile))
	mux.HandleFunc("PUT /api/quality-profiles/{id}/default", s.adminOnly(s.handleSetDefaultQualityProfile))

	// ── Shared surface: SSE for real-time download progress ────────────
	// (log_line / job_* topics within the stream are filtered per role in the
	// SSE hub, not at the router.)
	mux.HandleFunc("GET /api/events", s.handleEvents)

	// ── Settings surface: logs and debug (admin-only) ──────────────────
	// Logs — snapshot of the on-disk log file backing the settings Log tab.
	mux.HandleFunc("GET /api/logs", s.adminOnly(s.handleGetLogs))
	// Debug endpoint — full download state for troubleshooting.
	mux.HandleFunc("GET /api/debug/download/{id}", s.adminOnly(s.handleDebugDownload))
}

// SetProviderCooldown replaces the default (self-contained) cooldown with a
// shared app-wide instance, so a rate limit observed by album discovery or
// discover search also cools the provider for enrichment — and for jobs,
// which share the same bucket. Pass nil to keep the default.
func (s *Server) SetProviderCooldown(c *metadata.ProviderCooldown) {
	if c == nil {
		return
	}
	s.providerCooldown = c
	if s.runners != nil {
		s.runners.SetRateLimiter(c)
	}
}

// ListenAndServe starts the HTTP server (blocking).
func (s *Server) ListenAndServe() error {
	s.log.Info("listening", "addr", s.httpSrv.Addr, "component", "api")
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the server. The background job context is
// cancelled first; Shutdown blocks until any running job stops so the store
// is not closed underneath an in-flight scan/enrich. SSE client streams are
// then closed so the HTTP shutdown is not blocked by long-lived connections.
func (s *Server) Shutdown(ctx context.Context) error {
	s.bgCancel()
	s.jobs.Shutdown()
	s.sseHub.Shutdown()
	s.rateLimiter.Shutdown()
	s.sessions.Shutdown()
	return s.httpSrv.Shutdown(ctx)
}

// ─── Middleware ──────────────────────────────────────────────────────

// accessLogSkip lists endpoints polled on an interval (or probed) by the UI /
// container runtime. Each request carries no signal — it is a heartbeat, not an
// action — so they are excluded from the access log entirely, at every level.
// accessLogSkip lists endpoints polled on an interval (or probed) by the UI /
// container runtime. Each request carries no signal — it is a heartbeat, not an
// action — so they are excluded from the access log entirely, at every level.
// NOTE: these exact paths are GET-only today; path-only matching would also
// skip a future route reusing one of them with a different method.
var accessLogSkip = map[string]bool{
	"/api/jobs":      true, // polled 1s while a background job runs
	"/api/downloads": true, // polled 2s when SSE is disconnected
	"/api/events":    true, // persistent SSE stream (connection heartbeat)
	"/api/health":    true, // container/docker health probes
}

// skipAccessLog reports whether a request is a poll heartbeat rather than an
// action, and should be excluded from the access log at every level.
func skipAccessLog(r *http.Request) bool {
	if accessLogSkip[r.URL.Path] {
		return true
	}
	// Playlist detail is polled every 5s, but only for numeric IDs — the
	// /api/playlists/sources* browse endpoints are user-triggered actions and
	// must stay logged. PATCH/DELETE on the same path are actions too.
	if r.Method == http.MethodGet {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/api/playlists/"); ok {
			if _, err := strconv.Atoi(rest); err == nil {
				return true
			}
		}
	}
	return false
}

// accessIdentity is a request-scoped, mutable holder that lets withAccessLog
// learn who withAuth authenticated.
//
// Why a holder: withAccessLog wraps withAuth, and withAuth injects the resolved
// Identity into a *new* request context (contextWithIdentity) that the outer
// middleware never sees — it keeps the original *http.Request. A pointer
// installed in the original context is shared by value, so withAuth can fill it
// in and withAccessLog can read it after the chain returns.
//
// The mutex guards the record/read pair: resolution happens on the handler
// goroutine, but the holder is shared state and handlers may legitimately start
// work that touches it concurrently. Reads and writes are tiny, so contention
// is a non-issue.
//
// The raw API key is never stored here — only whether the request used it.
type accessIdentity struct {
	mu        sync.Mutex
	username  string
	viaAPIKey bool
}

// accessIdentityKey is the unexported context key type for *accessIdentity.
type accessIdentityKey struct{}

// withAccessIdentity returns a copy of ctx carrying holder.
func withAccessIdentity(ctx context.Context, holder *accessIdentity) context.Context {
	return context.WithValue(ctx, accessIdentityKey{}, holder)
}

// accessIdentityFrom returns the holder installed on ctx, or nil when none is
// present (e.g. withAuth exercised without the access-log middleware, as in
// tests). Callers must tolerate nil: record and snapshot are nil-safe.
func accessIdentityFrom(ctx context.Context) *accessIdentity {
	holder, _ := ctx.Value(accessIdentityKey{}).(*accessIdentity)
	return holder
}

// record stores the resolved identity on the holder. It is safe to call on a
// nil holder (the identity was resolved outside the access-log chain).
func (a *accessIdentity) record(id Identity) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.username = id.Username
	a.viaAPIKey = id.ViaAPIKey
}

// snapshot returns the recorded username and via-API-key flag, or zero values
// for a nil holder. Never returns the raw key.
func (a *accessIdentity) snapshot() (username string, viaAPIKey bool) {
	if a == nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.username, a.viaAPIKey
}

// withAccessLog writes one structured line per request to the dedicated access
// log (opt-in via logging.access_log). The app event log and stderr never see
// request lines. Polling endpoints are skipped entirely. A nil access log
// (feature disabled) is a no-op, so routing stays cheap when off.
//
// The line carries the resolved caller as "user" (blank when there is none)
// and "via_api_key" (true only for the global API key — the key itself is never
// logged). withAccessLog installs an accessIdentity holder before calling next;
// withAuth records the identity into it. 401 responses are still logged with a
// blank user, so failed auth remains visible.
func withAccessLog(accessLog io.Writer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wr := &responseWriter{ResponseWriter: w, status: http.StatusOK}

			// Install the holder before the chain runs so withAuth can fill it
			// in. Skip it when logging is disabled: no reader, no allocation.
			var holder *accessIdentity
			if accessLog != nil {
				holder = &accessIdentity{}
				r = r.WithContext(withAccessIdentity(r.Context(), holder))
			}

			next.ServeHTTP(wr, r)
			if accessLog == nil || skipAccessLog(r) {
				return
			}
			username, viaAPIKey := holder.snapshot()
			line, err := json.Marshal(map[string]any{
				"time":        time.Now().UTC().Format(time.RFC3339Nano),
				"remote_addr": clientAddr(r),
				"method":      r.Method,
				"path":        r.URL.Path,
				"proto":       r.Proto,
				"status":      wr.status,
				"bytes":       wr.bytes,
				"duration_ms": time.Since(start).Milliseconds(),
				"referer":     r.Referer(),
				"user_agent":  r.UserAgent(),
				"user":        username,
				"via_api_key": viaAPIKey,
			})
			if err != nil {
				return
			}
			_, _ = accessLog.Write(append(line, '\n'))
		})
	}
}

// clientAddr returns the real client address when the app sits behind a
// reverse proxy (nginx-style X-Forwarded-For first hop), falling back to the
// direct peer. XFF is client-suppliable — informational in the access log
// only, never used for anything security-sensitive.
func clientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if xff = strings.TrimSpace(xff); xff != "" {
			return xff
		}
	}
	return r.RemoteAddr
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// Flush implements http.Flusher so SSE connections work through the logging middleware.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer so http.ResponseController can reach the
// connection (e.g. SetWriteDeadline for streaming downloads, H4). Without this
// the access-log wrapper hides the real ResponseWriter, NewResponseController
// returns http.ErrNotSupported, and the global 30s WriteTimeout silently cuts
// every large download.
func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

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

// handleRateLimits reports active provider cooldowns and recent rate-limit
// events for observability (debug endpoint).
func (s *Server) handleRateLimits(w http.ResponseWriter, r *http.Request) {
	cooldowns, events := s.providerCooldown.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"cooldowns": cooldowns,
		"events":    events,
	})
}

// handleClearRateLimit removes a provider's cooldown manually (escape hatch
// for a provider parked by a long or malformed Retry-After).
func (s *Server) handleClearRateLimit(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider required"})
		return
	}
	removed := s.providerCooldown.Clear(provider)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"provider": provider,
		"removed":  removed,
	})
}

// handleSetupStatus reports whether the first-run setup wizard should be shown.
// It's driven by a setup_completed flag: true until the wizard's Done/Skip has
// been persisted, regardless of which sources happen to be configured.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": !s.cfg.Get().SetupCompleted})
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
	// The health checker owns the probe: it runs the check on its own loop
	// goroutine (or a single bounded goroutine when the loop is disabled).
	if len(rebuilt) > 0 && s.healthChecker != nil {
		s.healthChecker.RequestCheck(rebuilt)
	}

	// Sync metadata order with available providers before applying.
	// This ensures newly-configured providers (like Spotify dev mode)
	// appear in the order without manual UI reordering. The resolver and
	// enrichment handler read the order live from config via the shared
	// ProviderOrder, so persisting the synced order here propagates it.
	syncedMdOrder := mergeAvailableProviders(updated.MetadataOrder, s.mdRegistry.Available())
	if len(syncedMdOrder) > 0 && !stringSlicesEqual(syncedMdOrder, updated.MetadataOrder) {
		if err := s.cfg.Update(func(cfg *config.Config) error {
			cfg.MetadataOrder = syncedMdOrder
			return nil
		}); err != nil {
			s.log.Warn("failed to persist synced metadata order", "error", err, "component", "api")
		}
	}

	// Re-apply download source order, synced with connected providers.
	// The orchestrator and monitor/service read the order live from config via
	// the shared DownloadOrder provider, so persisting the synced order here is
	// sufficient to propagate it everywhere.
	if s.orchestrator != nil {
		syncedDlOrder := connectedNames(s.registry.Configured(), updated.DownloadOrder)
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

	// Apply logging changes live: level + rotation retention take effect
	// immediately. The serialization format is startup-only by design.
	if s.logRotator != nil && updated.Logging != nil {
		s.logRotator.SetLevel(updated.Logging.Level)
		s.logRotator.SetConfig(updated.Logging.LoggerConfig())
		if s.accessLog != nil {
			s.accessLog.SetConfig(updated.Logging.LoggerConfig())
		}
		s.log.Info("logging reconfigured", "level", s.logRotator.Level(), "component", "api")
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
	if offset < 0 {
		offset = 0
	}
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
