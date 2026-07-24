// Package sqlite implements the download.DownloadStore interface using SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
)

// Store implements download.DownloadStore backed by a *sql.DB connection.
type Store struct {
	db  *sql.DB
	log *slog.Logger
}

// New creates a Store using an existing database connection.
// The caller must ensure the downloads and download_events tables exist
// (e.g., via library/sqlite v3 migration).
func New(db *sql.DB, logger *slog.Logger) *Store {
	return &Store{db: db, log: logger}
}

// Close is a no-op — the underlying database connection is owned by the
// caller (library/sqlite) and should not be closed here.
func (s *Store) Close() error { return nil }

// ─── CRUD ─────────────────────────────────────────────────────────────

// Insert creates a new download record. The record must have ID, SourceName,
// Filename, and DisplayName set. State is forced to "queued" regardless of
// the incoming value.
func (s *Store) Insert(ctx context.Context, r *domain.DownloadRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO downloads (
			id, source_name, filename, display_name, state, progress,
			size, transferred, speed, file_path, error,
			track_id, cover_url, artist, album, title,
			track_number, disc_number, year,
			bitrate, format,
			retry_count, retry_after, playlist_id,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.SourceName, r.Filename, r.DisplayName,
		string(domain.DownloadQueued), 0.0,
		r.Size, 0, 0, "", "",
		r.TrackID, r.CoverURL, r.Artist, r.Album, r.Title,
		r.TrackNumber, r.DiscNumber, r.Year,
		r.Bitrate, r.Format,
		0, r.RetryAfter, r.PlaylistID,
		now, now,
	)
	if err != nil {
		s.log.Error("download insert failed", "error", err, "component", "dl_store")
		return fmt.Errorf("download insert: %w", err)
	}
	return nil
}

// Update atomically modifies the mutable fields of a download record.
// The record is identified by its ID field.
func (s *Store) Update(ctx context.Context, r *domain.DownloadRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)

	result, err := s.db.ExecContext(ctx, `
		UPDATE downloads SET
			source_name=?, filename=?, display_name=?,
			state=?, progress=?, size=?, transferred=?,
			speed=?, file_path=?, error=?, cover_url=?,
			artist=?, album=?, title=?,
			track_number=?, disc_number=?, year=?,
			bitrate=?, format=?,
			retry_count=?, retry_after=?,
			updated_at=?
		WHERE id=?`,
		r.SourceName, r.Filename, r.DisplayName,
		r.State, r.Progress, r.Size, r.Transferred,
		r.Speed, r.FilePath, r.Error, r.CoverURL,
		r.Artist, r.Album, r.Title,
		r.TrackNumber, r.DiscNumber, r.Year,
		r.Bitrate, r.Format,
		r.RetryCount, r.RetryAfter,
		now, r.ID,
	)
	if err != nil {
		s.log.Error("download update failed", "error", err, "component", "dl_store")
		return fmt.Errorf("download update: %w", err)
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("download %q not found", r.ID)
	}
	return nil
}

// UpdateProgress updates only progress and state fields without overwriting
// metadata (artist, album, title, etc.). Use during polling to avoid zeroing
// out metadata set at queue time.
// cover_url is only updated when a non-empty value is passed (e.g., from a
// plugin that provides cover art). Empty strings preserve the existing value.
func (s *Store) UpdateProgress(ctx context.Context, id string, state domain.DownloadState, progress float64, size, transferred, speed int64, filePath, coverURL string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `
		UPDATE downloads SET
			state=?, progress=?, size=?, transferred=?,
			speed=?, file_path=?,
			cover_url=CASE WHEN ? = '' THEN cover_url ELSE ? END,
			updated_at=?
		WHERE id=?`,
		string(state), progress, size, transferred,
		speed, filePath,
		coverURL, coverURL,
		now, id,
	)
	if err != nil {
		s.log.Error("download update progress failed", "error", err, "component", "dl_store")
		return fmt.Errorf("download update progress: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("download %q not found", id)
	}
	return nil
}

