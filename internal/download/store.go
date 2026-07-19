// Package download provides download management and orchestration.
package download

import (
	"context"

	"github.com/ramonskie/groovearr/internal/domain"
)

// DownloadStore defines the persistence interface for download records and events.
type DownloadStore interface {
	// Insert creates a new download record with initial state "queued".
	Insert(ctx context.Context, record *domain.DownloadRecord) error

	// Update atomically modifies mutable fields: state, progress, size,
	// transferred, speed, file_path, and error.
	Update(ctx context.Context, record *domain.DownloadRecord) error

	// TransitionState atomically changes the download's state only if it
	// currently matches oldState. Returns false if the state did not match
	// (e.g., was cancelled by a concurrent call).
	TransitionState(ctx context.Context, id string, oldState, newState domain.DownloadState) (bool, error)

	// Get returns a single download record by ID, or nil if not found.
	Get(ctx context.Context, id string) (*domain.DownloadRecord, error)

	// List returns all download records ordered by created_at DESC.
	List(ctx context.Context) ([]domain.DownloadRecord, error)

	// ListByState returns downloads filtered by a single state.
	ListByState(ctx context.Context, state domain.DownloadState) ([]domain.DownloadRecord, error)

	// ListActive returns all non-terminal downloads (state not imported, failed, or ignored).
	ListActive(ctx context.Context) ([]domain.DownloadRecord, error)

	// ListByPlaylist returns downloads filtered by playlist_id.
	ListByPlaylist(ctx context.Context, playlistID string) ([]domain.DownloadRecord, error)

	// RecordEvent inserts a new event into the download_events table.
	RecordEvent(ctx context.Context, event *domain.DownloadEvent) error

	// GetEvents returns all events for a download ordered by created_at.
	GetEvents(ctx context.Context, downloadID string) ([]domain.DownloadEvent, error)

	// DeleteTerminal removes all download records in terminal states
	// (imported, failed, ignored) and their associated events (via CASCADE).
	DeleteTerminal(ctx context.Context) error

	// Close releases any resources held by the store.
	Close() error
}
