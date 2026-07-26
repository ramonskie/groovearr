package spotify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/provider"
)

// Compile-time interface checks.
var _ download.Plugin = (*Plugin)(nil)
var _ metadata.Provider = (*Plugin)(nil)

// Plugin implements download.Plugin for Spotify metadata-only access.
// Free mode resolves Spotify URLs via oEmbed. Dev mode uses the
// authenticated Spotify Web API for search and playlist support.
// Spotify does NOT support downloading — all download methods are no-ops
// that return appropriate errors or empty results.
type Plugin struct {
	cfg    *SpotifyConfig
	dlPath string
	log    *slog.Logger

	// Dev-mode clients (nil in free mode).
	client *SpotifyClient
	api    *API

	// oembedClient is used for free-mode HTTP calls (oembed, connection check).
	// Defaults to http.DefaultClient. Injectable for tests.
	oembedClient *http.Client

	connected bool
	mu        sync.RWMutex
}

// NewPlugin creates a Spotify download plugin.
// In free mode the API client is nil — all operations use oEmbed.
// In dev mode an authenticated SpotifyClient and API wrapper are created.
func NewPlugin(cfg *SpotifyConfig, downloadPath string, logger *slog.Logger) *Plugin {
	p := &Plugin{
		cfg:        cfg,
		dlPath:     downloadPath,
		log:        logger,
		oembedClient: &http.Client{
			Transport: provider.NewRateLimitedTransport(http.DefaultTransport, spotifyOEmbedRate),
			Timeout:   15 * time.Second,
		},
	}
	if cfg.Mode == "dev" {
		p.client = NewClient(cfg, logger)
		p.api = NewAPI(p.client, logger)
	}
	return p
}

// ─── plugin.BasePlugin ────────────────────────────────────────────────

// Name returns the canonical source name.
func (p *Plugin) Name() string { return pluginName }

// DisplayName returns a human-readable label.
func (p *Plugin) DisplayName() string { return displayName }

// IsConfigured reports whether the plugin is ready to serve requests.
// Free mode is always ready. Dev mode requires client credentials (client_id + client_secret).
// Access tokens are obtained at runtime via OAuth — they are not part of configuration.
func (p *Plugin) IsConfigured() bool {
	if p.cfg.Mode == "free" {
		return true
	}
	return p.cfg.ClientID != "" && p.cfg.ClientSecret != ""
}

// CapabilityStatus returns per-capability connection status.
// In free mode only URL parsing is available. Discovery and playlist
// require dev mode with an authenticated Spotify Web API.
// Spotify never supports downloading — the "download" capability is omitted.
func (p *Plugin) CapabilityStatus() map[string]string {
	if p.cfg.Mode == "free" {
		return nil
	}

	s := "not_configured"
	if p.IsConfigured() {
		s = "configured"
		if p.Connected() {
			s = "connected"
		}
	}
	metaStatus := "not_configured"
	if p.cfg.Tokens.AccessToken != "" {
		metaStatus = s
	}
	return map[string]string{
		"metadata":  metaStatus,
		"playlist":  s,
		"discovery": s,
	}
}

// Connected returns the result of the last CheckConnection call.
func (p *Plugin) Connected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.connected
}

// CheckConnection verifies connectivity to the Spotify backend and
// updates the internal connected flag. The check varies by mode:
//   - free: HEAD request to open.spotify.com/oembed
//   - dev:  GET /v1/me via the Spotify Web API
func (p *Plugin) CheckConnection(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var err error
	if p.cfg.Mode == "free" {
		err = p.checkConnectionFree(ctx)
	} else {
		err = p.checkConnectionDev(ctx)
	}

	p.connected = (err == nil)
	return err
}

// ─── Connection checks (unexported) ───────────────────────────────────

func (p *Plugin) checkConnectionFree(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, oembedBaseURL, nil)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify connection check request creation failed", "error", err, "component", "spotify")
		}
		return fmt.Errorf("spotify: connection check: %w", err)
	}
	_, err = p.oembedClient.Do(req)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify connection check failed", "error", err, "component", "spotify")
		}
	}
	return err // network error → unreachable; any HTTP response → reachable
}

