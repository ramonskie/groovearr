package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// Client implements metadata.Provider using the MusicBrainz public API.
// It provides cover art search (via Cover Art Archive), track enrichment
// (ISRC, genres, label, release date), and artist image lookup (unavailable).
type Client struct {
	cfg       MusicBrainzConfig
	api       *apiClient
	log       *slog.Logger
	connected bool
	mu        sync.RWMutex // protects connected
}

// NewClient creates a MusicBrainz metadata provider.
func NewClient(cfg MusicBrainzConfig, logger *slog.Logger) *Client {
	return &Client{
		cfg: cfg,
		api: newAPIClient(cfg, logger),
		log: logger,
	}
}

// Compile-time interface check.
var _ metadata.Provider = (*Client)(nil)

// ─── plugin.BasePlugin ─────────────────────────────────────────────────

func (c *Client) Name() string              { return pluginName }
func (c *Client) DisplayName() string       { return displayName }
func (c *Client) IsConfigured() bool        { return true } // no credentials required
func (c *Client) IsMetadataAvailable() bool { return true }

// CapabilityStatus returns metadata status based on health check.
func (c *Client) CapabilityStatus() map[string]string {
	s := "configured"
	if c.Connected() {
		s = "connected"
	}
	return map[string]string{"metadata": s}
}

func (c *Client) CheckConnection(ctx context.Context) error {
	_, err := c.api.SearchReleaseGroup(ctx, "test", "test")
	c.mu.Lock()
	if err != nil {
		c.connected = false
		c.mu.Unlock()
		c.log.Error("musicbrainz check connection failed", "error", err, "component", "musicbrainz")
		if errors.Is(err, ErrRateLimited) {
			return fmt.Errorf("musicbrainz: rate limited: %w", err)
		}
		return fmt.Errorf("musicbrainz: connectivity check failed: %w", err)
	}
	c.connected = true
	c.mu.Unlock()
	return nil
}

func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// ─── metadata.Provider ─────────────────────────────────────────────────

// SearchCover looks up album cover art by resolving the artist+album to a
// MusicBrainz release group, then constructing a Cover Art Archive URL.
// Returns nil, nil if no matching release group is found.
func (c *Client) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	rg, err := c.api.SearchReleaseGroup(ctx, artist, album)
	if err != nil {
		c.log.Error("musicbrainz search cover failed", "error", err, "artist", artist, "album", album, "component", "musicbrainz")
		return nil, err
	}
	if rg == nil {
		return nil, nil
	}

	return &metadata.CoverResult{
		ImageURL: fmt.Sprintf("https://coverartarchive.org/release-group/%s/front", rg.MBID),
		Width:    500,
		Height:   500,
		Source:   "coverartarchive",
		ThumbURL: fmt.Sprintf("https://coverartarchive.org/release-group/%s/front-250.jpg", rg.MBID),
	}, nil
}

// SearchArtistImage looks up an artist image. MusicBrainz does not host
// artist images directly — returns nil, nil.
func (c *Client) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}

// SearchAlbum finds the album title for a track by searching MusicBrainz
// recordings via artist+title. Returns empty string if no match found.
func (c *Client) SearchAlbum(ctx context.Context, artist, title string) string {
	if artist == "" || title == "" {
		return ""
	}
	rg, err := c.api.SearchRecording(ctx, artist, title)
	if err != nil || rg == nil {
		return ""
	}
	return rg.Title
}

// EnrichTrack fetches ISRC codes, genres, label, release date, and external
// IDs from MusicBrainz for the given track.
func (c *Client) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	// If the track already has a MusicBrainz release MBID, use it directly.
	releaseMBID := track.ExternalIDs["musicbrainz_release"]
	if releaseMBID != "" {
		return c.enrichByRelease(ctx, releaseMBID)
	}

	// Otherwise, search by artist + album.
	// We need the artist name and album title — if not available, skip.
	// Track doesn't have artist/album names directly, only IDs.
	// The enrichment pipeline passes these separately, or we use track metadata.
	// For now: if we have a MusicBrainz release group ID, look it up.
	rgMBID := track.ExternalIDs["musicbrainz_release_group"]
	if rgMBID != "" {
		// Lookup the release group's first release and enrich from there.
		// For simplicity, search for releases by release group.
		return c.enrichByReleaseGroup(ctx, rgMBID)
	}

	return nil, nil
}

// ─── Internal helpers ──────────────────────────────────────────────────

func (c *Client) enrichByRelease(ctx context.Context, releaseMBID string) (*metadata.TrackMetadata, error) {
	info, err := c.api.LookupRelease(ctx, releaseMBID)
	if err != nil {
		c.log.Error("musicbrainz enrich by release failed", "error", err, "releaseMBID", releaseMBID, "component", "musicbrainz")
		return nil, err
	}
	if info == nil {
		return nil, nil
	}

	return releaseInfoToMetadata(info), nil
}

func (c *Client) enrichByReleaseGroup(ctx context.Context, rgMBID string) (*metadata.TrackMetadata, error) {
	// Release group → release resolution requires browsing the release list.
	// Placeholder for now — will be implemented when the enrichment pipeline
	// supplies artist+album names for MBID lookups.
	return nil, nil
}

func releaseInfoToMetadata(info *ReleaseInfo) *metadata.TrackMetadata {
	m := &metadata.TrackMetadata{
		ReleaseDate: info.Date,
		Label:       info.Label,
		ExternalIDs: map[string]string{
			"musicbrainz_release":       info.MBID,
			"musicbrainz_release_group": info.ReleaseGroupID,
		},
	}
	if len(info.Genres) > 0 {
		m.Genres = info.Genres
	}
	if len(info.ISRCs) > 0 {
		// ISRCs are aggregated across all tracks in the release.
		// Use the first as a best-guess — when track-number matching is available,
		// this should look up the ISRC for the specific track number.
		m.ISRC = info.ISRCs[0]
	}
	return m
}
