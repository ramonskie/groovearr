// Package sqlite implements the tracking.Store interface using SQLite.
//
// It does not own a database: the store wraps a *sql.DB shared with the
// library store (see library/sqlite.Store.DB) so the tracking tables created
// by the library schema migrate() live on the same connection and honour the
// same foreign_keys=on / WAL settings — mirroring how quality and download
// stores are wired.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/tracking"
)

// Store implements tracking.Store backed by a shared SQLite connection.
type Store struct {
	db *sql.DB
}

// NewSQLiteStore creates a tracking store backed by an existing *sql.DB. The
// caller owns the connection; the store never opens or closes its own.
func NewSQLiteStore(db *sql.DB) *Store {
	// The shared library connection requests foreign keys via a DSN query
	// parameter, but the modernc SQLite driver only honours _pragma=... — so
	// re-assert enforcement here, otherwise tracked_albums' ON DELETE CASCADE
	// never fires. library/sqlite.New pins MaxOpenConns(1), so this applies to
	// the single pooled connection the store is handed.
	// Best-effort: if the connection is already gone the store still reads and
	// writes fine; only cascade-dependent deletes are affected.
	_, _ = db.Exec("PRAGMA foreign_keys = ON")
	return &Store{db: db}
}

// Compile-time guarantee that *Store satisfies the full tracking.Store
// contract — a missing method fails the build here rather than at wiring time.
var _ tracking.Store = (*Store)(nil)

// timeLayout is the format produced by SQLite's datetime('now') default, used
// when a row was inserted without an explicit timestamp. Reads tolerate both
// this and RFC3339 so hand-written rows and store-written rows both parse.
const timeLayout = "2006-01-02 15:04:05"

// scanner is the common surface of *sql.Row and *sql.Rows, letting one scan
// helper serve both the single-row and list queries.
type scanner interface {
	Scan(dest ...any) error
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse(timeLayout, s); err == nil {
		return t
	}
	return time.Time{}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ─── Tracked artists ─────────────────────────────────────────────────

const artistColumns = `id, name, provider_name, provider_artist_id, monitored,
	monitor_mode, library_artist_id, auto_refresh, last_refreshed_at,
	created_at, updated_at`

const artistSelect = `SELECT ` + artistColumns + ` FROM tracked_artists`

// CreateTrackedArtist inserts a tracked artist, or refreshes the existing row
// with the same (provider_name, provider_artist_id) pair. On conflict only the
// provider-supplied name and updated_at are refreshed: monitor settings,
// library linkage, and created_at are user-owned and must survive a re-sync.
func (s *Store) CreateTrackedArtist(ctx context.Context, a *domain.TrackedArtist) (int64, error) {
	if a == nil {
		return 0, fmt.Errorf("create tracked artist: nil artist")
	}
	mode := a.MonitorMode
	if mode == "" {
		mode = domain.MonitorModeAll
	}
	now := nowRFC3339()

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tracked_artists (name, provider_name, provider_artist_id,
			monitored, monitor_mode, library_artist_id, auto_refresh,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_name, provider_artist_id) DO UPDATE SET
			name = excluded.name,
			updated_at = excluded.updated_at`,
		a.Name, a.ProviderName, a.ProviderArtistID,
		boolToInt(a.Monitored), mode, a.LibraryArtistID, boolToInt(a.AutoRefresh),
		now, now,
	); err != nil {
		return 0, fmt.Errorf("create tracked artist %q: %w", a.ProviderArtistID, err)
	}

	id, err := s.artistIDByProvider(ctx, a.ProviderName, a.ProviderArtistID)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) artistIDByProvider(ctx context.Context, providerName, providerArtistID string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM tracked_artists WHERE provider_name = ? AND provider_artist_id = ?`,
		providerName, providerArtistID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("resolve tracked artist %q: %w", providerArtistID, err)
	}
	return id, nil
}

