package lastfm

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

const pluginName = "lastfm"
const displayName = "Last.fm"

// Compile-time interface checks.
var _ discovery.Provider = (*Plugin)(nil)
var _ metadata.Provider = (*Plugin)(nil)

// Plugin implements discovery.Provider for Last.fm metadata browsing.
type Plugin struct {
	client    *Client
	connected bool
	mu        sync.RWMutex
	log       *slog.Logger
}

// NewPlugin creates a Last.fm discovery plugin.
func NewPlugin(cfg LastFMConfig, logger *slog.Logger) *Plugin {
	return &Plugin{
		client: NewClient(cfg, logger),
		log:    logger,
	}
}

// ─── plugin.BasePlugin ────────────────────────────────────────────────

// Name returns the canonical plugin name.
func (p *Plugin) Name() string { return pluginName }

// DisplayName returns a human-readable label.
func (p *Plugin) DisplayName() string { return displayName }

// IsConfigured returns true if an API key is set.
func (p *Plugin) IsConfigured() bool { return p.client.apiKey != "" }

// Connected returns the result of the last CheckConnection call.
func (p *Plugin) Connected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.connected
}

// CheckConnection probes the Last.fm API with a lightweight search.
func (p *Plugin) CheckConnection(ctx context.Context) error {
	_, err := p.client.SearchArtists(ctx, "test", 1)
	p.mu.Lock()
	p.connected = err == nil
	p.mu.Unlock()
	if err != nil {
		p.log.Error("lastfm connection check failed", "error", err, "component", "lastfm")
		return fmt.Errorf("lastfm: connection check failed: %w", err)
	}
	return nil
}

// CapabilityStatus reports per-capability connection status.
func (p *Plugin) CapabilityStatus() map[string]string {
	s := "not_configured"
	if p.IsConfigured() {
		s = "configured"
		if p.Connected() {
			s = "connected"
		}
	}
	return map[string]string{"discovery": s, "metadata": s}
}

// ─── discovery.Provider ───────────────────────────────────────────────

// SearchArtists searches for artists by name on Last.fm.
func (p *Plugin) SearchArtists(ctx context.Context, query string, limit int) ([]discovery.ArtistSummary, error) {
	artists, err := p.client.SearchArtists(ctx, query, limit)
	if err != nil {
		p.log.Error("lastfm search artists failed", "error", err, "query", query, "component", "lastfm")
		return nil, err
	}
	out := make([]discovery.ArtistSummary, len(artists))
	for i, a := range artists {
		// Encode name+mbid so GetArtistAlbums can extract the human-readable artist name.
		providerID := a.Name
		if a.MBID != "" {
			providerID = a.Name + "||" + a.MBID
		}
		out[i] = discovery.ArtistSummary{
			ProviderID:   providerID,
			ProviderName: "lastfm",
			Name:         a.Name,
			ImageURL:     a.ImageURL,
		}
	}
	return out, nil
}

// parseArtistID extracts the human-readable artist name from a composite
// provider ID in the format "name" or "name||mbid".
func parseArtistID(id string) string {
	if idx := strings.Index(id, "||"); idx >= 0 {
		return id[:idx]
	}
	return id
}

// GetArtistAlbums returns an artist's top albums from Last.fm.
func (p *Plugin) GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]discovery.AlbumResult, error) {
	artistName := parseArtistID(providerArtistID)
	albums, err := p.client.GetArtistTopAlbums(ctx, artistName, limit)
	if err != nil {
		p.log.Error("lastfm get artist albums failed", "error", err, "artist", artistName, "component", "lastfm")
		return nil, err
	}
	var out []discovery.AlbumResult
	for _, a := range albums {
		albumID := a.MBID
		if albumID == "" {
			albumID = a.Name
		}
		// Encode as "artist::album" so GetAlbumTracks can extract both parts.
		providerID := artistName + "::" + albumID
		out = append(out, discovery.AlbumResult{
			ProviderID:   providerID,
			ProviderName: "lastfm",
			ArtistName:   artistName,
			Title:        a.Name,
			CoverURL:     a.ImageURL,
			Type:         "album",
		})
	}
	return out, nil
}

