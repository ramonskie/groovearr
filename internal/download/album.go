package download

import (
	"context"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// AlbumProvider is a plugin that can search for full album releases and
// resolve their track listings. Album-capable sources (Prowlarr, future
// Usenet indexers) implement this instead of or in addition to Plugin.
type AlbumProvider interface {
	plugin.BasePlugin

	// SearchAlbum queries the source for full album releases matching query.
	SearchAlbum(ctx context.Context, query string) ([]domain.AlbumRelease, error)

	// ResolveTracks fetches the track listing for an album release.
	// Sources that don't provide track metadata should fall back to
	// MusicBrainz or return nil to defer to the caller.
	ResolveTracks(ctx context.Context, release domain.AlbumRelease) ([]domain.ExpectedTrack, error)

	// ResolveTracksForCount is like ResolveTracks but uses the actual
	// file count (known after download) and torrent title to pick the
	// best matching release. Returns the resolved MusicBrainz release MBID.
	ResolveTracksForCount(ctx context.Context, release domain.AlbumRelease, fileCount int, torrentTitle string) ([]domain.ExpectedTrack, string, error)
}

// DownloadClient handles the actual download execution for album-level
// downloads (e.g. qBittorrent, Transmission). It is separate from the
// search source so one indexer can hand off to multiple download clients.
type DownloadClient interface {
	plugin.BasePlugin

	// AddDownload starts a download and returns a provider-managed ID
	// (torrent hash, nzb ID, etc.) for subsequent status queries.
	AddDownload(ctx context.Context, uri, category, savepath string) (string, error)

	// GetStatus returns the current state of a tracked download.
	GetStatus(ctx context.Context, providerID string) (*Record, error)

	// GetProgress returns live byte-level progress for a download.
	// Returns nil, nil if progress is unavailable.
	GetProgress(ctx context.Context, providerID string) (*Progress, error)

	// Cancel cancels an active download. If remove is true, delete
	// downloaded files as well.
	Cancel(ctx context.Context, providerID string, remove bool) error

	// MaxConcurrent returns the maximum number of concurrent downloads.
	// Return 0 for unlimited.
	MaxConcurrent() int

	// DownloadTimeout returns the per-download timeout duration.
	DownloadTimeout() time.Duration
}
