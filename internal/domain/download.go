package domain

// DownloadState tracks the lifecycle of a single download.
type DownloadState string

const (
	DownloadInitializing DownloadState = "Initializing"
	DownloadDownloading  DownloadState = "InProgress, Downloading"
	DownloadSucceeded    DownloadState = "Completed, Succeeded"
	DownloadErrored      DownloadState = "Errored"
	DownloadCancelled    DownloadState = "Cancelled"
	DownloadAborted      DownloadState = "Aborted"
)

// TerminalStates returns true if the state is final (no more progress expected).
func (s DownloadState) Terminal() bool {
	switch s {
	case DownloadSucceeded, DownloadErrored, DownloadCancelled, DownloadAborted:
		return true
	}
	return false
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
	TrackID     string        `json:"track_id,omitempty"` // source-specific ID
}
