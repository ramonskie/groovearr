package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ramonskie/groovearr"
	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/playlist"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/sse"
)

// Server holds all dependencies for HTTP handlers.
type Server struct {
	cfg         *config.Persistence
	registry    *download.Registry
	mdRegistry  *metadata.Registry
	store       library.Store
	scanner     *library.Scanner
	downloadSvc *download.DownloadService
	eventBus    events.IEventAggregator
	sseHub      *sse.SSEHub
	matcher     *matching.Engine
	playlistSvc *playlist.Service
	httpSrv     *http.Server
}

// PluginRouteRegistrar is called after all standard routes are registered,
// giving plugins a chance to add their own HTTP endpoints.
type PluginRouteRegistrar func(mux *http.ServeMux)

func NewServer(addr string, cfg *config.Persistence, registry *download.Registry, mdRegistry *metadata.Registry, downloadSvc *download.DownloadService, store library.Store, scanner *library.Scanner, playlistSvc *playlist.Service, eventBus events.IEventAggregator, sseHub *sse.SSEHub, pluginRoutes ...PluginRouteRegistrar) *Server {
	s := &Server{
		cfg:         cfg,
		registry:    registry,
		mdRegistry:  mdRegistry,
		store:       store,
		scanner:     scanner,
		downloadSvc: downloadSvc,
		eventBus:    eventBus,
		sseHub:      sseHub,
		matcher:     matching.New(),
		playlistSvc: playlistSvc,
	}

	mux := http.NewServeMux()

	// Web UI — serve embedded static files with no-cache for development.
	// The embedded files are produced by `make build-ui` (Vite).  If the ui/dist/
	// directory is empty or missing, the Go binary was built without the UI.
	staticContent, err := fs.Sub(groovearr.UIFiles, "ui/dist")
	if err != nil {
		log.Fatalf("embedded UI files missing — run: make build-ui && go build ./cmd/groovearr: %v", err)
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
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handleUpdateConfig)
	mux.HandleFunc("GET /api/config/sources", s.handleGetSources)
	mux.HandleFunc("POST /api/config/test/{source}", s.handleTestConnection)
	mux.HandleFunc("POST /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/download", s.handleDownload)
	mux.HandleFunc("POST /api/download/match", s.handleDownloadBest)
	mux.HandleFunc("GET /api/downloads", s.handleGetDownloads)
	mux.HandleFunc("DELETE /api/downloads/{id}", s.handleCancelDownload)
	mux.HandleFunc("GET /api/library/tracks", s.handleLibraryTracks)
	mux.HandleFunc("GET /api/library/artists", s.handleLibraryArtists)
	mux.HandleFunc("GET /api/library/albums", s.handleLibraryAlbums)
	mux.HandleFunc("POST /api/library/scan", s.handleLibraryScan)
	mux.HandleFunc("GET /api/covers/{albumID}", s.handleCoverArt)

	// Playlist routes.
	mux.HandleFunc("GET /api/playlists/sources", s.handlePlaylistSources)
	mux.HandleFunc("GET /api/playlists/sources/{source}", s.handlePlaylistSourceBrowse)
	mux.HandleFunc("GET /api/playlists", s.handleListPlaylists)
	mux.HandleFunc("GET /api/playlists/{id}", s.handleGetPlaylist)
	mux.HandleFunc("POST /api/playlists/import", s.handleImportPlaylist)
	mux.HandleFunc("POST /api/playlists/{id}/download-missing", s.handleDownloadMissing)
	mux.HandleFunc("POST /api/playlists/{id}/sync", s.handleSyncPlaylist)
	mux.HandleFunc("DELETE /api/playlists/{id}", s.handleDeletePlaylist)

	// SSE endpoint for real-time download progress.
	mux.HandleFunc("GET /api/events", s.handleEvents)

	// Debug endpoint — full download state for troubleshooting.
	mux.HandleFunc("GET /api/debug/download/{id}", s.handleDebugDownload)

	// Let plugins register their own routes.
	for _, register := range pluginRoutes {
		register(mux)
	}

	s.httpSrv = &http.Server{Addr: addr, Handler: withLogging(withCORS(mux))}
	return s
}

