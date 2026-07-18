// Package sqlite implements the library.Store interface using SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"

	_ "modernc.org/sqlite"
)

// Store implements library.Store backed by SQLite.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite database at the given path.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=30000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite serializes writes.

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite migrate: %w", err)
	}

	return s, nil
}

// Close shuts down the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// ─── Schema ──────────────────────────────────────────────────────────

func (s *Store) migrate() error {
	// Ensure version tracking table exists.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("migration: schema_version: %w", err)
	}

	// Read current schema version (0 on fresh DB).
	var version int
	s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)

	// Version 1: initial schema (idempotent CREATE TABLE IF NOT EXISTS).
	if version < 1 {
		statements := []string{
		`CREATE TABLE IF NOT EXISTS artists (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			genres TEXT,
			summary TEXT,
			thumb_url TEXT,
			spotify_id TEXT,
			itunes_id TEXT,
			deezer_id TEXT,
			musicbrainz_id TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`DROP INDEX IF EXISTS idx_artists_name`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_artists_name ON artists(name)`,
		`CREATE TABLE IF NOT EXISTS albums (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			artist_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			year INTEGER,
			genres TEXT,
			track_count INTEGER,
			duration INTEGER,
			thumb_url TEXT,
			album_type TEXT DEFAULT 'album',
			release_date TEXT,
			spotify_id TEXT,
			itunes_id TEXT,
			deezer_id TEXT,
			musicbrainz_id TEXT,
			tidal_id TEXT,
			qobuz_id TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (artist_id) REFERENCES artists(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS tracks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			album_id INTEGER NOT NULL,
			artist_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			track_number INTEGER,
			disc_number INTEGER DEFAULT 1,
			duration INTEGER,
			file_path TEXT,
			bitrate INTEGER,
			file_size INTEGER,
			spotify_id TEXT,
			itunes_id TEXT,
			deezer_id TEXT,
			musicbrainz_id TEXT,
			tidal_id TEXT,
			qobuz_id TEXT,
			acoustid TEXT,
			isrc TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (album_id) REFERENCES albums(id) ON DELETE CASCADE,
			FOREIGN KEY (artist_id) REFERENCES artists(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_albums_artist ON albums(artist_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_album ON tracks(album_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks(artist_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_file_path ON tracks(file_path)`,
		`CREATE INDEX IF NOT EXISTS idx_artists_spotify ON artists(spotify_id)`,
		`CREATE INDEX IF NOT EXISTS idx_artists_deezer ON artists(deezer_id)`,
		`CREATE INDEX IF NOT EXISTS idx_albums_spotify ON albums(spotify_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_spotify ON tracks(spotify_id)`,
	}

	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migration v1: %w", err)
		}
	}
		version = 1
	}

	// Record current version.
	if _, err := s.db.Exec(`DELETE FROM schema_version`); err != nil {
		return fmt.Errorf("migration: clear version: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("migration: set version: %w", err)
	}
	return nil
}

// ─── Artists ─────────────────────────────────────────────────────────

func (s *Store) UpsertArtist(ctx context.Context, artist *domain.Artist) (int64, error) {
	genresJSON, _ := json.Marshal(artist.Genres)
	now := time.Now().UTC().Format(time.RFC3339)

	if artist.ID != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE artists SET name=?, genres=?, summary=?, thumb_url=?,
			spotify_id=?, itunes_id=?, deezer_id=?, musicbrainz_id=?,
			updated_at=?
			WHERE id=?`,
			artist.Name, string(genresJSON), artist.Summary, artist.ThumbURL,
			artist.SpotifyID, artist.ITunesID, artist.DeezerID, artist.MusicBrainzID,
			now, artist.ID,
		)
		return artist.ID, err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO artists (name, genres, summary, thumb_url,
			spotify_id, itunes_id, deezer_id, musicbrainz_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artist.Name, string(genresJSON), artist.Summary, artist.ThumbURL,
		artist.SpotifyID, artist.ITunesID, artist.DeezerID, artist.MusicBrainzID,
		now, now,
	)
	if err != nil {
		return 0, err
	}

	id, _ := result.LastInsertId()
	if id != 0 {
		return id, nil
	}

	// Duplicate name — return existing record ID.
	existing, err := s.GetArtistByName(ctx, artist.Name)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}
	return 0, fmt.Errorf("artist insert failed: %s", artist.Name)
}

func (s *Store) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, genres, summary, thumb_url,
			spotify_id, itunes_id, deezer_id, musicbrainz_id,
			created_at, updated_at
		FROM artists WHERE id=?`, id)
	return scanArtist(row)
}