// GetTrackedArtist returns the artist with the given ID, or (nil, nil).
func (s *Store) GetTrackedArtist(ctx context.Context, id int64) (*domain.TrackedArtist, error) {
	row := s.db.QueryRowContext(ctx, artistSelect+` WHERE id = ?`, id)
	a, err := scanArtist(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tracked artist %d: %w", id, err)
	}
	return a, nil
}

// GetTrackedArtistByProvider returns the artist for the provider pair, or
// (nil, nil) when none is tracked.
func (s *Store) GetTrackedArtistByProvider(ctx context.Context, providerName, providerArtistID string) (*domain.TrackedArtist, error) {
	row := s.db.QueryRowContext(ctx,
		artistSelect+` WHERE provider_name = ? AND provider_artist_id = ?`,
		providerName, providerArtistID)
	a, err := scanArtist(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tracked artist %s/%s: %w", providerName, providerArtistID, err)
	}
	return a, nil
}

// ListTrackedArtists returns every tracked artist ordered by name.
func (s *Store) ListTrackedArtists(ctx context.Context) ([]domain.TrackedArtist, error) {
	rows, err := s.db.QueryContext(ctx, artistSelect+` ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list tracked artists: %w", err)
	}
	defer rows.Close()

	var out []domain.TrackedArtist
	for rows.Next() {
		a, err := scanArtist(rows)
		if err != nil {
			return nil, fmt.Errorf("list tracked artists: %w", err)
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tracked artists: %w", err)
	}
	return out, nil
}

// UpdateArtistMonitor sets the monitored flag and monitor mode, refreshing
// updated_at.
func (s *Store) UpdateArtistMonitor(ctx context.Context, id int64, monitored bool, mode domain.MonitorMode) error {
	if mode == "" {
		mode = domain.MonitorModeAll
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE tracked_artists
		SET monitored = ?, monitor_mode = ?, updated_at = ?
		WHERE id = ?`,
		boolToInt(monitored), mode, nowRFC3339(), id,
	); err != nil {
		return fmt.Errorf("update artist monitor %d: %w", id, err)
	}
	return nil
}

