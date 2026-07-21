package domain

import (
	"encoding/json"
	"time"
)

// DownloadState tracks the lifecycle of a single download.
type DownloadState string

const (
	DownloadQueued        DownloadState = "queued"
	DownloadDownloading   DownloadState = "downloading"
	DownloadImportPending DownloadState = "importPending"
	DownloadImporting     DownloadState = "importing"
	DownloadImported      DownloadState = "imported"
	DownloadFailedPending DownloadState = "failedPending"
	DownloadFailed        DownloadState = "failed"
	DownloadIgnored       DownloadState = "ignored"
)

// PendingSourceName is the sentinel source value used by QueuePending
// to indicate a record awaiting source resolution before dispatch.
const PendingSourceName = "pending"

// IsPendingSource returns true if the record was created via QueuePending
// and has not yet been resolved to a real download source.
func (r *DownloadRecord) IsPendingSource() bool {
	return r.SourceName == PendingSourceName || r.Filename == ""
}

// Terminal returns true if the state is final (no more progress expected).
func (s DownloadState) Terminal() bool {
	switch s {
	case DownloadImported, DownloadFailed, DownloadIgnored:
		return true
	}
	return false
}

// IsRetryable returns true if the download can be retried from this state.
func (s DownloadState) IsRetryable() bool {
	return s == DownloadFailed
}

// ─── Event types ────────────────────────────────────────────────────

// DownloadEventType classifies a download lifecycle event.
type DownloadEventType string

const (
	EventQueued         DownloadEventType = "queued"
	EventProgress       DownloadEventType = "progress"
	EventCompleted      DownloadEventType = "completed"
	EventFailed         DownloadEventType = "failed"
	EventImportStarted  DownloadEventType = "importStarted"
	EventImportCompleted DownloadEventType = "importCompleted"
	EventRetry          DownloadEventType = "retry"
)

// DownloadEvent represents a discrete event in a download's lifecycle.
type DownloadEvent struct {
	ID         string            `json:"id"`
	DownloadID string            `json:"download_id"`
	Type       DownloadEventType `json:"type"`
	Payload    json.RawMessage   `json:"payload,omitempty"`
	Timestamp  time.Time         `json:"timestamp"`
}

// DownloadRecord holds the live state of a single download task.
type DownloadRecord struct {
	ID          string        `json:"id"`
	SourceName  string        `json:"source_name"`
	Filename    string        `json:"filename"`
	DisplayName string        `json:"display_name"`
	State       DownloadState `json:"state"`
	Progress    float64       `json:"progress"` // 0.0 — 100.0
	Size        int64         `json:"size"`     // bytes
	Transferred int64         `json:"transferred"`
	Speed       int64         `json:"speed"` // bytes/sec
	FilePath    string        `json:"file_path,omitempty"`
	Error       string        `json:"error,omitempty"`
	Username    string        `json:"username,omitempty"`  // source-specific username for download
	TrackID     string        `json:"track_id,omitempty"` // source-specific ID
	CoverURL    string        `json:"cover_url,omitempty"` // album cover image URL
	PlaylistID  string        `json:"playlist_id,omitempty"`  // playlist this download belongs to
	LibraryTrackID int64      `json:"library_track_id,omitempty"` // imported library track ID

	// Track metadata for post-download organization.
	Artist      string `json:"artist,omitempty"`
	Album       string `json:"album,omitempty"`
	Title       string `json:"title,omitempty"`
	TrackNumber int    `json:"track_number,omitempty"`
	DiscNumber  int    `json:"disc_number,omitempty"`
	Year        int    `json:"year,omitempty"`
	ISRC        string `json:"isrc,omitempty"`
}
