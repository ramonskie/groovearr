package discogs

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

const pluginName = "discogs"
const displayName = "Discogs"

// Compile-time interface checks.
var _ discovery.Provider = (*Plugin)(nil)
var _ metadata.Provider = (*Plugin)(nil)

// Plugin implements discovery.Provider for Discogs metadata browsing.
type Plugin struct {
	client    *Client
	connected bool
	mu        sync.RWMutex
	log       *slog.Logger
}

// NewPlugin creates a Discogs discovery plugin.
func NewPlugin(cfg DiscogsConfig, logger *slog.Logger) *Plugin {
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

// IsConfigured returns true — Discogs public API is always available.
func (p *Plugin) IsConfigured() bool { return true }

// Connected returns the result of the last CheckConnection call.
func (p *Plugin) Connected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.connected
}

// CheckConnection probes the Discogs API with a lightweight search.
func (p *Plugin) CheckConnection(ctx context.Context) error {
	_, err := p.client.SearchArtists(ctx, "test", 1)
	p.mu.Lock()
	p.connected = err == nil
	p.mu.Unlock()
	if err != nil {
		p.log.Error("discogs connection check failed", "error", err, "component", "discogs")
		return fmt.Errorf("discogs: connection check failed: %w", err)
	}
	return nil
}

// CapabilityStatus reports per-capability connection status.
func (p *Plugin) CapabilityStatus() map[string]string {
	s := "configured"
	if p.Connected() {
		s = "connected"
	}
	return map[string]string{"discovery": s, "metadata": s}
}

// ─── discovery.Provider ───────────────────────────────────────────────

// SearchArtists searches for artists by name on Discogs.
// If search results lack images, fetches full artist detail as fallback.
func (p *Plugin) SearchArtists(ctx context.Context, query string, limit int) ([]discovery.ArtistSummary, error) {
	artists, err := p.client.SearchArtists(ctx, query, limit)
	if err != nil {
		p.log.Error("discogs search artists failed", "error", err, "query", query, "component", "discogs")
		return nil, err
	}
	out := make([]discovery.ArtistSummary, len(artists))
	for i, a := range artists {
		imageURL := a.ImageURL
		if imageURL == "" && a.Thumb != "" {
			imageURL = a.Thumb
		}
		// Fallback: fetch full artist detail for better images.
		if imageURL == "" {
			detail, detailErr := p.client.GetArtist(ctx, a.ID)
			if detailErr != nil {
				p.log.Debug("discogs get artist image fallback failed",
					"artist", a.Name, "error", detailErr, "component", "discogs")
			} else if detail != nil && len(detail.Images) > 0 {
				// Prefer uri150 (thumbnail), fall back to full uri.
				imageURL = detail.Images[0].URI150
				if imageURL == "" {
					imageURL = detail.Images[0].URI
				}
			}
		}
		out[i] = discovery.ArtistSummary{
			ProviderID: strconv.Itoa(a.ID),
			Name:       a.Name,
			ImageURL:   imageURL,
		}
	}
	return out, nil
}

// GetArtistAlbums returns an artist's releases (albums, singles, etc.) from Discogs.
func (p *Plugin) GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]discovery.AlbumResult, error) {
	artistID, err := strconv.Atoi(providerArtistID)
	if err != nil {
		p.log.Error("discogs get artist albums invalid id", "error", err, "providerArtistID", providerArtistID, "component", "discogs")
		return nil, fmt.Errorf("discogs: invalid artist id: %w", err)
	}
	releases, err := p.client.GetArtistReleases(ctx, artistID, limit)
	if err != nil {
		p.log.Error("discogs get artist releases failed", "error", err, "artistID", artistID, "component", "discogs")
		return nil, err
	}
	var out []discovery.AlbumResult
	for i, r := range releases {
		if i >= limit {
			break
		}
		releaseType := r.Type
		if releaseType == "master" {
			releaseType = "album"
		}
		artistName := r.Artist
		out = append(out, discovery.AlbumResult{
			ProviderID:   strconv.Itoa(r.ID),
			ProviderName: "discogs",
			ArtistName:   artistName,
			Title:        r.Title,
			Year:         int(r.Year),
			CoverURL:     r.Thumb,
			Type:         releaseType,
		})
	}
	return out, nil
}

