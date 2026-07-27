package coverartarchive

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// Client implements metadata.Provider and metadata.CoverArtArchiveProvider
// using the Cover Art Archive public API. No credentials required.
type Client struct {
	api       *apiClient
	log       *slog.Logger
	connected bool
	mu        sync.RWMutex // protects connected
}

// NewClient creates a Cover Art Archive metadata provider.
func NewClient(log *slog.Logger) *Client {
	return &Client{
		api: newAPIClient(log),
		log: log,
	}
}

// Compile-time interface checks.
var (
	_ metadata.Provider                = (*Client)(nil)
	_ metadata.CoverArtArchiveProvider = (*Client)(nil)
)

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
	_, err := c.api.GetReleaseImages(ctx, "76df3287-6cda-33eb-8e9a-044b5e15ffdd") // Radiohead – OK Computer (known stable MBID)
	c.mu.Lock()
	if err != nil {
		c.connected = false
		c.mu.Unlock()
		if c.log != nil {
			c.log.Error("coverartarchive connectivity check failed", "error", err, "component", "caa")
		}
		return fmt.Errorf("coverartarchive: connectivity check failed: %w", err)
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

// SearchCover searches cover art by artist+album. CAA only resolves by MBID,
// so this method returns nil,nil — use SearchCoverByMBID instead.
func (c *Client) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	return nil, nil
}

// SearchArtistImage looks up an artist image. CAA is release-level only.
func (c *Client) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}

// SearchAlbum is unsupported by CAA — no recording-level data.
func (c *Client) SearchAlbum(ctx context.Context, artist, title string) string { return "" }

// EnrichTrack is unsupported by CAA — no ISRC/genre/label data.
func (c *Client) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	return nil, nil
}

// ─── metadata.CoverArtArchiveProvider ──────────────────────────────────

// SearchCoverByMBID looks up cover art by release MBID on the Cover Art Archive.
// Returns the front cover image if available, or the first image as fallback.
func (c *Client) SearchCoverByMBID(ctx context.Context, mbid string) (*metadata.CoverResult, error) {
	images, err := c.api.GetReleaseImages(ctx, mbid)
	if err != nil {
		if c.log != nil {
			c.log.Error("coverartarchive search cover failed", "error", err, "mbid", mbid, "component", "caa")
		}
		return nil, err
	}
	if images == nil || len(images.Images) == 0 {
		return nil, nil
	}

	// Prefer the front cover, fall back to first image.
	img := images.Images[0]
	for i := range images.Images {
		if images.Images[i].Front {
			img = images.Images[i]
			break
		}
	}

	result := &metadata.CoverResult{
		ImageURL: img.ImageURL,
		Source:   "coverartarchive",
	}

	// Extract thumbnail URLs.
	if thumb, ok := img.Thumbnails["500"]; ok {
		result.ThumbURL = thumb
	} else if thumb, ok := img.Thumbnails["large"]; ok {
		result.ThumbURL = thumb
	}

	return result, nil
}
