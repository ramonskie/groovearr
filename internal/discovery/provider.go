// Package discovery defines the metadata-first browse/search interface.
// Discovery providers let users find albums/tracks by artist, album name,
// or browse artist discographies — all with clean, authoritative metadata.
//
// This is separate from download.Plugin (which provides FILES) and
// metadata.Provider (which enriches ALREADY-IMPORTED tracks).
package discovery

import (
	"context"

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

	// SearchAlbums searches for albums by name.
	SearchAlbums(ctx context.Context, query string, limit int) ([]AlbumResult, error)
}

// ArtistSummary is a lightweight artist result from search.
type ArtistSummary struct {
	ProviderID string   `json:"provider_id"` // provider-specific ID (e.g. spotify:artist:xxx)
	Name       string   `json:"name"`
	ImageURL   string   `json:"image_url,omitempty"`
	Genres     []string `json:"genres,omitempty"`
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
