// Package download defines the plugin contract and registry for download sources.
package download

import (
	"context"

	"github.com/ramonskie/groovearr/internal/domain"
)

// Plugin is the interface every download source must implement.
// Sources include Soulseek (slskd), Deezer, Tidal, Qobuz, YouTube, etc.
type Plugin interface {
	// Name returns the canonical source name (e.g. "soulseek", "deezer").
	Name() string

	// DisplayName returns a human-readable label (e.g. "Soulseek", "Deezer").
	DisplayName() string

	// IsConfigured returns true if this source has valid credentials/settings.
	IsConfigured() bool

	// CheckConnection probes the source's API for reachability.
	CheckConnection(ctx context.Context) error

	// Search queries the source and returns matching tracks and albums.
	Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error)

	// Download initiates a download. Returns a download ID for status tracking.
	// username is source-dependent: slskd peer name for Soulseek, source name for streaming.
	// filename is source-specific encoding (Soulseek file path, Deezer "track_id||display").
	Download(ctx context.Context, username, filename string, fileSize int64) (string, error)

	// GetDownloads returns the live status of all tracked downloads for this source.
	GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error)

	// GetDownloadStatus returns status for a single download by ID.
	GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error)

	// CancelDownload cancels an active download. If remove is true, also drops the record.
	CancelDownload(ctx context.Context, downloadID string, remove bool) error

	// ClearCompleted removes all terminal-state downloads from tracking.
	ClearCompleted(ctx context.Context) error

	// Connected returns true if the source has been verified (auth/tested).
	// Optional: plugins that don't implement this always show as "configured" after setup.
	Connected() bool
}

// SearchPlugin extends Plugin for sources that support progress callbacks during search.
// This is optional — sources without live-progress search can skip implementing it.
type SearchPlugin interface {
	Plugin

	// SearchWithProgress is like Search but invokes the callback as results arrive.
	SearchWithProgress(ctx context.Context, query string, cb func(tracks []domain.TrackResult, albums []domain.AlbumResult, responseCount int)) ([]domain.TrackResult, []domain.AlbumResult, error)
}

// Progress holds live download progress for a single download.
type Progress struct {
	DownloadID  string
	Transferred int64 // bytes downloaded so far
	Total       int64 // total file size in bytes
	Speed       int64 // bytes per second
}

// DownloadProgressor is an optional interface for plugins that can report
// live download progress. Workers poll GetProgress to emit progress events.
// Plugins that do not implement this will have their downloads tracked via
// periodic GetDownloadStatus calls instead.
type DownloadProgressor interface {
	Plugin

	// GetProgress returns the current progress of a download.
	// downloadID is the plugin-specific identifier returned by Plugin.Download.
	GetProgress(ctx context.Context, downloadID string) (*Progress, error)
}
