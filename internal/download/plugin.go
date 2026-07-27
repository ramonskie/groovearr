// Package download defines the download-specific plugin contract and orchestrator.
// Base plugin interfaces live in internal/plugin; this package extends them
// with download-specific methods (Search, progress tracking, etc.).
package download

import (
	"context"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// Plugin extends plugin.BasePlugin with download-specific methods.
// Every download plugin must implement this. Sources include Soulseek, Deezer,
// Tidal, Qobuz, YouTube, etc.
//
// Download lifecycle methods (Start, status, cancel, progress) have been moved
// to MonitoredProvider. Plugin retains only search — the capabilities shared
// by all download sources including metadata-only ones like Spotify.
type Plugin interface {
	plugin.BasePlugin

	// Search queries the source and returns matching tracks and albums.
	Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error)
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
