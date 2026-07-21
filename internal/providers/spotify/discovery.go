package spotify

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ramonskie/groovearr/internal/discovery"
)

// DiscoveryPlugin wraps the Spotify API as a discovery.Provider.
type DiscoveryPlugin struct {
	api *API
}

// NewDiscovery creates a Spotify discovery provider.
func NewDiscovery(api *API) *DiscoveryPlugin {
	return &DiscoveryPlugin{api: api}
}

// ─── plugin.BasePlugin ──────────────────────────────────────────────

func (p *DiscoveryPlugin) Name() string             { return "spotify" }
func (p *DiscoveryPlugin) DisplayName() string      { return "Spotify" }
func (p *DiscoveryPlugin) IsConfigured() bool       { return true } // API availability checked at call time
func (p *DiscoveryPlugin) Connected() bool          { return p.IsConfigured() }
func (p *DiscoveryPlugin) CheckConnection(ctx context.Context) error {
	if p.api == nil {
		return fmt.Errorf("spotify: API not initialized")
	}
	return nil
}

// ─── discovery.Provider ─────────────────────────────────────────────

func (p *DiscoveryPlugin) SearchArtists(ctx context.Context, query string, limit int) ([]discovery.ArtistSummary, error) {
	page, err := p.api.SearchArtists(ctx, query, limit, 0)
	if err != nil {
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

func (p *DiscoveryPlugin) GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]discovery.AlbumResult, error) {
	page, err := p.api.GetArtistAlbums(ctx, providerArtistID, limit, 0, "album,single,compilation")
	if err != nil {
		return nil, err
	}

	var out []discovery.AlbumResult
	for _, a := range page.Items {
		year := 0
		if len(a.ReleaseDate) >= 4 {
			year, _ = strconv.Atoi(a.ReleaseDate[:4])
		}
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

func (p *DiscoveryPlugin) GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]discovery.TrackInfo, error) {
	// Fetch album metadata for the artist name.
	album, err := p.api.GetAlbum(ctx, providerAlbumID)
	if err != nil {
		return nil, err
	}

	artistName := ""
	if len(album.Artists) > 0 {
		artistName = album.Artists[0].Name
	}

	var out []discovery.TrackInfo
	offset := 0
	for {
		page, err := p.api.GetAlbumTracks(ctx, providerAlbumID, 50, offset)
		if err != nil {
			return nil, fmt.Errorf("spotify: get album tracks: %w", err)
		}
		for _, t := range page.Items {
			out = append(out, trackToInfo(t, artistName, album.Name))
		}
		if page.Next == "" || len(page.Items) == 0 {
			break
		}
		offset += len(page.Items)
	}

	return out, nil
}

func (p *DiscoveryPlugin) SearchAlbums(ctx context.Context, query string, limit int) ([]discovery.AlbumResult, error) {
	page, err := p.api.SearchAlbums(ctx, query, limit, 0)
	if err != nil {
		return nil, err
	}

	var out []discovery.AlbumResult
	for _, a := range page.Items {
		year := 0
		if len(a.ReleaseDate) >= 4 {
			year, _ = strconv.Atoi(a.ReleaseDate[:4])
		}
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

// ─── helpers ────────────────────────────────────────────────────────

// bestImage returns the URL of the image closest to the target width.
func bestImage(images []Image, targetWidth int) string {
	if len(images) == 0 {
		return ""
	}
	best := images[0]
	bestDiff := imageDiff(best, targetWidth)
	for _, img := range images[1:] {
		diff := imageDiff(img, targetWidth)
		if diff < bestDiff {
			best = img
			bestDiff = diff
		}
	}
	return best.URL
}

func imageDiff(img Image, target int) int {
	if img.Width == nil {
		return 99999
	}
	w := *img.Width
	if w > target {
		return w - target
	}
	return target - w
}

func trackToInfo(t SimplifiedTrack, artistName, albumTitle string) discovery.TrackInfo {
	trackArtists := artistName
	if len(t.Artists) > 0 {
		trackArtists = t.Artists[0].Name
	}
	return discovery.TrackInfo{
		ProviderID:  t.ID,
		ArtistName:  trackArtists,
		AlbumTitle:  albumTitle,
		Title:       t.Name,
		TrackNumber: t.TrackNumber,
		DiscNumber:  t.DiscNumber,
		DurationMs:  int64(t.DurationMs),
	}
}