// GetAlbumTracks returns all tracks on a Discogs release.
func (p *Plugin) GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]discovery.TrackInfo, error) {
	releaseID, err := strconv.Atoi(providerAlbumID)
	if err != nil {
		p.log.Error("discogs get album tracks invalid id", "error", err, "providerAlbumID", providerAlbumID, "component", "discogs")
		return nil, fmt.Errorf("discogs: invalid release id: %w", err)
	}
	release, err := p.client.GetRelease(ctx, releaseID)
	if err != nil {
		p.log.Error("discogs get release failed", "error", err, "releaseID", releaseID, "component", "discogs")
		return nil, err
	}
	if release == nil {
		return nil, nil
	}
	out := make([]discovery.TrackInfo, len(release.Tracklist))
	for i, t := range release.Tracklist {
		trackNum := i + 1
		if t.Position != "" {
			if n, err := strconv.Atoi(t.Position); err == nil {
				trackNum = n
			}
		}
		out[i] = discovery.TrackInfo{
			ArtistName:  release.ArtistName,
			AlbumTitle:  release.Title,
			Title:       t.Title,
			TrackNumber: trackNum,
			DiscNumber:  1,
			DurationMs:  parseDuration(t.Duration),
		}
	}
	return out, nil
}

// SearchAlbums searches for releases by query on Discogs.
func (p *Plugin) SearchAlbums(ctx context.Context, query string, limit int) ([]discovery.AlbumResult, error) {
	releases, err := p.client.SearchAlbums(ctx, query, limit)
	if err != nil {
		p.log.Error("discogs search albums failed", "error", err, "query", query, "component", "discogs")
		return nil, err
	}
	var out []discovery.AlbumResult
	for _, r := range releases {
		releaseType := r.Type
		if releaseType == "master" {
			releaseType = "album"
		}
		artistName := r.Artist
		out = append(out, discovery.AlbumResult{
			ProviderID:   strconv.Itoa(r.ID),
			ProviderName: "discogs",
			ArtistName:   artistName,
			Title:        r.Title,
			Year:         int(r.Year),
			CoverURL:     r.Thumb,
			Type:         releaseType,
		})
	}
	return out, nil
}

// ─── metadata.Provider ────────────────────────────────────────────────

// IsMetadataAvailable is always true — the public Discogs API is free.
func (p *Plugin) IsMetadataAvailable() bool { return true }

// SearchArtistImage looks up an artist image via Discogs search, with artist detail fallback.
func (p *Plugin) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	artists, err := p.client.SearchArtists(ctx, artist, 1)
	if err != nil || len(artists) == 0 {
		return nil, nil
	}
	imageURL := artists[0].ImageURL
	if imageURL == "" && artists[0].Thumb != "" {
		imageURL = artists[0].Thumb
	}
	// Fallback: fetch full artist detail for better images.
	if imageURL == "" {
		detail, detailErr := p.client.GetArtist(ctx, artists[0].ID)
		if detailErr != nil {
			p.log.Debug("discogs get artist image fallback failed",
				"artist", artist, "error", detailErr, "component", "discogs")
		} else if detail != nil && len(detail.Images) > 0 {
			imageURL = detail.Images[0].URI150
			if imageURL == "" {
				imageURL = detail.Images[0].URI
			}
		}
	}
	if imageURL == "" {
		return nil, nil
	}
	return &metadata.ArtistImageResult{
		ImageURL: imageURL,
		Source:   "discogs",
	}, nil
}

// SearchCover looks up album cover art via Discogs release search.
func (p *Plugin) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	query := artist + " " + album
	releases, err := p.client.SearchAlbums(ctx, query, 1)
	if err != nil || len(releases) == 0 {
		return nil, nil
	}
	if releases[0].Thumb == "" {
		return nil, nil
	}
	return &metadata.CoverResult{
		ImageURL: releases[0].Thumb,
		Source:   "discogs",
	}, nil
}

// SearchAlbum finds an album title for a track via Discogs release search.
func (p *Plugin) SearchAlbum(ctx context.Context, artist, title string) string {
	releases, err := p.client.SearchAlbums(ctx, artist+" "+title, 1)
	if err != nil || len(releases) == 0 {
		return ""
	}
	return releases[0].Title
}

// EnrichTrack fetches release metadata (release year) via Discogs release lookup.
func (p *Plugin) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	releases, err := p.client.SearchAlbums(ctx, track.Title, 1)
	if err != nil || len(releases) == 0 {
		return nil, nil
	}
	release, err := p.client.GetRelease(ctx, releases[0].ID)
	if err != nil || release == nil {
		return nil, nil
	}
	md := &metadata.TrackMetadata{}
	if release.Year > 0 {
		md.ReleaseDate = strconv.Itoa(int(release.Year))
	}
	return md, nil
}

// parseDuration converts a Discogs duration string (e.g., "4:32") to milliseconds.
func parseDuration(dur string) int64 {
	if dur == "" {
		return 0
	}
	parts := strings.SplitN(dur, ":", 2)
	if len(parts) == 2 {
		minutes, err1 := strconv.ParseInt(parts[0], 10, 64)
		seconds, err2 := strconv.ParseInt(parts[1], 10, 64)
		if err1 == nil && err2 == nil {
			return (minutes*60 + seconds) * 1000
		}
	}
	return 0
}
