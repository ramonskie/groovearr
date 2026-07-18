package api

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	deezerdl "github.com/ramonskie/groovearr/internal/download/deezer"
	"github.com/ramonskie/groovearr/internal/download/soulseek"
	"github.com/ramonskie/groovearr/internal/library"
)

//go:embed static/*
var staticFS embed.FS

// Server holds all dependencies for HTTP handlers.
type Server struct {
	cfg      *config.Persistence
	orch     *download.Orchestrator
	store    library.Store
	scanner  *library.Scanner
	postProc *download.PostProcessor
	httpSrv  *http.Server
}

// NewServer creates an HTTP server with all routes wired.
func NewServer(addr string, cfg *config.Persistence, orch *download.Orchestrator, store library.Store, scanner *library.Scanner, postProc *download.PostProcessor) *Server {
	s := &Server{
		cfg:      cfg,
		orch:     orch,
		store:    store,
		scanner:  scanner,
		postProc: postProc,
	}

	mux := http.NewServeMux()

	// Web UI — serve embedded static files with no-cache for development.
	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("embedded static files: %v", err)
	}
	mux.Handle("GET /", noCache(http.FileServer(http.FS(staticContent))))

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

	s.httpSrv = &http.Server{Addr: addr, Handler: withCORS(mux)}
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
	cfg := s.cfg.Get()
	// Mask API key partially.
	if cfg.Soulseek.APIKey != "" && len(cfg.Soulseek.APIKey) > 4 {
		masked := cfg.Soulseek.APIKey
		cfg.Soulseek.APIKey = masked[:2] + strings.Repeat("*", len(masked)-4) + masked[len(masked)-2:]
	}
	writeJSON(w, http.StatusOK, cfg)
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
		mergeConfig(&merged, &partial)

		if errs := merged.Validate(); len(errs) > 0 {
			return &validationError{errs}
		}

		mergeConfig(cfg, &partial)
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

	// Rebuild clients with new config.
	s.reloadSoulseek()
	s.reloadDeezer()

	// Ensure required directories exist.
	updated := s.cfg.Get()
	for _, p := range []string{updated.Soulseek.DownloadPath, updated.Library.LibraryPath} {
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
	names := s.orch.Registry().Names()
	sources := make([]map[string]any, 0, len(names))
	for _, name := range names {
		p := s.orch.Registry().Get(name)
		cfg := p.IsConfigured()
		status := "not_configured"
		if cfg {
			status = "configured"
			if p.Connected() {
				status = "connected"
			}
		}
		sources = append(sources, map[string]any{
			"name":         name,
			"display_name": p.DisplayName(),
			"configured":   cfg,
			"status":       status,
		})
	}
	writeJSON(w, http.StatusOK, sources)
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	p := s.orch.Registry().Get(source)
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
	tracks, albums, err := s.orch.Search(ctx, req.Source, req.Query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
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
		Source   string `json:"source"`
		Username string `json:"username"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	id, err := s.orch.Download(ctx, req.Source, req.Username, req.Filename, req.Size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"download_id": id})
}

// handleDownloadBest searches across configured sources for a matching track
// and downloads the best candidate. Used for cross-source fallback on album downloads.
func (s *Server) handleDownloadBest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title         string `json:"title"`
		Artist        string `json:"artist"`
		Duration      int64  `json:"duration"`       // milliseconds (optional, 0 = neutral)
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
	id, source, confidence, err := s.orch.DownloadBest(ctx, req.Title, req.Artist, req.Duration, req.ExcludeSource)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"download_id": id,
		"source":      source,
		"confidence":  confidence,
	})
}

func (s *Server) handleGetDownloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	downloads := s.orch.GetDownloads(ctx)
	if downloads == nil {
		downloads = []domain.DownloadRecord{}
	}

	// Run post-download hooks (e.g., file renaming).
	if s.postProc != nil {
		before := make(map[string]string, len(downloads))
		for _, d := range downloads {
			before[d.ID] = d.FilePath
		}

		s.postProc.ProcessDownloads(ctx, downloads)

		// Record path overrides so the orchestrator serves corrected paths on next poll.
		for _, d := range downloads {
			if newPath := d.FilePath; newPath != "" && newPath != before[d.ID] {
				s.orch.SetDownloadPath(d.ID, newPath)
			}
		}
	}

	writeJSON(w, http.StatusOK, downloads)
}

func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.orch.CancelDownload(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// ─── Library handlers ────────────────────────────────────────────────

func (s *Server) handleLibraryTracks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tracks, err := s.store.SearchTracks(ctx, "", 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if tracks == nil {
		tracks = []domain.Track{}
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (s *Server) handleLibraryArtists(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	artists, err := s.store.ListArtists(ctx, 0, 200)
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
	ctx := r.Context()
	albums, err := s.store.SearchAlbums(ctx, "", 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.Album{}
	}
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

// ─── Helpers ─────────────────────────────────────────────────────────

func (s *Server) reloadSoulseek() {
	cfg := s.cfg.Get()
	slskd := soulseek.New(cfg.Soulseek)
	if err := s.orch.Registry().Replace("soulseek", slskd); err != nil {
		log.Printf("reload soulseek: %v", err)
	}
}

func (s *Server) reloadDeezer() {
	cfg := s.cfg.Get()
	dl := deezerdl.NewDownloadClient(cfg.Deezer, cfg.Soulseek.DownloadPath)
	if err := s.orch.Registry().Replace("deezer", dl); err != nil {
		log.Printf("reload deezer: %v", err)
	}
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

// mergeConfig copies non-zero fields from partial into dst.
// Extracted from handleUpdateConfig so validation can preview the merge result.
func mergeConfig(dst, partial *config.Config) {
	if partial.Soulseek.SlskdURL != "" {
		dst.Soulseek.SlskdURL = partial.Soulseek.SlskdURL
	}
	if partial.Soulseek.APIKey != "" {
		dst.Soulseek.APIKey = partial.Soulseek.APIKey
	}
	if partial.Soulseek.DownloadPath != "" {
		dst.Soulseek.DownloadPath = partial.Soulseek.DownloadPath
	}
	if partial.Soulseek.SearchTimeout > 0 {
		dst.Soulseek.SearchTimeout = partial.Soulseek.SearchTimeout
	}
	if partial.Soulseek.MinUploadSpeed > 0 {
		dst.Soulseek.MinUploadSpeed = partial.Soulseek.MinUploadSpeed
	}
	if partial.Deezer.ARL != "" {
		dst.Deezer.ARL = partial.Deezer.ARL
	}
	if partial.Deezer.Quality != "" {
		dst.Deezer.Quality = partial.Deezer.Quality
	}
	if partial.Deezer.AccessToken != "" {
		dst.Deezer.AccessToken = partial.Deezer.AccessToken
	}
	if partial.Deezer.AllowFallback != nil {
		dst.Deezer.AllowFallback = partial.Deezer.AllowFallback
	}

	if partial.Library.FolderTemplate != "" {
		dst.Library.FolderTemplate = partial.Library.FolderTemplate
	}
	if partial.Library.LibraryPath != "" {
		dst.Library.LibraryPath = partial.Library.LibraryPath
	}

	if partial.Quality.PreferredFormat != "" {
		dst.Quality.PreferredFormat = partial.Quality.PreferredFormat
	}
	if partial.Quality.MinBitrate > 0 {
		dst.Quality.MinBitrate = partial.Quality.MinBitrate
	}
}
