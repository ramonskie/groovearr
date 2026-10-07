package domain

import "time"

// Track represents a music track in the library.
type Track struct {
	ID               int64     `json:"id"`
	AlbumID          int64     `json:"album_id"`
	ArtistID         int64     `json:"artist_id"`
	Title            string    `json:"title"`
	TrackNumber      int       `json:"track_number,omitempty"`
	DiscNumber       int       `json:"disc_number,omitempty"`
	Duration         int64     `json:"duration"` // milliseconds
	FilePath         string    `json:"file_path,omitempty"`
	Bitrate          int       `json:"bitrate,omitempty"`            // kbps
	QualityProfileID *int64    `json:"quality_profile_id,omitempty"` // NULL = app-wide default
	FileSize         int64     `json:"file_size,omitempty"`          // bytes
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	// External service IDs (keyed by source name, e.g., "spotify", "musicbrainz").
	ExternalIDs map[string]string `json:"external_ids,omitempty"`
	AcoustID    string            `json:"acoustid,omitempty"`
	ISRC        string            `json:"isrc,omitempty"`

	// AddedByUserID/AddedByUsername record who requested the download that
	// imported this track. DB-only attribution — never written to audio tags.
	// 0/"" means scanned or system-imported. Set on INSERT only; upserts never
	// clobber it.
	AddedByUserID   int64  `json:"added_by_user_id,omitempty"`
	AddedByUsername string `json:"added_by_username,omitempty"`
}
