// Package sqlite implements the catalog store interfaces (materialized album
// tracklists, album/artist metadata, artist discography) backed by SQLite.
//
// The store shares the library's single *sql.DB — it never opens a second
// connection pool. Schema is additive-only (CREATE TABLE IF NOT EXISTS); the
// tables are sidecars keyed by the library ids (albums.id, artists.id). Every
// mutation uses a short per-entity transaction so the single-writer connection
// never starves readers. Times are stored as RFC3339 UTC TEXT; genres are
// stored as JSON TEXT.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
)

// The store satisfies the four materialized sub-interfaces implemented in this
// file. The full catalog.Store assertion (which additionally requires
// RefreshQueue) lives in queue.go alongside the queue implementation.
var (
	_ catalog.AlbumTrackStore        = (*Store)(nil)
	_ catalog.AlbumMetadataStore     = (*Store)(nil)
	_ catalog.ArtistMetadataStore    = (*Store)(nil)
	_ catalog.ArtistDiscographyStore = (*Store)(nil)
)

// Store implements the catalog store interfaces backed by SQLite.
type Store struct {
	db  *sql.DB
	log *slog.Logger
}

// New returns a Store backed by the existing db and ensures the catalog tables
// exist. It never opens a new connection pool; the caller owns db's lifecycle
// (and its SetMaxOpenConns(1) configuration).
func New(db *sql.DB, logger *slog.Logger) (*Store, error) {
	s := &Store{db: db, log: logger}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("catalog sqlite: migrate: %w", err)
	}
	return s, nil
}

// ─── Schema ──────────────────────────────────────────────────────────

