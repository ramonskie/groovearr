// Package playlist provides playlist import from music streaming sources.
package playlist

import (
	"context"
)

// Source is the interface for playlist sources (Deezer, Spotify, Tidal, etc.).
// Separate from download.Plugin — playlist import is content discovery, not download.
type Source interface {
	// Name returns the canonical source name (e.g. "deezer", "spotify").
	Name() string

	// DisplayName returns a human-readable label (e.g. "Deezer", "Spotify").
	DisplayName() string

	// IsConfigured returns true if this source has valid credentials.
	IsConfigured() bool

	// GetUserPlaylists returns the authenticated user's playlists.
	GetUserPlaylists(ctx context.Context) ([]PlaylistInfo, error)

	// GetPlaylistTracks returns all tracks for a playlist. Also returns the playlist name.
	GetPlaylistTracks(ctx context.Context, sourcePlaylistID string) ([]TrackInfo, string, error)
}

// PlaylistInfo is a summary of a playlist from a source, used for listing.
type PlaylistInfo struct {
	SourceID    string
	Name        string
	Description string
	TrackCount  int
	CoverURL    string
	OwnerName   string
}

// TrackInfo holds track metadata from a playlist source.
type TrackInfo struct {
	SourceTrackID string
	Title         string
	Artist        string
	Album         string
	DurationMs    int64
	ISRC          string
}

// SourcePlaylistItem is a playlist from a source, enriched with import status.
type SourcePlaylistItem struct {
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	TrackCount  int    `json:"track_count"`
	CoverURL    string `json:"cover_url,omitempty"`
	OwnerName   string `json:"owner_name,omitempty"`
	Imported    bool   `json:"imported"` // already in our DB?
}