// TransitionState atomically changes the download's state only if it
// currently matches oldState. Returns true if the transition occurred.
func (s *Store) TransitionState(ctx context.Context, id string, oldState, newState domain.DownloadState) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `
		UPDATE downloads SET state=?, updated_at=? WHERE id=? AND state=?`,
		string(newState), now, id, string(oldState),
	)
	if err != nil {
		s.log.Error("transition state failed", "error", err, "component", "dl_store")
		return false, fmt.Errorf("transition state: %w", err)
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// Get returns a single download record by ID, or nil if not found.
func (s *Store) Get(ctx context.Context, id string) (*domain.DownloadRecord, error) {
	row := s.db.QueryRowContext(ctx, downloadSelect+" WHERE id=?", id)
	r, err := s.scanDownload(row)
	if err != nil {
		s.log.Error("download get failed", "error", err, "component", "dl_store")
	}
	return r, err
}

// List returns all download records ordered by created_at DESC.
func (s *Store) List(ctx context.Context) ([]domain.DownloadRecord, error) {
	rows, err := s.db.QueryContext(ctx, downloadSelect+" ORDER BY created_at DESC, id DESC")
	if err != nil {
		s.log.Error("download list failed", "error", err, "component", "dl_store")
		return nil, fmt.Errorf("download list: %w", err)
	}
	defer rows.Close()
	return s.scanDownloads(rows)
}

// ListByState returns downloads filtered by a single state.
func (s *Store) ListByState(ctx context.Context, state domain.DownloadState) ([]domain.DownloadRecord, error) {
	rows, err := s.db.QueryContext(ctx, 		downloadSelect+" WHERE state=? ORDER BY created_at DESC, id DESC", state)
	if err != nil {
		s.log.Error("download listByState failed", "error", err, "component", "dl_store")
		return nil, fmt.Errorf("download listByState: %w", err)
	}
	defer rows.Close()
	return s.scanDownloads(rows)
}

// ListActive returns all non-terminal downloads.
func (s *Store) ListActive(ctx context.Context) ([]domain.DownloadRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		downloadSelect+" WHERE state NOT IN (?, ?, ?) ORDER BY created_at DESC, id DESC",
		domain.DownloadImported, domain.DownloadFailed, domain.DownloadIgnored,
	)
	if err != nil {
		s.log.Error("download listActive failed", "error", err, "component", "dl_store")
		return nil, fmt.Errorf("download listActive: %w", err)
	}
	defer rows.Close()
	return s.scanDownloads(rows)
}

// ListByPlaylist returns downloads filtered by playlist_id.
func (s *Store) ListByPlaylist(ctx context.Context, playlistID string) ([]domain.DownloadRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		downloadSelect+" WHERE playlist_id=? ORDER BY created_at DESC, id DESC", playlistID,
	)
	if err != nil {
		s.log.Error("download listByPlaylist failed", "error", err, "component", "dl_store")
		return nil, fmt.Errorf("download listByPlaylist: %w", err)
	}
	defer rows.Close()
	return s.scanDownloads(rows)
}

// FindActiveByTitle returns the first active download matching artist+title, or nil.
func (s *Store) FindActiveByTitle(ctx context.Context, artist, title string) (*domain.DownloadRecord, error) {
	row := s.db.QueryRowContext(ctx, 
		downloadSelect+" WHERE artist=? AND title=? AND state NOT IN (?, ?, ?) LIMIT 1",
		artist, title, domain.DownloadImported, domain.DownloadFailed, domain.DownloadIgnored,
	)
	return s.scanDownload(row)
}

// ─── Events ────────────────────────────────────────────────────────────