func (s *Store) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS album_tracks (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			album_id          INTEGER NOT NULL,
			disc_number       INTEGER NOT NULL DEFAULT 1,
			track_number      INTEGER NOT NULL DEFAULT 0,
			title             TEXT NOT NULL DEFAULT '',
			artist_name       TEXT NOT NULL DEFAULT '',
			duration_ms       INTEGER NOT NULL DEFAULT 0,
			isrc              TEXT NOT NULL DEFAULT '',
			provider_name     TEXT NOT NULL DEFAULT '',
			provider_track_id TEXT NOT NULL DEFAULT '',
			origin            TEXT NOT NULL DEFAULT 'library',
			created_at        TEXT NOT NULL,
			updated_at        TEXT NOT NULL,
			UNIQUE(album_id, disc_number, track_number),
			FOREIGN KEY (album_id) REFERENCES albums(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_album_tracks_album ON album_tracks(album_id)`,
		`CREATE TABLE IF NOT EXISTS album_metadata (
			album_id            INTEGER PRIMARY KEY,
			provider_name       TEXT NOT NULL DEFAULT '',
			provider_album_id   TEXT NOT NULL DEFAULT '',
			refresh_status      TEXT NOT NULL DEFAULT 'pending',
			last_refreshed_at   TEXT,
			last_accessed_at    TEXT,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			refresh_error       TEXT NOT NULL DEFAULT '',
			updated_at          TEXT NOT NULL,
			FOREIGN KEY (album_id) REFERENCES albums(id) ON DELETE CASCADE
		)		`,
		// Supports the per-status rollup behind GET /api/catalog/status, which
		// the settings card polls every few seconds on the shared single
		// connection.
		`CREATE INDEX IF NOT EXISTS idx_album_metadata_refresh_status ON album_metadata(refresh_status)`,
		`CREATE TABLE IF NOT EXISTS artist_metadata (
			artist_id           INTEGER PRIMARY KEY,
			provider_name       TEXT NOT NULL DEFAULT '',
			provider_artist_id  TEXT NOT NULL DEFAULT '',
			image_url           TEXT NOT NULL DEFAULT '',
			genres              TEXT NOT NULL DEFAULT '[]',
			summary             TEXT NOT NULL DEFAULT '',
			refresh_status      TEXT NOT NULL DEFAULT 'pending',
			last_refreshed_at   TEXT,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			refresh_error       TEXT NOT NULL DEFAULT '',
			updated_at          TEXT NOT NULL,
			FOREIGN KEY (artist_id) REFERENCES artists(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS artist_discography (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			artist_id         INTEGER NOT NULL,
			provider_name     TEXT NOT NULL DEFAULT '',
			provider_album_id TEXT NOT NULL DEFAULT '',
			title             TEXT NOT NULL DEFAULT '',
			year              INTEGER NOT NULL DEFAULT 0,
			type              TEXT NOT NULL DEFAULT '',
			cover_url         TEXT NOT NULL DEFAULT '',
			track_count       INTEGER NOT NULL DEFAULT 0,
			last_seen_at      TEXT NOT NULL,
			FOREIGN KEY (artist_id) REFERENCES artists(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_artist_discography_artist ON artist_discography(artist_id)`,
		// Persisted, resumable refresh work queue. entity_type/entity_id are a
		// polymorphic (album|artist) reference, so no foreign key — the ids are
		// validated by the caller and the queue is drained by the refresh engine.
		`CREATE TABLE IF NOT EXISTS metadata_refresh_queue (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_type TEXT NOT NULL,
			entity_id   INTEGER NOT NULL,
			reason      TEXT NOT NULL DEFAULT '',
			priority    INTEGER NOT NULL DEFAULT 50,
			due_at      TEXT NOT NULL,
			attempts    INTEGER NOT NULL DEFAULT 0,
			last_error  TEXT NOT NULL DEFAULT '',
			created_at  TEXT NOT NULL,
			UNIQUE(entity_type, entity_id)
		)`,
		// Supports the claim query's ORDER BY priority DESC, due_at ASC.
		`CREATE INDEX IF NOT EXISTS idx_refresh_queue_claim ON metadata_refresh_queue(priority, due_at)`,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema transaction: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema: %w", err)
	}
	return nil
}

// ─── Album tracks ────────────────────────────────────────────────────

// ReplaceAlbumTracks atomically replaces every materialized track row for an
// album in one transaction (delete-then-insert).
func (s *Store) ReplaceAlbumTracks(ctx context.Context, albumID int64, tracks []catalog.AlbumTrack) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog sqlite: begin replace album tracks: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	if _, err := tx.ExecContext(ctx, `DELETE FROM album_tracks WHERE album_id=?`, albumID); err != nil {
		return fmt.Errorf("catalog sqlite: clear album tracks %d: %w", albumID, err)
	}

	if len(tracks) == 0 {
		return tx.Commit()
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO album_tracks (album_id, disc_number, track_number, title,
			artist_name, duration_ms, isrc, provider_name, provider_track_id,
			origin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("catalog sqlite: prepare album track insert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for i := range tracks {
		t := tracks[i]
		created := orNow(t.CreatedAt, now)
		updated := orNow(t.UpdatedAt, now)
		if _, err := stmt.ExecContext(ctx,
			albumID, t.DiscNumber, t.TrackNumber, t.Title,
			t.ArtistName, t.DurationMs, t.ISRC, t.ProviderName, t.ProviderTrackID,
			string(t.Origin), fmtTime(created), fmtTime(updated),
		); err != nil {
			return fmt.Errorf("catalog sqlite: insert album track %d/%d: %w", t.DiscNumber, t.TrackNumber, err)
		}
	}
	return tx.Commit()
}

// GetAlbumTracks returns the materialized rows in provider order
// (disc_number, track_number). Never an error when the album has no rows.
func (s *Store) GetAlbumTracks(ctx context.Context, albumID int64) ([]catalog.AlbumTrack, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT album_id, disc_number, track_number, title, artist_name,
			duration_ms, isrc, provider_name, provider_track_id, origin,
			created_at, updated_at
		FROM album_tracks
		WHERE album_id=?
		ORDER BY disc_number, track_number`, albumID)
	if err != nil {
		return nil, fmt.Errorf("catalog sqlite: query album tracks: %w", err)
	}
	defer rows.Close()

	out := []catalog.AlbumTrack{}
	for rows.Next() {
		var t catalog.AlbumTrack
		var origin, createdAt, updatedAt string
		if err := rows.Scan(&t.AlbumID, &t.DiscNumber, &t.TrackNumber, &t.Title,
			&t.ArtistName, &t.DurationMs, &t.ISRC, &t.ProviderName,
			&t.ProviderTrackID, &origin, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("catalog sqlite: scan album track: %w", err)
		}
		t.Origin = catalog.TrackOrigin(origin)
		t.CreatedAt = parseTime(createdAt)
		t.UpdatedAt = parseTime(updatedAt)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog sqlite: iterate album tracks: %w", err)
	}
	return out, nil
}

// DeleteAlbumTracks removes all materialized rows for an album.
func (s *Store) DeleteAlbumTracks(ctx context.Context, albumID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM album_tracks WHERE album_id=?`, albumID); err != nil {
		return fmt.Errorf("catalog sqlite: delete album tracks %d: %w", albumID, err)
	}
	return nil
}

// CountAlbumTracks returns how many materialized rows an album has.
func (s *Store) CountAlbumTracks(ctx context.Context, albumID int64) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM album_tracks WHERE album_id=?`, albumID).Scan(&n); err != nil {
		return 0, fmt.Errorf("catalog sqlite: count album tracks %d: %w", albumID, err)
	}
	return n, nil
}

// ─── Album metadata ──────────────────────────────────────────────────

// UpsertAlbumMetadata inserts or updates the metadata row for one album.
func (s *Store) UpsertAlbumMetadata(ctx context.Context, m catalog.AlbumMetadata) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO album_metadata (album_id, provider_name, provider_album_id,
			refresh_status, last_refreshed_at, last_accessed_at,
			consecutive_failures, refresh_error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(album_id) DO UPDATE SET
			provider_name=excluded.provider_name,
			provider_album_id=excluded.provider_album_id,
			refresh_status=excluded.refresh_status,
			last_refreshed_at=excluded.last_refreshed_at,
			last_accessed_at=excluded.last_accessed_at,
			consecutive_failures=excluded.consecutive_failures,
			refresh_error=excluded.refresh_error,
			updated_at=excluded.updated_at`,
		m.AlbumID, m.ProviderName, m.ProviderAlbumID, string(m.RefreshStatus),
		fmtTime(m.LastRefreshedAt), fmtTime(m.LastAccessedAt),
		m.ConsecutiveFailures, m.RefreshError, fmtTime(orNow(m.UpdatedAt, time.Now().UTC())))
	if err != nil {
		return fmt.Errorf("catalog sqlite: upsert album metadata %d: %w", m.AlbumID, err)
	}
	return nil
}

// GetAlbumMetadata returns the row, or (nil, nil) when absent.
func (s *Store) GetAlbumMetadata(ctx context.Context, albumID int64) (*catalog.AlbumMetadata, error) {
	var m catalog.AlbumMetadata
	var status, refreshed, accessed, updated string
	err := s.db.QueryRowContext(ctx, `
		SELECT album_id, provider_name, provider_album_id, refresh_status,
			COALESCE(last_refreshed_at, ''), COALESCE(last_accessed_at, ''),
			consecutive_failures, refresh_error, updated_at
		FROM album_metadata WHERE album_id=?`, albumID).
		Scan(&m.AlbumID, &m.ProviderName, &m.ProviderAlbumID, &status,
			&refreshed, &accessed, &m.ConsecutiveFailures, &m.RefreshError, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("catalog sqlite: get album metadata %d: %w", albumID, err)
	}
	m.RefreshStatus = catalog.RefreshStatus(status)
	m.LastRefreshedAt = parseTime(refreshed)
	m.LastAccessedAt = parseTime(accessed)
	m.UpdatedAt = parseTime(updated)
	return &m, nil
}

// ListAlbumsDueForRefresh returns album ids whose materialized metadata is
// older than olderThan (or never materialized), most recently accessed first,
// capped at limit. limit <= 0 returns an empty slice.
func (s *Store) ListAlbumsDueForRefresh(ctx context.Context, olderThan time.Time, limit int) ([]int64, error) {
	if limit <= 0 {
		return []int64{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT al.id
		FROM albums al
		LEFT JOIN album_metadata am ON am.album_id = al.id
		WHERE am.album_id IS NULL
		   OR am.last_refreshed_at IS NULL
		   OR am.last_refreshed_at < ?
		ORDER BY am.last_accessed_at DESC, al.id
		LIMIT ?`, fmtTime(olderThan), limit)
	if err != nil {
		return nil, fmt.Errorf("catalog sqlite: list albums due: %w", err)
	}
	defer rows.Close()

	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("catalog sqlite: scan album due: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog sqlite: iterate albums due: %w", err)
	}
	return out, nil
}

// TouchAlbumAccessed records a read so the refresh order favors hot albums. It
// creates a pending row when the album has no metadata yet and never clobbers
// existing refresh bookkeeping.
func (s *Store) TouchAlbumAccessed(ctx context.Context, albumID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO album_metadata (album_id, last_accessed_at, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(album_id) DO UPDATE SET
			last_accessed_at=excluded.last_accessed_at,
			updated_at=excluded.updated_at`,
		albumID, fmtTime(at), fmtTime(orNow(at, time.Now().UTC())))
	if err != nil {
		return fmt.Errorf("catalog sqlite: touch album %d: %w", albumID, err)
	}
	return nil
}

// ─── Artist metadata ─────────────────────────────────────────────────

// UpsertArtistMetadata inserts or updates the overview row for one artist.
func (s *Store) UpsertArtistMetadata(ctx context.Context, m catalog.ArtistMetadata) error {
	genresJSON, err := json.Marshal(m.Genres)
	if err != nil {
		return fmt.Errorf("catalog sqlite: marshal artist genres: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO artist_metadata (artist_id, provider_name, provider_artist_id,
			image_url, genres, summary, refresh_status, last_refreshed_at,
			consecutive_failures, refresh_error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(artist_id) DO UPDATE SET
			provider_name=excluded.provider_name,
			provider_artist_id=excluded.provider_artist_id,
			image_url=excluded.image_url,
			genres=excluded.genres,
			summary=excluded.summary,
			refresh_status=excluded.refresh_status,
			last_refreshed_at=excluded.last_refreshed_at,
			consecutive_failures=excluded.consecutive_failures,
			refresh_error=excluded.refresh_error,
			updated_at=excluded.updated_at`,
		m.ArtistID, m.ProviderName, m.ProviderArtistID, m.ImageURL,
		string(genresJSON), m.Summary, string(m.RefreshStatus),
		fmtTime(m.LastRefreshedAt), m.ConsecutiveFailures, m.RefreshError,
		fmtTime(orNow(m.UpdatedAt, time.Now().UTC())))
	if err != nil {
		return fmt.Errorf("catalog sqlite: upsert artist metadata %d: %w", m.ArtistID, err)
	}
	return nil
}

// GetArtistMetadata returns the row, or (nil, nil) when absent.
func (s *Store) GetArtistMetadata(ctx context.Context, artistID int64) (*catalog.ArtistMetadata, error) {
	var m catalog.ArtistMetadata
	var genresJSON, status, refreshed, updated string
	err := s.db.QueryRowContext(ctx, `
		SELECT artist_id, provider_name, provider_artist_id, image_url, genres,
			summary, refresh_status, COALESCE(last_refreshed_at, ''),
			consecutive_failures, refresh_error, updated_at
		FROM artist_metadata WHERE artist_id=?`, artistID).
		Scan(&m.ArtistID, &m.ProviderName, &m.ProviderArtistID, &m.ImageURL,
			&genresJSON, &m.Summary, &status, &refreshed,
			&m.ConsecutiveFailures, &m.RefreshError, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("catalog sqlite: get artist metadata %d: %w", artistID, err)
	}
	if genresJSON != "" {
		if err := json.Unmarshal([]byte(genresJSON), &m.Genres); err != nil {
			return nil, fmt.Errorf("catalog sqlite: unmarshal artist genres %d: %w", artistID, err)
		}
	}
	m.RefreshStatus = catalog.RefreshStatus(status)
	m.LastRefreshedAt = parseTime(refreshed)
	m.UpdatedAt = parseTime(updated)
	return &m, nil
}

// ─── Artist discography ──────────────────────────────────────────────

// ReplaceArtistDiscography atomically replaces an artist's discovered releases
// in one transaction (delete-then-insert).
func (s *Store) ReplaceArtistDiscography(ctx context.Context, artistID int64, releases []catalog.ArtistDiscographyRelease) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog sqlite: begin replace discography: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	if _, err := tx.ExecContext(ctx, `DELETE FROM artist_discography WHERE artist_id=?`, artistID); err != nil {
		return fmt.Errorf("catalog sqlite: clear discography %d: %w", artistID, err)
	}

	if len(releases) == 0 {
		return tx.Commit()
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO artist_discography (artist_id, provider_name, provider_album_id,
			title, year, type, cover_url, track_count, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("catalog sqlite: prepare discography insert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for i := range releases {
		r := releases[i]
		if _, err := stmt.ExecContext(ctx,
			artistID, r.ProviderName, r.ProviderAlbumID, r.Title, r.Year,
			r.Type, r.CoverURL, r.TrackCount, fmtTime(orNow(r.LastSeenAt, now)),
		); err != nil {
			return fmt.Errorf("catalog sqlite: insert release %q: %w", r.Title, err)
		}
	}
	return tx.Commit()
}

// GetArtistDiscography returns an artist's discovered releases ordered by year
// then title. Never an error when the artist has no releases.
func (s *Store) GetArtistDiscography(ctx context.Context, artistID int64) ([]catalog.ArtistDiscographyRelease, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT artist_id, provider_name, provider_album_id, title, year, type,
			cover_url, track_count, last_seen_at
		FROM artist_discography
		WHERE artist_id=?
		ORDER BY year, title`, artistID)
	if err != nil {
		return nil, fmt.Errorf("catalog sqlite: query discography: %w", err)
	}
	defer rows.Close()

	out := []catalog.ArtistDiscographyRelease{}
	for rows.Next() {
		var r catalog.ArtistDiscographyRelease
		var lastSeen string
		if err := rows.Scan(&r.ArtistID, &r.ProviderName, &r.ProviderAlbumID,
			&r.Title, &r.Year, &r.Type, &r.CoverURL, &r.TrackCount, &lastSeen); err != nil {
			return nil, fmt.Errorf("catalog sqlite: scan release: %w", err)
		}
		r.LastSeenAt = parseTime(lastSeen)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog sqlite: iterate discography: %w", err)
	}
	return out, nil
}

// ─── Time helpers ────────────────────────────────────────────────────

// fmtTime renders t as RFC3339 UTC, or SQL NULL when t is the zero time so
// "never refreshed/accessed" stays distinguishable from a real timestamp.
func fmtTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTime parses an RFC3339 UTC timestamp; empty or malformed input yields
// the zero time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// orNow returns t, or fallback when t is the zero time.
func orNow(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}