func (s *Store) GetArtistByName(ctx context.Context, name string) (*domain.Artist, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, genres, summary, thumb_url,
			spotify_id, itunes_id, deezer_id, musicbrainz_id,
			created_at, updated_at
		FROM artists WHERE name=?`, name)
	return scanArtist(row)
}

func (s *Store) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, genres, summary, thumb_url,
			spotify_id, itunes_id, deezer_id, musicbrainz_id,
			created_at, updated_at
		FROM artists ORDER BY name LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtists(rows)
}

func (s *Store) SearchArtists(ctx context.Context, query string, limit int) ([]domain.Artist, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, genres, summary, thumb_url,
			spotify_id, itunes_id, deezer_id, musicbrainz_id,
			created_at, updated_at
		FROM artists WHERE name LIKE ? ORDER BY name LIMIT ?`,
		"%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtists(rows)
}

// ─── Albums ──────────────────────────────────────────────────────────

func (s *Store) UpsertAlbum(ctx context.Context, album *domain.Album) (int64, error) {
	genresJSON, _ := json.Marshal(album.Genres)
	now := time.Now().UTC().Format(time.RFC3339)

	if album.ID != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE albums SET artist_id=?, title=?, year=?, genres=?, track_count=?,
			duration=?, thumb_url=?, album_type=?, release_date=?,
			spotify_id=?, itunes_id=?, deezer_id=?, musicbrainz_id=?,
			tidal_id=?, qobuz_id=?, updated_at=?
			WHERE id=?`,
			album.ArtistID, album.Title, album.Year, string(genresJSON), album.TrackCount,
			album.Duration, album.ThumbURL, album.AlbumType, album.ReleaseDate,
			album.SpotifyID, album.ITunesID, album.DeezerID, album.MusicBrainzID,
			album.TidalID, album.QobuzID, now, album.ID,
		)
		return album.ID, err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO albums (artist_id, title, year, genres, track_count,
			duration, thumb_url, album_type, release_date,
			spotify_id, itunes_id, deezer_id, musicbrainz_id,
			tidal_id, qobuz_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		album.ArtistID, album.Title, album.Year, string(genresJSON), album.TrackCount,
		album.Duration, album.ThumbURL, album.AlbumType, album.ReleaseDate,
		album.SpotifyID, album.ITunesID, album.DeezerID, album.MusicBrainzID,
		album.TidalID, album.QobuzID, now, now,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, artist_id, title, year, genres, track_count, duration, thumb_url,
			album_type, release_date, spotify_id, itunes_id, deezer_id,
			musicbrainz_id, tidal_id, qobuz_id, created_at, updated_at
		FROM albums WHERE id=?`, id)
	return scanAlbum(row)
}

