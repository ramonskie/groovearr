package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/download"
)

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
