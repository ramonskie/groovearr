// Package domain defines the core music library types.
package domain

import "time"

// Artist represents a music artist in the library.
type Artist struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Genres    []string  `json:"genres,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	ThumbURL  string    `json:"thumb_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// External service IDs.
	SpotifyID     string `json:"spotify_id,omitempty"`
	ITunesID      string `json:"itunes_id,omitempty"`
	DeezerID      string `json:"deezer_id,omitempty"`
	MusicBrainzID string `json:"musicbrainz_id,omitempty"`
}

// ArtistImage represents a cached artist image URL from a specific source.
type ArtistImage struct {
	URL    string `json:"url"`
	Source string `json:"source"` // spotify, deezer, itunes, etc.
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
