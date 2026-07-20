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

	// External service IDs (keyed by source name, e.g., "spotify", "musicbrainz").
	ExternalIDs map[string]string `json:"external_ids,omitempty"`

	// Release date from external source.
	ReleaseDate string `json:"release_date,omitempty"`
}