func (s *Store) GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error) {
	rows, err := s.db.QueryContext(ctx, albumSelect+" WHERE artist_id=? ORDER BY year, title", artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlbums(rows)
}

func (s *Store) SearchAlbums(ctx context.Context, query string, limit int) ([]domain.Album, error) {
	rows, err := s.db.QueryContext(ctx, albumSelect+" WHERE title LIKE ? ORDER BY title LIMIT ?",
		"%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlbums(rows)
}

// ─── Tracks ──────────────────────────────────────────────────────────

func (s *Store) UpsertTrack(ctx context.Context, track *domain.Track) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	if track.ID != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE tracks SET album_id=?, artist_id=?, title=?, track_number=?,
			disc_number=?, duration=?, file_path=?, bitrate=?, file_size=?,
			spotify_id=?, itunes_id=?, deezer_id=?, musicbrainz_id=?,
			tidal_id=?, qobuz_id=?, acoustid=?, isrc=?, updated_at=?
			WHERE id=?`,
			track.AlbumID, track.ArtistID, track.Title, track.TrackNumber,
			track.DiscNumber, track.Duration, track.FilePath, track.Bitrate, track.FileSize,
			track.SpotifyID, track.ITunesID, track.DeezerID, track.MusicBrainzID,
			track.TidalID, track.QobuzID, track.AcoustID, track.ISRC,
			now, track.ID,
		)
		return track.ID, err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO tracks (album_id, artist_id, title, track_number,
			disc_number, duration, file_path, bitrate, file_size,
			spotify_id, itunes_id, deezer_id, musicbrainz_id,
			tidal_id, qobuz_id, acoustid, isrc, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		track.AlbumID, track.ArtistID, track.Title, track.TrackNumber,
		track.DiscNumber, track.Duration, track.FilePath, track.Bitrate, track.FileSize,
		track.SpotifyID, track.ITunesID, track.DeezerID, track.MusicBrainzID,
		track.TidalID, track.QobuzID, track.AcoustID, track.ISRC,
		now, now,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) GetTrack(ctx context.Context, id int64) (*domain.Track, error) {
	row := s.db.QueryRowContext(ctx, trackSelect+" WHERE id=?", id)
	return scanTrack(row)
}

func (s *Store) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	rows, err := s.db.QueryContext(ctx, trackSelect+" WHERE album_id=? ORDER BY disc_number, track_number", albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTracks(rows)
}

func (s *Store) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	rows, err := s.db.QueryContext(ctx, trackSelect+" WHERE artist_id=? ORDER BY title", artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTracks(rows)
}

func (s *Store) SearchTracks(ctx context.Context, query string, limit int) ([]domain.Track, error) {
	rows, err := s.db.QueryContext(ctx, trackSelect+" WHERE title LIKE ? ORDER BY title LIMIT ?",
		"%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTracks(rows)
}

func (s *Store) GetTrackByFilePath(ctx context.Context, filePath string) (*domain.Track, error) {
	row := s.db.QueryRowContext(ctx, trackSelect+" WHERE file_path=?", filePath)
	return scanTrack(row)
}

func (s *Store) DeleteTrack(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM tracks WHERE id=?", id)
	return err
}

// ─── External ID lookups ─────────────────────────────────────────────

func (s *Store) GetArtistByExternalID(ctx context.Context, service, externalID string) (*domain.Artist, error) {
	col := externalIDColumn(service)
	if col == "" {
		return nil, fmt.Errorf("unsupported service: %s", service)
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, genres, summary, thumb_url,
			spotify_id, itunes_id, deezer_id, musicbrainz_id,
			created_at, updated_at
		FROM artists WHERE `+col+`=?`, externalID)
	return scanArtist(row)
}

func (s *Store) GetAlbumByExternalID(ctx context.Context, service, externalID string) (*domain.Album, error) {
	col := externalIDColumn(service)
	if col == "" {
		return nil, fmt.Errorf("unsupported service: %s", service)
	}
	row := s.db.QueryRowContext(ctx,
		albumSelect+" WHERE "+col+"=?", externalID)
	return scanAlbum(row)
}

func (s *Store) GetTrackByExternalID(ctx context.Context, service, externalID string) (*domain.Track, error) {
	col := externalIDColumn(service)
	if col == "" {
		return nil, fmt.Errorf("unsupported service: %s", service)
	}
	row := s.db.QueryRowContext(ctx,
		trackSelect+" WHERE "+col+"=?", externalID)
	return scanTrack(row)
}

// ─── Internal scan helpers ───────────────────────────────────────────

const albumSelect = `SELECT id, artist_id, title, year, genres, track_count, duration, thumb_url,
	album_type, release_date, spotify_id, itunes_id, deezer_id,
	musicbrainz_id, tidal_id, qobuz_id, created_at, updated_at
	FROM albums`

const trackSelect = `SELECT id, album_id, artist_id, title, track_number, disc_number,
	duration, file_path, bitrate, file_size,
	spotify_id, itunes_id, deezer_id, musicbrainz_id,
	tidal_id, qobuz_id, acoustid, isrc, created_at, updated_at
	FROM tracks`