func (p *Plugin) checkConnectionDev(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SpotifyWebAPI+"/me", nil)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify connection check request creation failed", "error", err, "component", "spotify")
		}
		return fmt.Errorf("spotify: connection check: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify connection check failed", "error", err, "component", "spotify")
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("spotify: /me returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// ─── download.Plugin: Search ──────────────────────────────────────────

// Search resolves a query into track and album results.
//   - Free mode: query must be a Spotify URL (open.spotify.com/track/{id}).
//     The URL is parsed and metadata is extracted via oEmbed.
//   - Dev mode: query is a free-text search passed to the Spotify Web API.
func (p *Plugin) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	if p.cfg.Mode == "free" {
		return p.searchFree(ctx, query)
	}
	return p.searchDev(ctx, query)
}

// searchFree resolves a single Spotify URL via oEmbed and parses the
// embed HTML for track metadata. Returns one TrackResult and no albums.
// If the query is not a Spotify URL (e.g., a free-text search from the
// download orchestrator), returns empty results without an error.
func (p *Plugin) searchFree(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	su, err := ParseSpotifyURL(query)
	if err != nil {
		// Not a Spotify URL — free mode can't search free-text. Return empty
		// so the download orchestrator moves on to other sources silently.
		return nil, nil, nil
	}

	oembed, err := FetchOEmbedWithClient(ctx, p.oembedClient, query, p.log)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify free search oembed failed", "error", err, "component", "spotify")
		}
		return nil, nil, err
	}

	embed := ParseEmbedHTML(oembed.HTML)
	info := ExtractTrackInfo(oembed, embed)

	track := domain.TrackResult{
		SearchResult: domain.SearchResult{
			Username: pluginName,
			Filename: "spotify://" + su.Type + "/" + su.ID,
			Quality:  "metadata",
		},
		Artist:   info.Artist,
		Title:    info.Title,
		CoverURL: info.CoverURL,
	}

	return []domain.TrackResult{track}, nil, nil
}

// searchDev calls the Spotify Web API for track and album search.
// Album search failure is non-fatal — tracks are still returned.
func (p *Plugin) searchDev(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	paging, err := p.api.SearchTracks(ctx, query, 50, 0)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify search tracks failed", "error", err, "component", "spotify")
		}
		return nil, nil, err
	}

	tracks := make([]domain.TrackResult, len(paging.Items))
	for i, t := range paging.Items {
		tracks[i] = trackToResult(&t)
	}

	// Album search is best-effort.
	albumPaging, err := p.api.SearchAlbums(ctx, query, 20, 0)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify search albums failed (non-fatal)", "error", err, "component", "spotify")
		}
		return tracks, nil, nil
	}

	albums := make([]domain.AlbumResult, len(albumPaging.Items))
	for i, a := range albumPaging.Items {
		albums[i] = albumToResult(&a)
	}

	return tracks, albums, nil
}

// ─── download.Plugin: Download ────────────────────────────────────────

// Download always returns an error — Spotify does not support downloading.
func (p *Plugin) Download(_ context.Context, _, _ string, _ int64) (string, error) {
	return "", fmt.Errorf("spotify does not support downloading")
}

// ─── download.Plugin: Download tracking ───────────────────────────────

// GetDownloads returns an empty list — Spotify never has active downloads.
func (p *Plugin) GetDownloads(_ context.Context) ([]domain.DownloadRecord, error) {
	return nil, nil
}

// GetDownloadStatus always returns not-found — Spotify never downloads.
func (p *Plugin) GetDownloadStatus(_ context.Context, _ string) (*domain.DownloadRecord, error) {
	return nil, fmt.Errorf("spotify: no downloads")
}

// CancelDownload is a no-op — Spotify never has active downloads.
func (p *Plugin) CancelDownload(_ context.Context, _ string, _ bool) error {
	return nil
}

// ClearCompleted is a no-op — Spotify never accumulates completed downloads.
func (p *Plugin) ClearCompleted(_ context.Context) error {
	return nil
}

// ─── Type mappers ─────────────────────────────────────────────────────

// trackToResult maps a Spotify API Track to a domain.TrackResult.
func trackToResult(t *Track) domain.TrackResult {
	coverURL := ""
	if len(t.Album.Images) > 0 {
		coverURL = t.Album.Images[0].URL
	}
	return domain.TrackResult{
		SearchResult: domain.SearchResult{
			Username: pluginName,
			Filename: "spotify://track/" + t.ID,
			Duration: int64(t.DurationMs),
			Quality:  "metadata",
		},
		Artist:      joinArtists(t.Artists),
		Title:       t.Name,
		Album:       t.Album.Name,
		TrackNumber: t.TrackNumber,
		CoverURL:    coverURL,
	}
}

