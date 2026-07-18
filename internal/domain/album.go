package domain

import "time"

// AlbumType classifies an album's release format.
type AlbumType string

const (
	AlbumTypeAlbum       AlbumType = "album"
	AlbumTypeSingle      AlbumType = "single"
	AlbumTypeEP           AlbumType = "ep"
	AlbumTypeCompilation AlbumType = "compilation"
	AlbumTypeLive        AlbumType = "live"
)

// Album represents a music album in the library.
type Album struct {
	ID         int64     `json:"id"`
	ArtistID   int64     `json:"artist_id"`
	Title      string    `json:"title"`
	Year       int       `json:"year,omitempty"`
	Genres     []string  `json:"genres,omitempty"`
	TrackCount int       `json:"track_count"`
	Duration   int64     `json:"duration"` // milliseconds
	ThumbURL   string    `json:"thumb_url,omitempty"`
	AlbumType  AlbumType `json:"album_type,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	// External service IDs.
	SpotifyID     string `json:"spotify_id,omitempty"`
	ITunesID      string `json:"itunes_id,omitempty"`
	DeezerID      string `json:"deezer_id,omitempty"`
	MusicBrainzID string `json:"musicbrainz_id,omitempty"`
	TidalID       string `json:"tidal_id,omitempty"`
	QobuzID       string `json:"qobuz_id,omitempty"`

	// Release date from external source.
	ReleaseDate string `json:"release_date,omitempty"`
}
