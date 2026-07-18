package domain

import "time"

// Track represents a music track in the library.
type Track struct {
	ID          int64     `json:"id"`
	AlbumID     int64     `json:"album_id"`
	ArtistID    int64     `json:"artist_id"`
	Title       string    `json:"title"`
	TrackNumber int       `json:"track_number,omitempty"`
	DiscNumber  int       `json:"disc_number,omitempty"`
	Duration    int64     `json:"duration"` // milliseconds
	FilePath    string    `json:"file_path,omitempty"`
	Bitrate     int       `json:"bitrate,omitempty"`  // kbps
	FileSize    int64     `json:"file_size,omitempty"` // bytes
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// External service IDs.
	SpotifyID     string `json:"spotify_id,omitempty"`
	ITunesID      string `json:"itunes_id,omitempty"`
	DeezerID      string `json:"deezer_id,omitempty"`
	MusicBrainzID string `json:"musicbrainz_id,omitempty"`
	TidalID       string `json:"tidal_id,omitempty"`
	QobuzID       string `json:"qobuz_id,omitempty"`
	AcoustID      string `json:"acoustid,omitempty"`
	ISRC          string `json:"isrc,omitempty"`
}
