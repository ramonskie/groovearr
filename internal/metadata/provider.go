// Package metadata defines the metadata provider contract and supporting types.
// Metadata providers enrich library tracks with cover art, artist images, ISRC codes,
// genres, and other metadata from external services (Cover Art Archive, MusicBrainz,
// Last.fm, Deezer, etc.).
//
// A metadata provider implements the Provider interface (which extends plugin.BasePlugin)
// and registers via a plugin.PluginFactory with capability "metadata".
// The type-safe metadata.Registry wraps plugin.Registry for capability-based routing.
package metadata

import (
	"context"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// Provider extends plugin.BasePlugin with metadata-specific methods.
// Every metadata provider must implement this. Sources include Cover Art Archive,
// MusicBrainz, Last.fm, Deezer, iTunes, etc.
type Provider interface {
	plugin.BasePlugin

	// SearchCover looks up album cover art by artist and album name.
	// Returns nil, nil if no cover is found.
	SearchCover(ctx context.Context, artist, album string) (*CoverResult, error)

	// SearchArtistImage looks up an artist image by artist name.
	// Returns nil, nil if no image is found.
	SearchArtistImage(ctx context.Context, artist string) (*ArtistImageResult, error)

	// EnrichTrack fetches metadata (ISRC, genres, release date, etc.) for a track.
	// The returned TrackMetadata describes what was found; the caller decides
	// which fields to apply to the track.
	// Returns nil, nil if no enrichment data is available.
	EnrichTrack(ctx context.Context, track *domain.Track) (*TrackMetadata, error)
}

// CoverResult holds album cover art data from a metadata provider.
type CoverResult struct {
	ImageURL string `json:"image_url"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Source   string `json:"source"`            // provider name (e.g. "coverartarchive", "deezer")
	ThumbURL string `json:"thumb_url,omitempty"` // smaller version when available
}

// ArtistImageResult holds artist image data from a metadata provider.
type ArtistImageResult struct {
	ImageURL string `json:"image_url"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Source   string `json:"source"`            // provider name
	ThumbURL string `json:"thumb_url,omitempty"` // smaller version when available
}

// TrackMetadata holds enrichment fields that a metadata provider discovered
// for a track. All fields are optional — providers populate only what they can find.
type TrackMetadata struct {
	ISRC        string            `json:"isrc,omitempty"`
	AcoustID    string            `json:"acoustid,omitempty"`
	Genres      []string          `json:"genres,omitempty"`
	ReleaseDate string            `json:"release_date,omitempty"`
	Label       string            `json:"label,omitempty"`
	ExternalIDs map[string]string `json:"external_ids,omitempty"` // keyed by source (e.g. "musicbrainz")
}

// ArtistMetadataProvider is an optional interface for providers that can
// fetch richer artist information beyond a single image.
type ArtistMetadataProvider interface {
	Provider

	// GetArtistBio returns a short biography for the artist.
	GetArtistBio(ctx context.Context, artist string) (string, error)

	// GetSimilarArtists returns names of similar artists.
	GetSimilarArtists(ctx context.Context, artist string) ([]string, error)
}

// LyricsProvider is an optional interface for providers that can fetch
// song lyrics.
type LyricsProvider interface {
	Provider

	// GetLyrics returns lyrics for the given artist and track title.
	GetLyrics(ctx context.Context, artist, title string) (string, error)
}