func scanArtist(row *sql.Row) (*domain.Artist, error) {
	var a domain.Artist
	var genresJSON string
	var createdAt, updatedAt string
	err := row.Scan(&a.ID, &a.Name, &genresJSON, &a.Summary, &a.ThumbURL,
		&a.SpotifyID, &a.ITunesID, &a.DeezerID, &a.MusicBrainzID,
		&createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	json.Unmarshal([]byte(genresJSON), &a.Genres)
	a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	a.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &a, nil
}

func scanArtists(rows *sql.Rows) ([]domain.Artist, error) {
	var artists []domain.Artist
	for rows.Next() {
		var a domain.Artist
		var genresJSON, createdAt, updatedAt string
		if err := rows.Scan(&a.ID, &a.Name, &genresJSON, &a.Summary, &a.ThumbURL,
			&a.SpotifyID, &a.ITunesID, &a.DeezerID, &a.MusicBrainzID,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(genresJSON), &a.Genres)
		a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		a.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		artists = append(artists, a)
	}
	return artists, rows.Err()
}

func scanAlbum(row *sql.Row) (*domain.Album, error) {
	var a domain.Album
	var genresJSON, createdAt, updatedAt string
	var albumType string
	err := row.Scan(&a.ID, &a.ArtistID, &a.Title, &a.Year, &genresJSON, &a.TrackCount,
		&a.Duration, &a.ThumbURL, &albumType, &a.ReleaseDate,
		&a.SpotifyID, &a.ITunesID, &a.DeezerID, &a.MusicBrainzID,
		&a.TidalID, &a.QobuzID, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	json.Unmarshal([]byte(genresJSON), &a.Genres)
	a.AlbumType = domain.AlbumType(albumType)
	a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	a.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &a, nil
}

func scanAlbums(rows *sql.Rows) ([]domain.Album, error) {
	var albums []domain.Album
	for rows.Next() {
		var a domain.Album
		var genresJSON, createdAt, updatedAt, albumType string
		if err := rows.Scan(&a.ID, &a.ArtistID, &a.Title, &a.Year, &genresJSON,
			&a.TrackCount, &a.Duration, &a.ThumbURL, &albumType, &a.ReleaseDate,
			&a.SpotifyID, &a.ITunesID, &a.DeezerID, &a.MusicBrainzID,
			&a.TidalID, &a.QobuzID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(genresJSON), &a.Genres)
		a.AlbumType = domain.AlbumType(albumType)
		a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		a.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		albums = append(albums, a)
	}
	return albums, rows.Err()
}

func scanTrack(row *sql.Row) (*domain.Track, error) {
	var t domain.Track
	var createdAt, updatedAt string
	err := row.Scan(&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.TrackNumber,
		&t.DiscNumber, &t.Duration, &t.FilePath, &t.Bitrate, &t.FileSize,
		&t.SpotifyID, &t.ITunesID, &t.DeezerID, &t.MusicBrainzID,
		&t.TidalID, &t.QobuzID, &t.AcoustID, &t.ISRC,
		&createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &t, nil
}

func scanTracks(rows *sql.Rows) ([]domain.Track, error) {
	var tracks []domain.Track
	for rows.Next() {
		var t domain.Track
		var createdAt, updatedAt string
		if err := rows.Scan(&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.TrackNumber,
			&t.DiscNumber, &t.Duration, &t.FilePath, &t.Bitrate, &t.FileSize,
			&t.SpotifyID, &t.ITunesID, &t.DeezerID, &t.MusicBrainzID,
			&t.TidalID, &t.QobuzID, &t.AcoustID, &t.ISRC,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

func externalIDColumn(service string) string {
	switch strings.ToLower(service) {
	case "spotify":
		return "spotify_id"
	case "itunes", "apple":
		return "itunes_id"
	case "deezer":
		return "deezer_id"
	case "musicbrainz":
		return "musicbrainz_id"
	case "tidal":
		return "tidal_id"
	case "qobuz":
		return "qobuz_id"
	default:
		return ""
	}
}