// albumToResult maps a Spotify SimplifiedAlbum to a domain.AlbumResult.
func albumToResult(a *SimplifiedAlbum) domain.AlbumResult {
	year := ""
	if len(a.ReleaseDate) >= 4 {
		year = a.ReleaseDate[:4]
	}
	return domain.AlbumResult{
		Username:        pluginName,
		AlbumTitle:      a.Name,
		Artist:          joinArtists(a.Artists),
		TrackCount:      a.TotalTracks,
		DominantQuality: "metadata",
		Year:            year,
	}
}

// joinArtists concatenates artist names with ", ".
func joinArtists(artists []SimplifiedArtist) string {
	if len(artists) == 0 {
		return ""
	}
	if len(artists) == 1 {
		return artists[0].Name
	}
	names := make([]string, len(artists))
	for i, a := range artists {
		names[i] = a.Name
	}
	result := names[0]
	for i := 1; i < len(names); i++ {
		result += ", " + names[i]
	}
	return result
}

// ─── discovery.Provider (dev mode only) ───────────────────────────────

func (p *Plugin) SearchArtists(ctx context.Context, query string, limit int) ([]discovery.ArtistSummary, error) {
	if p.api == nil {
		return nil, fmt.Errorf("spotify: discovery requires dev mode")
	}
	page, err := p.api.SearchArtists(ctx, query, limit, 0)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify search artists failed", "error", err, "component", "spotify")
		}
		return nil, err
	}
	var out []discovery.ArtistSummary
	for _, a := range page.Items {
		out = append(out, discovery.ArtistSummary{
			ProviderID: a.ID,
			Name:       a.Name,
			ImageURL:   bestImage(a.Images, 300),
			Genres:     a.Genres,
		})
	}
	return out, nil
}

func (p *Plugin) GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]discovery.AlbumResult, error) {
	if p.api == nil {
		return nil, fmt.Errorf("spotify: discovery requires dev mode")
	}
	return spotifyArtistAlbums(p.api, ctx, providerArtistID, limit, p.log)
}

func (p *Plugin) GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]discovery.TrackInfo, error) {
	if p.api == nil {
		return nil, fmt.Errorf("spotify: discovery requires dev mode")
	}
	return spotifyAlbumTracks(p.api, ctx, providerAlbumID, p.log)
}

func (p *Plugin) SearchAlbums(ctx context.Context, query string, limit int) ([]discovery.AlbumResult, error) {
	if p.api == nil {
		return nil, fmt.Errorf("spotify: discovery requires dev mode")
	}
	page, err := p.api.SearchAlbums(ctx, query, limit, 0)
	if err != nil {
		if p.log != nil {
			p.log.Error("spotify search albums failed", "error", err, "component", "spotify")
		}
		return nil, err
	}
	var out []discovery.AlbumResult
	for _, a := range page.Items {
		year, _ := strconv.Atoi(strings.Split(a.ReleaseDate, "-")[0])
		artistName := ""
		if len(a.Artists) > 0 {
			artistName = a.Artists[0].Name
		}
		out = append(out, discovery.AlbumResult{
			ProviderID:   a.ID,
			ProviderName: "spotify",
			ArtistName:   artistName,
			Title:        a.Name,
			Year:         year,
			CoverURL:     bestImage(a.Images, 300),
			TrackCount:   a.TotalTracks,
			Type:         strings.ToLower(a.AlbumType),
		})
	}
	return out, nil
}

// ─── metadata.Provider ────────────────────────────────────────────────

// IsMetadataAvailable returns true when Spotify is in dev mode with a valid
// access token. Free mode cannot perform metadata lookups via the API.
func (p *Plugin) IsMetadataAvailable() bool {
	return p.cfg.Mode == "dev" && p.cfg.Tokens.AccessToken != ""
}

// SearchAlbum finds the album title for a track via the Spotify Web API.
// Returns empty string when no match is found or the API is unavailable.
func (p *Plugin) SearchAlbum(ctx context.Context, artist, title string) string {
	if p.api == nil || artist == "" || title == "" {
		return ""
	}
	query := fmt.Sprintf("track:%s artist:%s", title, artist)
	result, err := p.api.SearchTracks(ctx, query, 3, 0)
	if err != nil {
		p.log.Warn("spotify search album failed", "error", err, "artist", artist, "title", title, "component", "spotify")
		return ""
	}
	for _, t := range result.Items {
		if t.Album.Name != "" {
			return t.Album.Name
		}
	}
	return ""
}

