// Package download provides download management and orchestration types.
// These were previously in internal/domain/download.go. Moving them here
// keeps download lifecycle types close to download behavior and follows
// Go conventions for package locality.
package download

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
)

// State tracks the lifecycle of a single download.
type State string

const (
	StateQueued        State = "queued"
	StateDownloading   State = "downloading"
	StateImportPending State = "importPending"
	StateImporting     State = "importing"
	StateImported      State = "imported"
	StateFailedPending State = "failedPending"
	StateFailed        State = "failed"
	StateIgnored       State = "ignored"
)

// MaxRetries caps the number of automatic retry attempts for both
// search resolution (failedPending) and download execution (failed) states.
const MaxRetries = 5

// PendingSource is the sentinel source value used by QueuePending
// to indicate a record awaiting source resolution before dispatch.
const PendingSource = "pending"

// Terminal returns true if the state is final (no more progress expected).
func (s State) Terminal() bool {
	switch s {
	case StateImported, StateFailed, StateIgnored:
		return true
	}
	return false
}

// IsRetryable returns true if the download can be retried from this state.
func (s State) IsRetryable() bool {
	return s == StateFailed || s == StateFailedPending
}

// ─── Event types ────────────────────────────────────────────────────

// EventType classifies a download lifecycle event.
type EventType string

const (
	EventQueued          EventType = "queued"
	EventProgress        EventType = "progress"
	EventCompleted       EventType = "completed"
	EventFailed          EventType = "failed"
	EventImportStarted   EventType = "importStarted"
	EventImportCompleted EventType = "importCompleted"
	EventRetry           EventType = "retry"
)

// Event represents a discrete event in a download's lifecycle.
type Event struct {
	ID         string          `json:"id"`
	DownloadID string          `json:"download_id"`
	Type       EventType       `json:"type"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Timestamp  time.Time       `json:"timestamp"`
}

// Record holds the live state of a single download task.
type Record struct {
	ID               string  `json:"id"`
	SourceName       string  `json:"source_name"`
	Filename         string  `json:"filename"`
	DisplayName      string  `json:"display_name"`
	State            State   `json:"state"`
	Progress         float64 `json:"progress"` // 0.0 — 100.0
	Size             int64   `json:"size"`     // bytes
	Transferred      int64   `json:"transferred"`
	Speed            int64   `json:"speed"` // bytes/sec
	FilePath         string  `json:"file_path,omitempty"`
	Error            string  `json:"error,omitempty"`
	Username         string  `json:"username,omitempty"`           // source-specific username for download
	TrackID          string  `json:"track_id,omitempty"`           // source-specific ID
	CoverURL         string  `json:"cover_url,omitempty"`          // album cover image URL
	PlaylistID       string  `json:"playlist_id,omitempty"`        // playlist this download belongs to
	LibraryTrackID   int64   `json:"library_track_id,omitempty"`   // imported library track ID
	QualityProfileID *int64  `json:"quality_profile_id,omitempty"` // profile applied at download time
	RetryCount       int     `json:"retry_count,omitempty"`        // number of resolution retries
	RetryAfter       string  `json:"retry_after,omitempty"`        // RFC3339 — don't retry until after this time

	// Track metadata for post-download organization.
	Artist      string `json:"artist,omitempty"`
	Album       string `json:"album,omitempty"`
	Title       string `json:"title,omitempty"`
	TrackNumber int    `json:"track_number,omitempty"`
	DiscNumber  int    `json:"disc_number,omitempty"`
	Year        int    `json:"year,omitempty"`
	ISRC        string `json:"isrc,omitempty"`
	Bitrate     int    `json:"bitrate,omitempty"` // kbps
	Format      string `json:"format,omitempty"`  // "flac", "mp3", etc.

	// Album-level download fields. Zero-valued for track downloads.
	AlbumType        string               `json:"album_type,omitempty"`        // "Album", "Compilation"
	AlbumTracks      []domain.ExpectedTrack `json:"album_tracks,omitempty"`    // expected track listing
	DownloadClient   string               `json:"download_client,omitempty"`   // dispatch target (e.g. "qbittorrent")
	MagnetURI        string               `json:"magnet_uri,omitempty"`        // for torrent sources
	FolderPath       string               `json:"folder_path,omitempty"`       // downloaded folder path
	ImportedTrackIDs []int64              `json:"imported_track_ids,omitempty"` // linked library tracks
}

// IsPendingSource returns true if the record was created via QueuePending
// and has not yet been resolved to a real download source.
func (r *Record) IsPendingSource() bool {
	return r.SourceName == PendingSource || r.Filename == ""
}

// IsAlbum returns true if this record represents a full album download
// rather than a single track.
func (r *Record) IsAlbum() bool {
	return r.AlbumType != ""
}

// IsCompilation returns true if the album has multiple artists (VA release).
func (r *Record) IsCompilation() bool {
	if strings.EqualFold(r.AlbumType, string(domain.AlbumTypeCompilation)) {
		return true
	}
	artists := make(map[string]bool)
	for _, t := range r.AlbumTracks {
		if t.Artist != "" {
			artists[t.Artist] = true
		}
	}
	return len(artists) > 1
}

// Meta carries track metadata supplied at queue time.
type Meta struct {
	Artist      string
	Album       string
	Title       string
	TrackNumber int
	DiscNumber  int
	Year        int
	TrackID     string
	ISRC        string
	CoverURL    string
	PlaylistID  string
	Bitrate     int    // kbps
	Format      string // "flac", "mp3", etc.

	// Source-specific download parameters.
	Username string // e.g., slskd peer name, streaming source name
	Filename string // source-specific file identifier (e.g., Soulseek path)
	Size     int64  // file size in bytes
}
