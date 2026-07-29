package domain

import "time"

// AlbumType classifies an album's release format.
type AlbumType string

const (
	AlbumTypeAlbum       AlbumType = "album"
	AlbumTypeSingle      AlbumType = "single"
	AlbumTypeEP          AlbumType = "ep"
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

// ExpectedTrack holds per-track metadata for album-level downloads.
// The Artist field enables correct per-artist tagging for compilations.
type ExpectedTrack struct {
	TrackNumber int    `json:"track_number"`
	Artist      string `json:"artist,omitempty"` // empty = same as album artist
	Title       string `json:"title"`
	Duration    int    `json:"duration,omitempty"` // seconds, 0 if unknown
}

// AlbumRelease represents an album-level search result from an album-capable
// source (e.g. Prowlarr/RuTracker, future Usenet). It contains the download
// URI and structured metadata needed to create an AlbumRecord.
type AlbumRelease struct {
	SourceName string          `json:"source_name"`
	Artist     string          `json:"artist"`
	Album      string          `json:"album"`
	Year       int             `json:"year,omitempty"`
	Format     string          `json:"format,omitempty"` // "flac", "mp3"
	Size       int64           `json:"size,omitempty"`   // bytes
	Seeders    int             `json:"seeders,omitempty"`
	MagnetURI  string          `json:"magnet_uri,omitempty"`
	CoverURL   string          `json:"cover_url,omitempty"`
	AlbumType  string          `json:"album_type,omitempty"` // "Album", "Compilation" (from MusicBrainz)
	MBID       string          `json:"mbid,omitempty"`       // MusicBrainz Release Group ID
	Tracks     []ExpectedTrack `json:"tracks,omitempty"`     // populated by ResolveTracks
}