// UpdateArtistLibraryLink persists the local library artist mapped to a tracked
// artist, refreshing updated_at so the linkage change is visible on re-read.
func (s *Store) UpdateArtistLibraryLink(ctx context.Context, artistID int64, libraryArtistID int64) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE tracked_artists
		SET library_artist_id = ?, updated_at = ?
		WHERE id = ?`,
		libraryArtistID, nowRFC3339(), artistID,
	); err != nil {
		return fmt.Errorf("update artist library link %d: %w", artistID, err)
	}
	return nil
}

// DeleteTrackedArtist removes the artist row. Discovered albums are removed by
// the tracked_albums ON DELETE CASCADE constraint (foreign_keys=on).
func (s *Store) DeleteTrackedArtist(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tracked_artists WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete tracked artist %d: %w", id, err)
	}
	return nil
}

// ─── Tracked albums ──────────────────────────────────────────────────

const albumColumns = `id, tracked_artist_id, provider_album_id, provider_name,
	title, year, album_type, monitored, status, library_album_id,
	first_seen_at, last_seen_at, last_searched_at`

const albumSelect = `SELECT ` + albumColumns + ` FROM tracked_albums`

// UpsertTrackedAlbum inserts or refreshes the album keyed on
// (tracked_artist_id, provider_album_id). Provider-owned metadata (title,
// year, type) and last_seen_at are refreshed on every upsert. The lifecycle
// state is forward-only: status advances to downloaded when the incoming row
// is downloaded, but a stored downloaded or ignored never regresses, and
// monitored is left untouched so an explicit per-album toggle survives.
// first_seen_at is preserved. A non-nil incoming LibraryAlbumID is persisted,
// and a nil incoming link never clears an existing one (COALESCE) — a
// transient match miss must not drop a good library link.
func (s *Store) UpsertTrackedAlbum(ctx context.Context, a *domain.TrackedAlbum) (int64, error) {
	if a == nil {
		return 0, fmt.Errorf("upsert tracked album: nil album")
	}
	albumType := a.AlbumType
	if albumType == "" {
		albumType = string(domain.AlbumTypeAlbum)
	}
	status := a.Status
	if status == "" {
		status = domain.AlbumStatusWanted
	}
	now := nowRFC3339()

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tracked_albums (tracked_artist_id, provider_album_id,
			provider_name, title, year, album_type, monitored, status,
			library_album_id, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tracked_artist_id, provider_album_id) DO UPDATE SET
			provider_name = excluded.provider_name,
			title = excluded.title,
			year = excluded.year,
			album_type = excluded.album_type,
			status = CASE
				WHEN tracked_albums.status IN ('downloaded', 'ignored') THEN tracked_albums.status
				WHEN excluded.status = 'downloaded' THEN 'downloaded'
				ELSE tracked_albums.status
			END,
			library_album_id = COALESCE(excluded.library_album_id, tracked_albums.library_album_id),
			last_seen_at = excluded.last_seen_at`,
		a.TrackedArtistID, a.ProviderAlbumID, a.ProviderName, a.Title,
		a.Year, albumType, boolToInt(a.Monitored), status,
		a.LibraryAlbumID, now, now,
	); err != nil {
		return 0, fmt.Errorf("upsert tracked album %q: %w", a.ProviderAlbumID, err)
	}

	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM tracked_albums WHERE tracked_artist_id = ? AND provider_album_id = ?`,
		a.TrackedArtistID, a.ProviderAlbumID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("resolve tracked album %q: %w", a.ProviderAlbumID, err)
	}
	return id, nil
}

// GetTrackedAlbumByProvider returns the album for the tracked artist and
// provider album ID, or (nil, nil).
func (s *Store) GetTrackedAlbumByProvider(ctx context.Context, artistID int64, providerAlbumID string) (*domain.TrackedAlbum, error) {
	row := s.db.QueryRowContext(ctx,
		albumSelect+` WHERE tracked_artist_id = ? AND provider_album_id = ?`,
		artistID, providerAlbumID)
	a, err := scanAlbum(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tracked album %d/%s: %w", artistID, providerAlbumID, err)
	}
	return a, nil
}

// ListTrackedAlbums returns every album discovered for the tracked artist.
func (s *Store) ListTrackedAlbums(ctx context.Context, artistID int64) ([]domain.TrackedAlbum, error) {
	rows, err := s.db.QueryContext(ctx,
		albumSelect+` WHERE tracked_artist_id = ? ORDER BY year, title, id`, artistID)
	if err != nil {
		return nil, fmt.Errorf("list tracked albums for artist %d: %w", artistID, err)
	}
	defer rows.Close()
	return scanAlbums(rows, "list tracked albums for artist")
}

// ListWantedAlbums returns monitored albums across all artists whose status is
// wanted or downloading — the acquisition worklist.
func (s *Store) ListWantedAlbums(ctx context.Context) ([]domain.TrackedAlbum, error) {
	rows, err := s.db.QueryContext(ctx, albumSelect+
		` WHERE monitored = 1 AND status IN ('wanted', 'downloading') ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list wanted albums: %w", err)
	}
	defer rows.Close()
	return scanAlbums(rows, "list wanted albums")
}

// UpdateAlbumMonitor sets the monitored flag for a single album. tracked_albums
// has no updated_at column, so only the target column changes.
func (s *Store) UpdateAlbumMonitor(ctx context.Context, albumID int64, monitored bool) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tracked_albums SET monitored = ? WHERE id = ?`,
		boolToInt(monitored), albumID,
	); err != nil {
		return fmt.Errorf("update album monitor %d: %w", albumID, err)
	}
	return nil
}

// MarkAlbumStatus advances an album's acquisition lifecycle status.
func (s *Store) MarkAlbumStatus(ctx context.Context, albumID int64, status domain.AlbumStatus) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tracked_albums SET status = ? WHERE id = ?`,
		status, albumID,
	); err != nil {
		return fmt.Errorf("mark album %d status %q: %w", albumID, status, err)
	}
	return nil
}

