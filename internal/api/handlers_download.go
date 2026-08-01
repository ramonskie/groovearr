package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
)

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