// GetAlbumTracks returns all tracks on a Last.fm album.
func (p *Plugin) GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]discovery.TrackInfo, error) {
	// providerAlbumID format: "artist::album" (Last.fm needs both to look up an album).
	parts := splitArtistAlbum(providerAlbumID)
	if len(parts) != 2 {
		p.log.Error("lastfm get album tracks invalid id format", "providerAlbumID", providerAlbumID, "component", "lastfm")
		return nil, fmt.Errorf("lastfm: invalid album id format: %s (expected artist::album)", providerAlbumID)
	}
	album, err := p.client.GetAlbumInfo(ctx, parts[0], parts[1])
	if err != nil {
		p.log.Error("lastfm get album info failed", "error", err, "artist", parts[0], "album", parts[1], "component", "lastfm")
		return nil, err
	}
	if album == nil {
		return nil, nil
	}
	out := make([]discovery.TrackInfo, len(album.Tracks))
	for i, t := range album.Tracks {
		durSec, _ := strconv.ParseInt(t.Duration, 10, 64)
		out[i] = discovery.TrackInfo{
			ArtistName:  album.Artist,
			AlbumTitle:  album.Name,
			Title:       t.Name,
			TrackNumber: i + 1,
			DiscNumber:  1,
			DurationMs:  durSec * 1000,
		}
	}
	return out, nil
}

// SearchAlbums searches for albums via artist search since Last.fm doesn't
// have a direct album search API.
func (p *Plugin) SearchAlbums(ctx context.Context, query string, limit int) ([]discovery.AlbumResult, error) {
	// Last.fm has no direct album search endpoint.
	// Try searching for artist first, then get their top albums.
	artists, err := p.client.SearchArtists(ctx, query, 1)
	if err != nil {
		p.log.Error("lastfm search albums via artist failed", "error", err, "query", query, "component", "lastfm")
		return nil, err
	}
	if len(artists) == 0 {
		return nil, nil
	}
	albums, err := p.client.GetArtistTopAlbums(ctx, artists[0].Name, limit)
	if err != nil {
		p.log.Error("lastfm search albums top albums failed", "error", err, "artist", artists[0].Name, "component", "lastfm")
		return nil, err
	}
	var out []discovery.AlbumResult
	for _, a := range albums {
		albumID := a.MBID
		if albumID == "" {
			albumID = a.Name
		}
		// Encode as "artist::album" so GetAlbumTracks can extract both parts.
		providerID := artists[0].Name + "::" + albumID
		out = append(out, discovery.AlbumResult{
			ProviderID:   providerID,
			ProviderName: "lastfm",
			ArtistName:   artists[0].Name,
			Title:        a.Name,
			CoverURL:     a.ImageURL,
			Type:         "album",
		})
	}
	return out, nil
}

// ─── metadata.Provider ────────────────────────────────────────────────

// IsMetadataAvailable returns true when the API key is configured.
func (p *Plugin) IsMetadataAvailable() bool { return p.client.apiKey != "" }

// SearchArtistImage looks up an artist image via Last.fm artist search.
func (p *Plugin) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	artists, err := p.client.SearchArtists(ctx, artist, 1)
	if err != nil || len(artists) == 0 {
		return nil, nil
	}
	if artists[0].ImageURL == "" {
		return nil, nil
	}
	return &metadata.ArtistImageResult{
		ImageURL: artists[0].ImageURL,
		Source:   "lastfm",
	}, nil
}

// SearchCover looks up album cover art via Last.fm album.getInfo.
func (p *Plugin) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	info, err := p.client.GetAlbumInfo(ctx, artist, album)
	if err != nil || info == nil {
		return nil, nil
	}
	if info.ImageURL == "" {
		return nil, nil
	}
	return &metadata.CoverResult{
		ImageURL: info.ImageURL,
		Source:   "lastfm",
	}, nil
}

// SearchAlbum finds an album title via Last.fm album.getInfo.
func (p *Plugin) SearchAlbum(ctx context.Context, artist, title string) string {
	info, err := p.client.GetAlbumInfo(ctx, artist, title)
	if err != nil || info == nil {
		return ""
	}
	return info.Name
}

// EnrichTrack is unsupported by the Last.fm API.
func (p *Plugin) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	return nil, nil
}

// splitArtistAlbum splits a "artist::album" id into its two parts.
func splitArtistAlbum(id string) []string {
	idx := strings.Index(id, "::")
	if idx < 0 {
		return []string{id}
	}
	return []string{id[:idx], id[idx+2:]}
}
