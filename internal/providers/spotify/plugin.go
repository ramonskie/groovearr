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
	"github.com/ramonskie/groovearr/internal/provider"
)

// Compile-time interface check.
var _ download.Plugin = (*Plugin)(nil)

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