// MarkAlbumSearched records that a SearchMissing pass processed the album by
// stamping last_searched_at with the current UTC time.
func (s *Store) MarkAlbumSearched(ctx context.Context, albumID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tracked_albums SET last_searched_at = ? WHERE id = ?`,
		nowRFC3339(), albumID,
	); err != nil {
		return fmt.Errorf("mark album %d searched: %w", albumID, err)
	}
	return nil
}

// GetTrackedAlbum returns the album with the given internal ID, or (nil, nil).
func (s *Store) GetTrackedAlbum(ctx context.Context, albumID int64) (*domain.TrackedAlbum, error) {
	row := s.db.QueryRowContext(ctx, albumSelect+` WHERE id = ?`, albumID)
	a, err := scanAlbum(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tracked album %d: %w", albumID, err)
	}
	return a, nil
}

// TouchArtistRefreshed records that the artist's discography was just
// reconciled.
func (s *Store) TouchArtistRefreshed(ctx context.Context, artistID int64) error {
	now := nowRFC3339()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tracked_artists SET last_refreshed_at = ?, updated_at = ? WHERE id = ?`,
		now, now, artistID,
	); err != nil {
		return fmt.Errorf("touch artist refreshed %d: %w", artistID, err)
	}
	return nil
}

// ─── Scan helpers ────────────────────────────────────────────────────

func scanArtist(sc scanner) (*domain.TrackedArtist, error) {
	var a domain.TrackedArtist
	var monitored, autoRefresh int
	var libraryArtistID sql.NullInt64
	var lastRefreshed sql.NullString
	var createdAt, updatedAt string

	if err := sc.Scan(&a.ID, &a.Name, &a.ProviderName, &a.ProviderArtistID,
		&monitored, &a.MonitorMode, &libraryArtistID, &autoRefresh,
		&lastRefreshed, &createdAt, &updatedAt); err != nil {
		return nil, err
	}

	a.Monitored = monitored != 0
	a.AutoRefresh = autoRefresh != 0
	if a.MonitorMode == "" {
		a.MonitorMode = domain.MonitorModeAll
	}
	if libraryArtistID.Valid {
		v := libraryArtistID.Int64
		a.LibraryArtistID = &v
	}
	if lastRefreshed.Valid {
		if t := parseTime(lastRefreshed.String); !t.IsZero() {
			a.LastRefreshedAt = &t
		}
	}
	a.CreatedAt = parseTime(createdAt)
	a.UpdatedAt = parseTime(updatedAt)
	return &a, nil
}

func scanAlbum(sc scanner) (*domain.TrackedAlbum, error) {
	var a domain.TrackedAlbum
	var year, monitored, libraryAlbumID sql.NullInt64
	var albumType sql.NullString
	var firstSeen, lastSeen string
	var lastSearched sql.NullString

	if err := sc.Scan(&a.ID, &a.TrackedArtistID, &a.ProviderAlbumID,
		&a.ProviderName, &a.Title, &year, &albumType, &monitored, &a.Status,
		&libraryAlbumID, &firstSeen, &lastSeen, &lastSearched); err != nil {
		return nil, err
	}

	if year.Valid {
		a.Year = int(year.Int64)
	}
	a.AlbumType = albumType.String
	a.Monitored = monitored.Int64 != 0
	if libraryAlbumID.Valid {
		v := libraryAlbumID.Int64
		a.LibraryAlbumID = &v
	}
	a.FirstSeenAt = parseTime(firstSeen)
	a.LastSeenAt = parseTime(lastSeen)
	if lastSearched.Valid {
		if t := parseTime(lastSearched.String); !t.IsZero() {
			a.LastSearchedAt = &t
		}
	}
	return &a, nil
}

func scanAlbums(rows *sql.Rows, op string) ([]domain.TrackedAlbum, error) {
	var out []domain.TrackedAlbum
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op, err)
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}
