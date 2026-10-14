// Package discovery defines the metadata-first browse/search interface.
// Discovery providers let users find albums/tracks by artist, album name,
// or browse artist discographies — all with clean, authoritative metadata.
//
// This is separate from download.Plugin (which provides FILES) and
// metadata.Provider (which enriches ALREADY-IMPORTED tracks).
package discovery

import (
	"context"
	"strings"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// Provider extends plugin.BasePlugin with metadata-first browse methods.
// Providers register with capability "discovery" via the plugin system.
type Provider interface {
	plugin.BasePlugin

	// SearchArtists searches for artists by name.
	SearchArtists(ctx context.Context, query string, limit int) ([]ArtistSummary, error)

	// GetArtistAlbums returns an artist's albums.
	GetArtistAlbums(ctx context.Context, providerArtistID string, limit int) ([]AlbumResult, error)

	// GetAlbumTracks returns all tracks on an album.
	GetAlbumTracks(ctx context.Context, providerAlbumID string) ([]TrackInfo, error)

	// SearchAlbums searches for albums. Callers supply a structured
	// AlbumQuery — Artist+Title (the catalog path) or Text (the Discover
	// free-text path) — and render the phrase with AlbumQuery.Query() rather
	// than guessing an artist/title boundary themselves.
	SearchAlbums(ctx context.Context, q AlbumQuery, limit int) ([]AlbumResult, error)
}

// AlbumQuery is a structured album search. Callers set Artist+Title (the
// catalog carries the boundary it already has) or Text (the Discover free-text
// path). Query() renders the phrase with a defined precedence so no provider has
// to re-derive it.
type AlbumQuery struct {
	Artist string
	Title  string
	Text   string
}

// Query renders the search phrase. Structured Artist+Title wins; otherwise the
// bare Title; otherwise the free-text Text. Artist and Title are trimmed and
// whitespace-collapsed when joined. Returns "" only when every field is blank.
func (q AlbumQuery) Query() string {
	if artist := strings.TrimSpace(q.Artist); artist != "" {
		return strings.Join(strings.Fields(artist+" "+strings.TrimSpace(q.Title)), " ")
	}
	if title := strings.TrimSpace(q.Title); title != "" {
		return title
	}
	return strings.TrimSpace(q.Text)
}

// TopTrackProvider is an optional extension for discovery providers that
// can return an artist's most popular tracks. Type-assert from Provider.
type TopTrackProvider interface {
	GetArtistTopTracks(ctx context.Context, providerArtistID string, limit int) ([]TrackInfo, error)
}

// ArtistSummary is a lightweight artist result from search.
type ArtistSummary struct {
	ProviderID   string   `json:"provider_id"`
	ProviderName string   `json:"provider_name"`
	Name         string   `json:"name"`
	ImageURL     string   `json:"image_url,omitempty"`
	Genres       []string `json:"genres,omitempty"`
}

// AlbumResult is an album from search or artist discography.
type AlbumResult struct {
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	ArtistName   string `json:"artist_name"`
	Title        string `json:"title"`
	Year         int    `json:"year,omitempty"`
	CoverURL     string `json:"cover_url,omitempty"`
	TrackCount   int    `json:"track_count"`
	Type         string `json:"type"` // "album", "single", "compilation", "ep"
}

// TrackInfo is a track on an album, with full metadata.
type TrackInfo struct {
	ProviderID  string `json:"provider_id"`
	ArtistName  string `json:"artist_name"`
	AlbumTitle  string `json:"album_title"`
	Title       string `json:"title"`
	TrackNumber int    `json:"track_number"`
	DiscNumber  int    `json:"disc_number"`
	DurationMs  int64  `json:"duration_ms"`
	ISRC        string `json:"isrc,omitempty"`
}