// SearchCover looks up album cover art via the Spotify Web API.
// Returns nil, nil when no match is found or parameters are empty.
func (p *Plugin) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	if p.api == nil || artist == "" || album == "" {
		return nil, nil
	}
	query := fmt.Sprintf("album:%s artist:%s", album, artist)
	result, err := p.api.SearchAlbums(ctx, query, 3, 0)
	if err != nil {
		p.log.Warn("spotify search cover failed", "error", err, "artist", artist, "album", album, "component", "spotify")
		return nil, err
	}
	for _, a := range result.Items {
		if len(a.Images) > 0 {
			img := a.Images[0]
			w, h := 0, 0
			if img.Width != nil {
				w = *img.Width
			}
			if img.Height != nil {
				h = *img.Height
			}
			thumbURL := ""
			if len(a.Images) > 2 {
				thumbURL = a.Images[2].URL
			} else if len(a.Images) > 1 {
				thumbURL = a.Images[1].URL
			}
			return &metadata.CoverResult{
				ImageURL: img.URL,
				Width:    w,
				Height:   h,
				Source:   "spotify",
				ThumbURL: thumbURL,
			}, nil
		}
	}
	return nil, nil
}

// SearchArtistImage looks up an artist image via the Spotify Web API.
// Returns nil, nil when no match is found or the artist name is empty.
func (p *Plugin) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	if p.api == nil || artist == "" {
		return nil, nil
	}
	result, err := p.api.SearchArtists(ctx, artist, 1, 0)
	if err != nil {
		p.log.Warn("spotify search artist image failed", "error", err, "artist", artist, "component", "spotify")
		return nil, err
	}
	for _, a := range result.Items {
		if len(a.Images) > 0 {
			img := a.Images[0]
			w, h := 0, 0
			if img.Width != nil {
				w = *img.Width
			}
			if img.Height != nil {
				h = *img.Height
			}
			thumbURL := ""
			if len(a.Images) > 2 {
				thumbURL = a.Images[2].URL
			} else if len(a.Images) > 1 {
				thumbURL = a.Images[1].URL
			}
			return &metadata.ArtistImageResult{
				ImageURL: img.URL,
				Width:    w,
				Height:   h,
				Source:   "spotify",
				ThumbURL: thumbURL,
			}, nil
		}
	}
	return nil, nil
}

// EnrichTrack fetches ISRC, genres, release date, and label for a track
// via the Spotify Web API. Uses the Spotify track ID from track.ExternalIDs
// when available (fast path). Otherwise searches by title.
// Returns nil, nil when no match is found or the API is unavailable.
func (p *Plugin) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	if p.api == nil || track.Title == "" {
		return nil, nil
	}

	var t *Track
	// Fast path: resolve by existing Spotify track ID.
	if id := track.ExternalIDs["spotify"]; id != "" {
		if resolved, err := p.api.GetTrack(ctx, id); err == nil {
			t = resolved
		} else {
			p.log.Warn("spotify get track failed", "error", err, "track_id", id, "component", "spotify")
		}
	}
	// Slow path: search by title.
	if t == nil {
		result, err := p.api.SearchTracks(ctx, track.Title, 1, 0)
		if err != nil {
			p.log.Warn("spotify enrich track search failed", "error", err, "track", track.Title, "component", "spotify")
			return nil, err
		}
		if len(result.Items) == 0 {
			return nil, nil
		}
		t = &result.Items[0]
	}

	meta := &metadata.TrackMetadata{
		ISRC:        t.ExternalIDs.ISRC,
		ReleaseDate: t.Album.ReleaseDate,
		ExternalIDs: map[string]string{"spotify": t.ID},
	}
	// Fetch full album details for label and genres.
	if t.Album.ID != "" {
		if album, err := p.api.GetAlbum(ctx, t.Album.ID); err == nil {
			meta.Label = album.Label
			meta.Genres = album.Genres
		} else {
			p.log.Warn("spotify get album failed", "error", err, "album_id", t.Album.ID, "component", "spotify")
		}
	}
	// Fallback to artist genres if album returned none.
	if len(meta.Genres) == 0 && len(t.Artists) > 0 && t.Artists[0].ID != "" {
		if artist, err := p.api.GetArtist(ctx, t.Artists[0].ID); err == nil {
			meta.Genres = artist.Genres
		} else {
			p.log.Warn("spotify get artist failed", "error", err, "artist_id", t.Artists[0].ID, "component", "spotify")
		}
	}
	return meta, nil
}
