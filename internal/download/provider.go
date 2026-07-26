package download

import (
	"context"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
)

// MonitoredProvider defines a download provider that exposes fine-grained
// control and monitoring capabilities. It replaces the download-specific
// methods previously on the Plugin interface (Download, GetDownloadStatus,
// GetProgress, CancelDownload).
type MonitoredProvider interface {
	// StartDownload initiates a non-blocking download and returns a
	// provider-managed download ID for subsequent status queries.
	StartDownload(ctx context.Context, meta DownloadMeta) (string, error)

	// GetStatus returns the current state of a tracked download.
	GetStatus(ctx context.Context, providerID string) (*domain.DownloadRecord, error)

	// GetProgress returns live byte-level progress for a download.
	// Providers that do not support progress reporting must return nil, nil.
	GetProgress(ctx context.Context, providerID string) (*Progress, error)

	// Cancel cancels an active download. If remove is true, the provider
	// should also drop internal tracking of the download.
	Cancel(ctx context.Context, providerID string, remove bool) error

	// ActiveDownloads returns the provider-managed IDs of all currently
	// tracked downloads.
	ActiveDownloads() []string

	// MaxConcurrent returns the maximum number of concurrent downloads
	// allowed for this provider. Return 0 for unlimited.
	MaxConcurrent() int

	// DownloadTimeout returns the per-provider timeout duration. Downloads
	// exceeding this duration are considered stalled.
	DownloadTimeout() time.Duration
}
