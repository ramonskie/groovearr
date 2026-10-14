package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/strutil"
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
	// Drop providers still in rate-limit cooldown — they would just fail again.
	// Track whether any were dropped so the empty result is explainable.
	cooledDown := false
	if len(providers) > 0 {
		active := providers[:0]
		for _, p := range providers {
			if s.providerCooldown.CoolingDown(p.Name()) {
				cooledDown = true
				continue
			}
			active = append(active, p)
		}
		providers = active
	}
	if len(providers) == 0 {
		if cooledDown {
			// 503 (not 429): the client isn't throttled — the upstream
			// providers are. 429 would read as "slow down" to the caller.
			s.log.Warn("discover search: all providers cooling down",
				"component", "discover")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error":   "all discovery providers are rate-limited, retry shortly",
				"artists": []discovery.ArtistSummary{},
				"albums":  []discovery.AlbumResult{},
			})
		} else {
			s.log.Warn("discover search: no discovery providers found",
				"component", "discover")
			writeJSON(w, http.StatusOK, map[string]any{
				"artists": []discovery.ArtistSummary{},
				"albums":  []discovery.AlbumResult{},
			})
		}
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
					s.noteProviderRateLimit(provider.Name(), err)
					s.log.Warn("discover search artists error",
						"provider", provider.Name(), "error", err, "component", "discover")
					results[idx].err = err
				}
				results[idx].artists = a
			}
			if searchType == "" || searchType == "album" {
				a, err := provider.SearchAlbums(ctx, discovery.AlbumQuery{Text: q}, 10)
				if err != nil {
					s.noteProviderRateLimit(provider.Name(), err)
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
	// MetadataPending is an additive hint: true when the artist overview is not
	// yet materialized in the catalog (a cold read with no local artist, or a
	// provider/cooldown fallback). The pre-existing fields keep their shape so
	// the UI reads them unchanged.
	MetadataPending bool `json:"metadata_pending"`
}

// handleDiscoverArtistOverview returns an artist overview (identity, top
// tracks, discography) for a library artist name. It is served WARM from the
// materialized catalog — artist_metadata + artist_discography — with zero
// provider calls; only a cold artist (no catalog row) falls back to one bounded
// provider fetch that is then materialized into the catalog — and only when the
// catalog is enabled (a disabled catalog skips the fetch and the writes, serving
// the pending shape). There is no TTL JSON cache.
func (s *Server) handleDiscoverArtistOverview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	s.log.Info("discover artist overview", "query", q, "component", "discover")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query parameter 'q' is required"})
		return
	}

	ctx := r.Context()

	// The catalog is keyed by the local artist id, so resolve the library
	// artist first. A name not in the library has no warm row and is fetched
	// lazily below (materialization is skipped when it cannot be keyed).
	artist := s.lookupLibraryArtist(ctx, q)
	var artistID int64
	displayName := q
	if artist != nil {
		artistID = artist.ID
		if artist.Name != "" {
			displayName = artist.Name
		}
	}

	// ── Warm read: catalog only, zero provider calls ──
	if resp, ok := s.warmArtistOverview(ctx, artistID, displayName); ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// ── Cold read — catalog disabled: never fetch a provider or write ──
	// A disabled catalog must not materialize anything, so the cold path skips
	// the provider fetch and the artist_metadata/artist_discography writes. The
	// warm read above already missed, so there is nothing materialized to
	// serve; return the pending shape (local identity only) without error.
	if !s.isCatalogEnabled() {
		s.log.Info("discover artist overview: catalog disabled, skipping provider fetch",
			"query", q, "component", "discover")
		writeJSON(w, http.StatusOK, pendingArtistOverview(artist, q))
		return
	}

	queryKey := normalizeKey(q)

	// ── Cold read — resolve artist and fetch fresh data ──
	if s.discoveryReg == nil || len(s.discoveryReg.Any()) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no discovery providers configured"})
		return
	}

	// 1. Resolve artist (same logic as handleDiscoverResolveArtist). Providers
	// parked in rate-limit cooldown are dropped — they would only fail again.
	providers := s.activeDiscoveryProviders()

	const searchTimeout = 5 * time.Second
	searchCtx, cancel := context.WithTimeout(r.Context(), searchTimeout)
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
			a, err := provider.SearchArtists(searchCtx, q, 10)
			if err != nil {
				s.noteProviderRateLimit(provider.Name(), err)
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

	if r.Context().Err() != nil {
		return
	}

	if resolved == nil {
		// No provider match. A known library artist still gets its local
		// identity back as pending instead of a hard 404.
		if artist != nil {
			s.log.Info("discover artist overview: no provider match", "query", q, "component", "discover")
			writeJSON(w, http.StatusOK, pendingArtistOverview(artist, q))
			return
		}
		s.log.Info("discover artist overview: artist not found", "query", q, "component", "discover")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artist not found"})
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
			s.noteProviderRateLimit(fetchProvider.Name(), err)
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

	// 4. Materialize into the catalog so the next read is warm and provider-free.
	// The catalog is keyed by the local artist id, so this is skipped for a name
	// that has no library artist. A write failure is logged, never fatal.
	if artistID != 0 && s.artistCatalog != nil {
		releases := buildArtistDiscography(artistID, resolved.ProviderName, albums, time.Now().UTC())
		if merr := s.materializeArtistOverview(ctx, artistID, resolved, releases); merr != nil {
			s.log.Warn("artist overview materialization failed", "query", q, "error", merr, "component", "discover")
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

// lookupLibraryArtist resolves the local artist row by name so the catalog —
// keyed by the local artist id — can serve a warm read. A miss is a normal cold
// state, not an error.
func (s *Server) lookupLibraryArtist(ctx context.Context, name string) *domain.Artist {
	if s.store == nil {
		return nil
	}
	artist, err := s.store.GetArtistByName(ctx, name)
	if err != nil {
		s.log.Warn("discover artist overview: local artist lookup failed",
			"query", name, "error", err, "component", "discover")
		return nil
	}
	return artist
}

// activeDiscoveryProviders returns the configured discovery providers with any
// parked in rate-limit cooldown dropped — they would only fail again.
func (s *Server) activeDiscoveryProviders() []discovery.Provider {
	if s.discoveryReg == nil {
		return nil
	}
	providers := s.discoveryReg.Configured()
	if len(providers) == 0 {
		providers = s.discoveryReg.Any()
	}
	active := make([]discovery.Provider, 0, len(providers))
	for _, p := range providers {
		if s.providerCooldown.CoolingDown(p.Name()) {
			continue
		}
		active = append(active, p)
	}
	return active
}

// warmArtistOverview serves the artist overview purely from the catalog
// (artist_metadata + artist_discography). It returns ok=false when no
// materialized artist_metadata row exists, so the caller can fall back to a
// bounded provider fetch. A warm read makes zero provider calls. Top tracks are
// not materialized in the catalog yet (artist refresh engine pending), so the
// list is empty.
func (s *Server) warmArtistOverview(ctx context.Context, artistID int64, displayName string) (*artistOverviewResponse, bool) {
	if artistID == 0 || s.artistCatalog == nil {
		return nil, false
	}
	meta, err := s.artistCatalog.GetArtistMetadata(ctx, artistID)
	if err != nil {
		s.log.Warn("catalog artist metadata read failed", "artist_id", artistID, "error", err, "component", "discover")
		return nil, false
	}
	if meta == nil {
		return nil, false
	}
	releases, err := s.artistCatalog.GetArtistDiscography(ctx, artistID)
	if err != nil {
		s.log.Warn("catalog artist discography read failed", "artist_id", artistID, "error", err, "component", "discover")
		releases = nil
	}
	return &artistOverviewResponse{
		Artist: discovery.ArtistSummary{
			ProviderID:   meta.ProviderArtistID,
			ProviderName: meta.ProviderName,
			Name:         displayName,
			ImageURL:     meta.ImageURL,
			Genres:       meta.Genres,
		},
		TopTracks:   []discovery.TrackInfo{},
		Discography: countArtistDiscography(releases),
	}, true
}

// materializeArtistOverview writes the fetched artist overview and discography
// into the durable catalog so the next read is warm. The two writes are short,
// independent statements; the first error is returned so the caller can log it
// without failing the response.
func (s *Server) materializeArtistOverview(ctx context.Context, artistID int64, resolved *discovery.ArtistSummary, releases []catalog.ArtistDiscographyRelease) error {
	now := time.Now().UTC()
	if err := s.artistCatalog.UpsertArtistMetadata(ctx, catalog.ArtistMetadata{
		ArtistID:         artistID,
		ProviderName:     resolved.ProviderName,
		ProviderArtistID: resolved.ProviderID,
		ImageURL:         resolved.ImageURL,
		Genres:           resolved.Genres,
		RefreshStatus:    catalog.StatusFresh,
		LastRefreshedAt:  now,
		UpdatedAt:        now,
	}); err != nil {
		return fmt.Errorf("upsert artist metadata: %w", err)
	}
	if err := s.artistCatalog.ReplaceArtistDiscography(ctx, artistID, releases); err != nil {
		return fmt.Errorf("replace artist discography: %w", err)
	}
	return nil
}

// buildArtistDiscography maps provider album results to catalog discography
// releases for one artist. An empty album type defaults to "album".
func buildArtistDiscography(artistID int64, providerName string, albums []discovery.AlbumResult, now time.Time) []catalog.ArtistDiscographyRelease {
	releases := make([]catalog.ArtistDiscographyRelease, 0, len(albums))
	for i := range albums {
		a := albums[i]
		pname := a.ProviderName
		if pname == "" {
			pname = providerName
		}
		releaseType := a.Type
		if releaseType == "" {
			releaseType = "album"
		}
		releases = append(releases, catalog.ArtistDiscographyRelease{
			ArtistID:        artistID,
			ProviderName:    pname,
			ProviderAlbumID: a.ProviderID,
			Title:           a.Title,
			Year:            a.Year,
			Type:            releaseType,
			CoverURL:        a.CoverURL,
			TrackCount:      a.TrackCount,
			LastSeenAt:      now,
		})
	}
	return releases
}

// countArtistDiscography counts materialized releases by type for the UI's
// discography summary. An empty type defaults to "album".
func countArtistDiscography(releases []catalog.ArtistDiscographyRelease) map[string]int {
	out := make(map[string]int, len(releases))
	for i := range releases {
		t := releases[i].Type
		if t == "" {
			t = "album"
		}
		out[t]++
	}
	return out
}

// pendingArtistOverview is the additive "cold / not materialized" shape: it
// carries whatever local identity is known so the UI can render the artist while
// the catalog entry is still pending. Top tracks and discography are empty.
func pendingArtistOverview(artist *domain.Artist, q string) artistOverviewResponse {
	out := artistOverviewResponse{
		Artist:          discovery.ArtistSummary{Name: q},
		TopTracks:       []discovery.TrackInfo{},
		Discography:     map[string]int{},
		MetadataPending: true,
	}
	if artist != nil {
		out.Artist.Name = artist.Name
		out.Artist.Genres = artist.Genres
	}
	return out
}

// normalizeKey strips accents, lowercases, and removes non-alphanumeric for dedup.
func normalizeKey(s string) string { return strutil.NormalizeName(s) }

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

	// Album-acquisition policy (album-first vs per-track) lives ONLY in
	// download.Service.QueueAlbumWithFallback — do not re-implement it here.
	trackQueues := make([]download.TrackQueue, 0, len(tracks))
	for _, t := range tracks {
		trackQueues = append(trackQueues, download.TrackQueue{
			Artist:      t.ArtistName,
			Album:       t.AlbumTitle,
			Title:       t.Title,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.DiscNumber,
			ISRC:        t.ISRC,
		})
	}

	cfg := s.cfg.Get()
	res, err := s.downloadSvc.QueueAlbumWithFallback(ctx, requesterID(ctx), requesterUsername(ctx), artistName, albumName, trackQueues, cfg.DownloadClient, cfg.AlbumSources)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if res.Mode == download.QueueModeAlbum {
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":        "album",
			"download_id": res.DownloadID,
			"artist":      artistName,
			"album":       albumName,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":   "track",
		"queued": res.Queued,
		"total":  res.Total,
		"errors": res.Errors,
	})
}