// RecordEvent inserts a new event into the download_events table.
func (s *Store) RecordEvent(ctx context.Context, e *domain.DownloadEvent) error {
	now := time.Now().UTC().Format(time.RFC3339)
	payload := string(e.Payload)
	if payload == "" {
		payload = "{}"
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO download_events (download_id, event_type, payload, created_at)
		VALUES (?, ?, ?, ?)`,
		e.DownloadID, e.Type, payload, now,
	)
	if err != nil {
		s.log.Error("event record failed", "error", err, "component", "dl_store")
		return fmt.Errorf("event record: %w", err)
	}

	id, _ := result.LastInsertId()
	e.ID = fmt.Sprintf("%d", id)
	e.Timestamp, _ = time.Parse(time.RFC3339, now)
	return nil
}

// GetEvents returns all events for a download ordered by created_at.
func (s *Store) GetEvents(ctx context.Context, downloadID string) ([]domain.DownloadEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, download_id, event_type, payload, created_at
		FROM download_events
		WHERE download_id=?
		ORDER BY created_at`,
		downloadID,
	)
	if err != nil {
		s.log.Error("events get failed", "error", err, "component", "dl_store")
		return nil, fmt.Errorf("events get: %w", err)
	}
	defer rows.Close()

	var events []domain.DownloadEvent
	for rows.Next() {
		var e domain.DownloadEvent
		var id int64
		var payload, createdAt string
		if err := rows.Scan(&id, &e.DownloadID, &e.Type, &payload, &createdAt); err != nil {
			s.log.Error("events scan failed", "error", err, "component", "dl_store")
			return nil, fmt.Errorf("events scan: %w", err)
		}
		e.ID = fmt.Sprintf("%d", id)
		e.Payload = json.RawMessage(payload)
		e.Timestamp, _ = time.Parse(time.RFC3339, createdAt)
		events = append(events, e)
	}
	return events, rows.Err()
}

// ─── Cleanup ────────────────────────────────────────────────────────────

// DeleteTerminal removes all download records in terminal states
// (imported, failed, ignored). Events are removed via CASCADE.
func (s *Store) DeleteTerminal(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM downloads WHERE state IN (?, ?, ?)`,
		domain.DownloadImported, domain.DownloadFailed, domain.DownloadIgnored,
	)
	if err != nil {
		s.log.Error("deleteTerminal failed", "error", err, "component", "dl_store")
		return fmt.Errorf("deleteTerminal: %w", err)
	}
	return nil
}

// ─── Scan helpers ──────────────────────────────────────────────────────

const downloadSelect = `SELECT
	id, source_name, filename, display_name, state,
	progress, size, transferred, speed, file_path, error,
	track_id, cover_url,
	artist, album, title, track_number, disc_number, year,
	bitrate, format,
	retry_count, retry_after, playlist_id,
	created_at, updated_at
	FROM downloads`

func (s *Store) scanDownload(row *sql.Row) (*domain.DownloadRecord, error) {
	var r domain.DownloadRecord
	var stateStr string
	var playlistID, retryAfter, createdAt, updatedAt string
	err := row.Scan(
		&r.ID, &r.SourceName, &r.Filename, &r.DisplayName, &stateStr,
		&r.Progress, &r.Size, &r.Transferred, &r.Speed, &r.FilePath, &r.Error,
		&r.TrackID, &r.CoverURL,
		&r.Artist, &r.Album, &r.Title, &r.TrackNumber, &r.DiscNumber, &r.Year,
		&r.Bitrate, &r.Format,
		&r.RetryCount, &retryAfter, &playlistID,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		s.log.Error("download scan failed", "error", err, "component", "dl_store")
		return nil, fmt.Errorf("download scan: %w", err)
	}

	r.PlaylistID = playlistID
	r.RetryAfter = retryAfter
	r.State = domain.DownloadState(stateStr)
	return &r, nil
}

func (s *Store) scanDownloads(rows *sql.Rows) ([]domain.DownloadRecord, error) {
	var out []domain.DownloadRecord
	for rows.Next() {
		var r domain.DownloadRecord
		var stateStr string
		var playlistID, retryAfter, createdAt, updatedAt string
		if err := rows.Scan(
			&r.ID, &r.SourceName, &r.Filename, &r.DisplayName, &stateStr,
			&r.Progress, &r.Size, &r.Transferred, &r.Speed, &r.FilePath, &r.Error,
			&r.TrackID, &r.CoverURL,
			&r.Artist, &r.Album, &r.Title, &r.TrackNumber, &r.DiscNumber, &r.Year,
			&r.Bitrate, &r.Format,
			&r.RetryCount, &retryAfter, &playlistID,
			&createdAt, &updatedAt,
		); err != nil {
			s.log.Error("downloads scan failed", "error", err, "component", "dl_store")
			return nil, fmt.Errorf("downloads scan: %w", err)
		}
		r.PlaylistID = playlistID
		r.RetryAfter = retryAfter
		r.State = domain.DownloadState(stateStr)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("downloads iteration failed", "error", err, "component", "dl_store")
		return out, err
	}
	return out, nil
}

// Ensure interface compliance at compile time.
var _ download.DownloadStore = (*Store)(nil)