// ListenAndServe starts the HTTP server (blocking).
func (s *Server) ListenAndServe() error {
	log.Printf("Groovearr listening on %s", s.httpSrv.Addr)
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// ─── Middleware ──────────────────────────────────────────────────────

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

	// Rebuild all configured plugins with new config.
	updated := s.cfg.Get()
	resources := plugin.PluginResources{DownloadPath: updated.Library.DownloadPath}
	for name := range updated.Sources {
		if err := s.registry.Rebuild(name, updated.Sources[name], resources); err != nil {
			log.Printf("reload %s: %v", name, err)
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
				log.Printf("mkdir %s: %v", p, err)
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
	var sources []map[string]any

	// Collect from download registry.
	for _, name := range s.registry.Names() {
		if p := s.registry.Get(name); p != nil {
			sources = append(sources, sourceEntry(name, p.DisplayName(), p.IsConfigured(), p.Connected()))
		}
	}

	// Collect from metadata registry.
	for _, name := range s.mdRegistry.Names() {
		if p := s.mdRegistry.Get(name); p != nil {
			sources = append(sources, sourceEntry(name, p.DisplayName(), p.IsConfigured(), p.Connected()))
		}
	}

	writeJSON(w, http.StatusOK, sources)
}

func sourceEntry(name, displayName string, configured, connected bool) map[string]any {
	status := "not_configured"
	if configured {
		status = "configured"
		if connected {
			status = "connected"
		}
	}
	return map[string]any{
		"name":         name,
		"display_name": displayName,
		"configured":   configured,
		"status":       status,
	}
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")

	// Check both registries.
	var p plugin.BasePlugin
	if dp := s.registry.Get(source); dp != nil {
		p = dp
	} else if mp := s.mdRegistry.Get(source); mp != nil {
		p = mp
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
		writeJSON(w, http.StatusOK, map[string]any{"status": "configured", "error": err.Error()})
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
					log.Printf("api: search %s failed: %v", p.Name(), err)
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	id, err := s.downloadSvc.Queue(ctx, req.Source, req.Username, req.Filename, req.Size, download.DownloadMeta{
		Artist:      req.Artist,
		Album:       req.Album,
		Title:       req.Title,
		TrackNumber: req.TrackNumber,
		DiscNumber:  req.DiscNumber,
		Year:        req.Year,
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
		Duration      int64  `json:"duration"`       // milliseconds (optional, 0 = neutral)
		ExcludeSource string `json:"exclude_source"` // source to skip (the one that just failed)
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

	// Create a search-only orchestrator with the handler's quality config.
	orch := download.NewOrchestrator(s.registry, func() config.QualityConfig {
		return s.cfg.Get().Quality
	})

	best, err := orch.FindBestMatch(ctx, req.Title, req.Artist, req.Duration, req.ExcludeSource)
	if err != nil {
		writeJSON(w, http.StatusNotFound, err)
		return
	}

	username := best.Track.Username

	id, err := s.downloadSvc.Queue(ctx, best.SourceName, username, best.Track.Filename, best.Track.Size, download.DownloadMeta{
		Artist:      best.Track.Artist,
		Album:       best.Track.Album,
		Title:       best.Track.Title,
		TrackNumber: best.Track.TrackNumber,
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
	downloads, err := s.downloadSvc.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if downloads == nil {
		downloads = []domain.DownloadRecord{}
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
			log.Printf("scanner: error scanning %s: %v", p, err)
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
	if _, err := os.Stat(coverPath); os.IsNotExist(err) {
		http.Error(w, "cover not found", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, coverPath)
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Source == "" || req.PlaylistID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("source and playlist_id required"))
		return
	}

	result, err := s.playlistSvc.ImportPlaylist(r.Context(), req.Source, req.PlaylistID)
	if err != nil {
		log.Printf("playlist: import %s/%s failed: %v", req.Source, req.PlaylistID, err)
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
			log.Printf("playlist sync %d: %v", id, err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
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
		"download":    dl,
		"state":       dl.State,
		"terminal":    dl.State.Terminal(),
		"playlist_id": dl.PlaylistID,
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
