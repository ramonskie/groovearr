// Package download provides download management and orchestration.
package download

import (
	"context"
)

// Store defines the persistence interface for download records and events.
type Store interface {
	// Insert creates a new download record with initial state "queued".
	Insert(ctx context.Context, record *Record) error

	// Update atomically modifies mutable fields: state, progress, size,
	// transferred, speed, file_path, error, and metadata.
	Update(ctx context.Context, record *Record) error

	// UpdateProgress updates only progress and state fields without
	// overwriting metadata (artist, album, title, etc.).
	UpdateProgress(ctx context.Context, id string, state State, progress float64, size, transferred, speed int64, filePath, coverURL string) error

	// TransitionState atomically changes the download's state only if it
	// currently matches oldState. Returns false if the state did not match
	// (e.g., was cancelled by a concurrent call).
	TransitionState(ctx context.Context, id string, oldState, newState State) (bool, error)

	// Get returns a single download record by ID, or nil if not found.
	Get(ctx context.Context, id string) (*Record, error)

	// List returns all download records ordered by created_at DESC.
	List(ctx context.Context) ([]Record, error)

	// ListByState returns downloads filtered by a single state.
	ListByState(ctx context.Context, state State) ([]Record, error)

	// ListActive returns all non-terminal downloads (state not imported, failed, or ignored).
	ListActive(ctx context.Context) ([]Record, error)

	// ListByPlaylist returns downloads filtered by playlist_id.
	ListByPlaylist(ctx context.Context, playlistID string) ([]Record, error)

	// FindActiveByTitle returns the first active download matching artist+title, or nil.
	FindActiveByTitle(ctx context.Context, artist, title string) (*Record, error)

	// RecordEvent inserts a new event into the download_events table.
	RecordEvent(ctx context.Context, event *Event) error

	// GetEvents returns all events for a download ordered by created_at.
	GetEvents(ctx context.Context, downloadID string) ([]Event, error)

	// DeleteTerminal removes all download records in terminal states
	// (imported, failed, ignored) and their associated events (via CASCADE).
	DeleteTerminal(ctx context.Context) error

	// Close releases any resources held by the store.
	Close() error
}
