// Package sqlite implements the library.Store interface using SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"

	_ "modernc.org/sqlite"
)

// Store implements library.Store backed by SQLite.
type Store struct {
	db  *sql.DB
	log *slog.Logger
}

// New opens (or creates) a SQLite database at the given path.
func New(path string, logger *slog.Logger) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=30000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite serializes writes.

	s := &Store{db: db, log: logger}
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

// DB returns the underlying *sql.DB so that other packages (e.g. download)
// can share the same SQLite connection.
func (s *Store) DB() *sql.DB {
	return s.db
}

// ─── Schema ──────────────────────────────────────────────────────────

func (s *Store) migrate() error {
	statements := []string{
		// ── Library: artists, albums, tracks ──
		`CREATE TABLE IF NOT EXISTS artists (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			genres TEXT,
			summary TEXT,
			thumb_url TEXT,
			external_ids TEXT DEFAULT '{}',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
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
			external_ids TEXT DEFAULT '{}',
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
			external_ids TEXT DEFAULT '{}',
			acoustid TEXT,
			isrc TEXT,
			quality_profile_id INTEGER,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (album_id) REFERENCES albums(id) ON DELETE CASCADE,
			FOREIGN KEY (artist_id) REFERENCES artists(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_albums_artist ON albums(artist_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_album ON tracks(album_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks(artist_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_file_path ON tracks(file_path)`,

		// ── Playlists ──
		`CREATE TABLE IF NOT EXISTS playlists (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			source             TEXT NOT NULL,
			source_playlist_id TEXT NOT NULL,
			name               TEXT NOT NULL,
			description        TEXT,
			track_count        INTEGER,
			cover_url          TEXT,
			owner_name         TEXT,
			is_public          INTEGER DEFAULT 1,
			auto_sync          INTEGER DEFAULT 0,
			sync_mode          TEXT DEFAULT 'mirror',
			synced_at          TEXT,
			created_at         TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at         TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(source, source_playlist_id)
		)`,
		`CREATE TABLE IF NOT EXISTS playlist_tracks (
			playlist_id     INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
			position        INTEGER NOT NULL,
			track_id        INTEGER REFERENCES tracks(id),
			source_track_id TEXT NOT NULL,
			title           TEXT NOT NULL,
			artist          TEXT NOT NULL,
			album           TEXT,
			duration_ms     INTEGER,
			isrc            TEXT,
			added_at        TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (playlist_id, position)
		)`,

		// ── Downloads ──
		`CREATE TABLE IF NOT EXISTS downloads (
			id TEXT PRIMARY KEY,
			source_name TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			filename TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'initializing',
			progress REAL NOT NULL DEFAULT 0,
			size INTEGER NOT NULL DEFAULT 0,
			transferred INTEGER NOT NULL DEFAULT 0,
			speed INTEGER NOT NULL DEFAULT 0,
			file_path TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			track_id TEXT NOT NULL DEFAULT '',
			cover_url TEXT NOT NULL DEFAULT '',
			artist TEXT NOT NULL DEFAULT '',
			album TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			track_number INTEGER NOT NULL DEFAULT 0,
			disc_number INTEGER NOT NULL DEFAULT 0,
			year INTEGER NOT NULL DEFAULT 0,
			retry_count INTEGER NOT NULL DEFAULT 0,
			retry_after TEXT NOT NULL DEFAULT '',
			bitrate INTEGER NOT NULL DEFAULT 0,
			format TEXT NOT NULL DEFAULT '',
			playlist_id TEXT NOT NULL DEFAULT '',
			quality_profile_id INTEGER,
			isrc TEXT NOT NULL DEFAULT '',
			library_track_id INTEGER NOT NULL DEFAULT 0,
			album_type TEXT NOT NULL DEFAULT '',
			album_tracks TEXT NOT NULL DEFAULT '',
			download_client TEXT NOT NULL DEFAULT '',
			provider_id TEXT NOT NULL DEFAULT '',
			magnet_uri TEXT NOT NULL DEFAULT '',
			folder_path TEXT NOT NULL DEFAULT '',
			imported_track_ids TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS download_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			download_id TEXT NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
			event_type TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_downloads_state ON downloads(state)`,
		`CREATE INDEX IF NOT EXISTS idx_downloads_playlist_id ON downloads(playlist_id)`,

		// ── Quality profiles ──
		`CREATE TABLE IF NOT EXISTS quality_profiles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			description TEXT DEFAULT '',
			ranked_targets TEXT NOT NULL DEFAULT '[]',
			fallback_enabled INTEGER NOT NULL DEFAULT 1,
			search_mode TEXT NOT NULL DEFAULT 'priority',
			rank_candidates_by_quality INTEGER NOT NULL DEFAULT 0,
			upgrade_policy TEXT NOT NULL DEFAULT 'acceptable',
			upgrade_cutoff_index INTEGER NOT NULL DEFAULT 0,
			replace_lower_quality INTEGER NOT NULL DEFAULT 0,
			is_default INTEGER NOT NULL DEFAULT 0,
			created_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_quality_profiles_default ON quality_profiles(is_default)`,
		`INSERT INTO quality_profiles (name, description, ranked_targets, fallback_enabled, search_mode, rank_candidates_by_quality, upgrade_policy, upgrade_cutoff_index, is_default)
		SELECT 'Balanced', 'FLAC preferred, MP3 320 fallback',
			'[{"label":"FLAC","format":"flac"},{"label":"MP3 320","format":"mp3","min_bitrate":320}]',
			1, 'priority', 1, 'acceptable', 0, 1
		WHERE NOT EXISTS (SELECT 1 FROM quality_profiles WHERE name = 'Balanced')`,
		`INSERT INTO quality_profiles (name, description, ranked_targets, fallback_enabled, search_mode, rank_candidates_by_quality, upgrade_policy, upgrade_cutoff_index, is_default)
		SELECT 'Lossless Only', 'FLAC only, no fallback',
			'[{"label":"FLAC","format":"flac"}]',
			0, 'priority', 1, 'acceptable', 0, 0
		WHERE NOT EXISTS (SELECT 1 FROM quality_profiles WHERE name = 'Lossless Only')`,
		`INSERT INTO quality_profiles (name, description, ranked_targets, fallback_enabled, search_mode, rank_candidates_by_quality, upgrade_policy, upgrade_cutoff_index, is_default)
		SELECT 'Any Quality', 'Accept any format and bitrate',
			'[]',
			1, 'priority', 0, 'acceptable', 0, 0
		WHERE NOT EXISTS (SELECT 1 FROM quality_profiles WHERE name = 'Any Quality')`,

		// ── Album discovery cache ──
		`CREATE TABLE IF NOT EXISTS album_discovery_cache (
			album_id         INTEGER PRIMARY KEY,
			provider_name    TEXT NOT NULL,
			provider_album_id TEXT NOT NULL,
			tracks_json      TEXT NOT NULL,
			cached_at        TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (album_id) REFERENCES albums(id) ON DELETE CASCADE
		)`,

		// ── Artist overview cache ──
		`CREATE TABLE IF NOT EXISTS artist_overview_cache (
			normalized_name    TEXT PRIMARY KEY,
			artist_name        TEXT NOT NULL DEFAULT '',
			provider_name      TEXT NOT NULL,
			provider_artist_id TEXT NOT NULL,
			image_url          TEXT NOT NULL DEFAULT '',
			genres_json        TEXT NOT NULL DEFAULT '[]',
			top_tracks_json    TEXT,
			discography_json   TEXT NOT NULL,
			cached_at          TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}

	// Wrap schema init in a transaction so partial failures don't leave the
	// database in an inconsistent state.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("schema: begin transaction: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			s.log.Error("schema init failed", "error", err, "component", "lib_store")
			return fmt.Errorf("schema init: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("schema: commit: %w", err)
	}

	// Run idempotent migrations outside the main transaction. ALTER TABLE
	// ADD COLUMN in SQLite fails if the column already exists — we tolerate
	// that error to support upgrades from databases created before the column
	// was added.
	migrations := []string{
		`ALTER TABLE playlists ADD COLUMN sync_mode TEXT DEFAULT 'mirror'`,
		`UPDATE playlists SET sync_mode='mirror' WHERE sync_mode IS NULL OR sync_mode=''`,
		`ALTER TABLE downloads ADD COLUMN provider_id TEXT DEFAULT ''`,
	}
	for _, stmt := range migrations {
		if _, err := s.db.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				s.log.Debug("migration already applied", "stmt", stmt, "component", "lib_store")
			} else {
				s.log.Error("migration failed", "stmt", stmt, "error", err, "component", "lib_store")
				return fmt.Errorf("migration %q: %w", stmt, err)
			}
		}
	}

	return nil
}

// ─── Artists ─────────────────────────────────────────────────────────

func (s *Store) UpsertArtist(ctx context.Context, artist *domain.Artist) (int64, error) {
	genresJSON, _ := json.Marshal(artist.Genres)
	extIDsJSON, _ := json.Marshal(artist.ExternalIDs)
	now := time.Now().UTC().Format(time.RFC3339)

	if artist.ID != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE artists SET name=?, genres=?, summary=?, thumb_url=?,
			external_ids=?, updated_at=?
			WHERE id=?`,
			artist.Name, string(genresJSON), artist.Summary, artist.ThumbURL,
			string(extIDsJSON), now, artist.ID,
		)
		if err != nil {
			s.log.Error("upsert artist update failed", "error", err, "component", "lib_store")
		}
		return artist.ID, err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO artists (name, genres, summary, thumb_url,
			external_ids, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		artist.Name, string(genresJSON), artist.Summary, artist.ThumbURL,
		string(extIDsJSON), now, now,
	)
	if err != nil {
		s.log.Error("upsert artist insert failed", "error", err, "component", "lib_store")
		return 0, err
	}

	id, _ := result.LastInsertId()
	if id != 0 {
		return id, nil
	}

	// Duplicate name — return existing record ID.
	existing, err := s.GetArtistByName(ctx, artist.Name)
	if err != nil {
		s.log.Error("upsert artist getByName failed", "error", err, "component", "lib_store")
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
			external_ids, created_at, updated_at,
			COALESCE((SELECT al.id FROM albums al WHERE al.artist_id = artists.id ORDER BY al.year, al.title LIMIT 1), 0)
		FROM artists WHERE id=?`, id)
	return s.scanArtist(row)
}

func (s *Store) GetArtistByName(ctx context.Context, name string) (*domain.Artist, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, genres, summary, thumb_url,
			external_ids, created_at, updated_at,
			COALESCE((SELECT al.id FROM albums al WHERE al.artist_id = artists.id ORDER BY al.year, al.title LIMIT 1), 0)
		FROM artists WHERE name=?`, name)
	return s.scanArtist(row)
}

func (s *Store) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, genres, summary, thumb_url,
			external_ids, created_at, updated_at,
			COALESCE((SELECT al.id FROM albums al WHERE al.artist_id = artists.id ORDER BY al.year, al.title LIMIT 1), 0)
		FROM artists ORDER BY name LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		s.log.Error("list artists failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()
	return s.scanArtists(rows)
}

func (s *Store) SearchArtists(ctx context.Context, query string, limit int) ([]domain.Artist, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, genres, summary, thumb_url,
			external_ids, created_at, updated_at,
			COALESCE((SELECT al.id FROM albums al WHERE al.artist_id = artists.id ORDER BY al.year, al.title LIMIT 1), 0)
		FROM artists WHERE name LIKE ? ORDER BY name LIMIT ?`,
		"%"+query+"%", limit)
	if err != nil {
		s.log.Error("search artists failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()
	return s.scanArtists(rows)
}

func (s *Store) SetArtistThumbURL(ctx context.Context, artistID int64, thumbURL string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE artists SET thumb_url=?, updated_at=? WHERE id=?`,
		thumbURL, now, artistID)
	if err != nil {
		s.log.Error("set artist thumb_url failed", "error", err, "component", "lib_store")
	}
	return err
}

// ─── Albums ──────────────────────────────────────────────────────────

func (s *Store) UpsertAlbum(ctx context.Context, album *domain.Album) (int64, error) {
	genresJSON, _ := json.Marshal(album.Genres)
	extIDsJSON, _ := json.Marshal(album.ExternalIDs)
	now := time.Now().UTC().Format(time.RFC3339)

	if album.ID != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE albums SET artist_id=?, title=?, year=?, genres=?, track_count=?,
			duration=?, thumb_url=?, album_type=?, release_date=?,
			external_ids=?, updated_at=?
			WHERE id=?`,
			album.ArtistID, album.Title, album.Year, string(genresJSON), album.TrackCount,
			album.Duration, album.ThumbURL, album.AlbumType, album.ReleaseDate,
			string(extIDsJSON), now, album.ID,
		)
		if err != nil {
			s.log.Error("upsert album update failed", "error", err, "component", "lib_store")
		}
		return album.ID, err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO albums (artist_id, title, year, genres, track_count,
			duration, thumb_url, album_type, release_date,
			external_ids, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		album.ArtistID, album.Title, album.Year, string(genresJSON), album.TrackCount,
		album.Duration, album.ThumbURL, album.AlbumType, album.ReleaseDate,
		string(extIDsJSON), now, now,
	)
	if err != nil {
		s.log.Error("upsert album insert failed", "error", err, "component", "lib_store")
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	row := s.db.QueryRowContext(ctx, albumSelect+" WHERE id=?", id)
	return s.scanAlbum(row)
}

func (s *Store) GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error) {
	rows, err := s.db.QueryContext(ctx, albumSelect+" WHERE artist_id=? ORDER BY year, title", artistID)
	if err != nil {
		s.log.Error("get albums by artist failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()
	return s.scanAlbums(rows)
}

func (s *Store) SearchAlbums(ctx context.Context, query string, limit int) ([]domain.Album, error) {
	rows, err := s.db.QueryContext(ctx, albumSelect+" WHERE title LIKE ? ORDER BY title LIMIT ?",
		"%"+query+"%", limit)
	if err != nil {
		s.log.Error("search albums failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()
	return s.scanAlbums(rows)
}

// ─── Tracks ──────────────────────────────────────────────────────────

func (s *Store) UpsertTrack(ctx context.Context, track *domain.Track) (int64, error) {
	extIDsJSON, _ := json.Marshal(track.ExternalIDs)
	now := time.Now().UTC().Format(time.RFC3339)

	if track.ID != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE tracks SET album_id=?, artist_id=?, title=?, track_number=?,
			disc_number=?, duration=?, file_path=?, bitrate=?, file_size=?,
			external_ids=?, acoustid=?, isrc=?, quality_profile_id=?, updated_at=?
			WHERE id=?`,
			track.AlbumID, track.ArtistID, track.Title, track.TrackNumber,
			track.DiscNumber, track.Duration, track.FilePath, track.Bitrate, track.FileSize,
			string(extIDsJSON), track.AcoustID, track.ISRC, track.QualityProfileID,
			now, track.ID,
		)
		if err != nil {
			s.log.Error("upsert track update failed", "error", err, "component", "lib_store")
		}
		return track.ID, err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO tracks (album_id, artist_id, title, track_number,
			disc_number, duration, file_path, bitrate, file_size,
			external_ids, acoustid, isrc, quality_profile_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		track.AlbumID, track.ArtistID, track.Title, track.TrackNumber,
		track.DiscNumber, track.Duration, track.FilePath, track.Bitrate, track.FileSize,
		string(extIDsJSON), track.AcoustID, track.ISRC, track.QualityProfileID,
		now, now,
	)
	if err != nil {
		s.log.Error("upsert track insert failed", "error", err, "component", "lib_store")
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) GetTrack(ctx context.Context, id int64) (*domain.Track, error) {
	row := s.db.QueryRowContext(ctx, trackSelect+" WHERE id=?", id)
	return s.scanTrack(row)
}

func (s *Store) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	rows, err := s.db.QueryContext(ctx, trackSelect+" WHERE album_id=? ORDER BY disc_number, track_number", albumID)
	if err != nil {
		s.log.Error("get tracks by album failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()
	return s.scanTracks(rows)
}

func (s *Store) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	rows, err := s.db.QueryContext(ctx, trackSelect+" WHERE artist_id=? ORDER BY title", artistID)
	if err != nil {
		s.log.Error("get tracks by artist failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()
	return s.scanTracks(rows)
}

func (s *Store) SearchTracks(ctx context.Context, query string, limit int) ([]domain.Track, error) {
	rows, err := s.db.QueryContext(ctx, trackSelect+" WHERE title LIKE ? ORDER BY title LIMIT ?",
		"%"+query+"%", limit)
	if err != nil {
		s.log.Error("search tracks failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()
	return s.scanTracks(rows)
}

func (s *Store) GetTrackByISRC(ctx context.Context, isrc string) (*domain.Track, error) {
	if isrc == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, trackSelect+" WHERE isrc = ?", isrc)
	t, err := s.scanTrack(row)
	if err != nil {
		s.log.Error("get track by isrc failed", "error", err, "component", "lib_store")
		return nil, err
	}
	return t, nil
}

func (s *Store) GetTrackByFilePath(ctx context.Context, filePath string) (*domain.Track, error) {
	row := s.db.QueryRowContext(ctx, trackSelect+" WHERE file_path=?", filePath)
	return s.scanTrack(row)
}

func (s *Store) DeleteTrack(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM tracks WHERE id=?", id)
	if err != nil {
		s.log.Error("delete track failed", "error", err, "component", "lib_store")
	}
	return err
}

// ListTracksWithQuality returns all library tracks with quality-related metadata
// for upgrade scanning. Format is derived from the file_path extension.
func (s *Store) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	rows, err := s.db.QueryContext(ctx, trackSelect)
	if err != nil {
		return nil, fmt.Errorf("list tracks with quality: %w", err)
	}
	defer rows.Close()
	return s.scanTracks(rows)
}

// ImportTrack creates or updates artist and album records, then inserts
// the track. Returns the track ID. Used by both scanner and download handler.
func (s *Store) ImportTrack(ctx context.Context, track *domain.Track, artistName, albumTitle string, albumYear int, genres []string) (int64, error) {
	artistID, err := s.getOrCreateArtist(ctx, artistName)
	if err != nil {
		s.log.Error("import track: getOrCreateArtist failed", "error", err, "component", "lib_store")
		return 0, fmt.Errorf("import artist: %w", err)
	}

	albumID, err := s.getOrCreateAlbum(ctx, artistID, albumTitle, albumYear, genres)
	if err != nil {
		s.log.Error("import track: getOrCreateAlbum failed", "error", err, "component", "lib_store")
		return 0, fmt.Errorf("import album: %w", err)
	}

	track.ArtistID = artistID
	track.AlbumID = albumID
	return s.UpsertTrack(ctx, track)
}

func (s *Store) getOrCreateArtist(ctx context.Context, name string) (int64, error) {
	existing, err := s.GetArtistByName(ctx, name)
	if err != nil {
		s.log.Error("getOrCreateArtist: getByName failed", "error", err, "component", "lib_store")
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}
	return s.UpsertArtist(ctx, &domain.Artist{Name: name})
}

func (s *Store) getOrCreateAlbum(ctx context.Context, artistID int64, title string, year int, genres []string) (int64, error) {
	// Exact match first — SearchAlbums uses LIKE and a hard limit of 10,
	// which can miss the correct album if 10+ similar titles exist.
	row := s.db.QueryRowContext(ctx,
		albumSelect+" WHERE artist_id=? AND LOWER(title)=LOWER(?) LIMIT 1",
		artistID, title)
	al, err := s.scanAlbum(row)
	if err != nil {
		s.log.Error("getOrCreateAlbum: exact match scan failed", "error", err, "component", "lib_store")
		return 0, err
	}
	if al != nil {
		if al.Year == 0 && year != 0 {
			al.Year = year
			if _, err := s.UpsertAlbum(ctx, al); err != nil {
				s.log.Error("update album year failed", "title", title, "error", err, "component", "lib_store")
			}
		}
		return al.ID, nil
	}

	// Fallback: fuzzy search via LIKE for near matches.
	albums, err := s.SearchAlbums(ctx, title, 10)
	if err != nil {
		s.log.Error("getOrCreateAlbum: search fallback failed", "error", err, "component", "lib_store")
		return 0, err
	}
	for _, al := range albums {
		if strings.EqualFold(al.Title, title) && al.ArtistID == artistID {
			if al.Year == 0 && year != 0 {
				al.Year = year
				if _, err := s.UpsertAlbum(ctx, &al); err != nil {
					s.log.Error("update album year failed", "title", title, "error", err, "component", "lib_store")
				}
			}
			return al.ID, nil
		}
	}

	return s.UpsertAlbum(ctx, &domain.Album{
		ArtistID:  artistID,
		Title:     title,
		Year:      year,
		Genres:    genres,
		AlbumType: domain.AlbumTypeAlbum,
	})
}

// ─── External ID lookups ─────────────────────────────────────────────

func (s *Store) GetArtistByExternalID(ctx context.Context, service, externalID string) (*domain.Artist, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, genres, summary, thumb_url,
			external_ids, created_at, updated_at,
			COALESCE((SELECT al.id FROM albums al WHERE al.artist_id = artists.id ORDER BY al.year, al.title LIMIT 1), 0)
		FROM artists WHERE json_extract(external_ids, ?) = ?`,
		"$."+service, externalID)
	return s.scanArtist(row)
}

func (s *Store) GetAlbumByExternalID(ctx context.Context, service, externalID string) (*domain.Album, error) {
	row := s.db.QueryRowContext(ctx,
		albumSelect+" WHERE json_extract(external_ids, ?) = ?",
		"$."+service, externalID)
	return s.scanAlbum(row)
}

func (s *Store) GetTrackByExternalID(ctx context.Context, service, externalID string) (*domain.Track, error) {
	row := s.db.QueryRowContext(ctx,
		trackSelect+" WHERE json_extract(external_ids, ?) = ?",
		"$."+service, externalID)
	return s.scanTrack(row)
}

// ─── Internal scan helpers ───────────────────────────────────────────

const albumSelect = `SELECT id, artist_id, title, year, genres, track_count, duration, thumb_url,
	album_type, release_date, external_ids, created_at, updated_at
	FROM albums`

const trackSelect = `SELECT id, album_id, artist_id, title, track_number, disc_number,
	duration, file_path, bitrate, file_size,
	external_ids, acoustid, isrc, quality_profile_id, created_at, updated_at
	FROM tracks`

func (s *Store) scanArtist(row *sql.Row) (*domain.Artist, error) {
	var a domain.Artist
	var genresJSON, extIDsJSON, createdAt, updatedAt string
	err := row.Scan(&a.ID, &a.Name, &genresJSON, &a.Summary, &a.ThumbURL,
		&extIDsJSON, &createdAt, &updatedAt, &a.FirstAlbumID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		s.log.Error("scan artist failed", "error", err, "component", "lib_store")
		return nil, err
	}
	json.Unmarshal([]byte(genresJSON), &a.Genres)
	if extIDsJSON != "" {
		json.Unmarshal([]byte(extIDsJSON), &a.ExternalIDs)
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	a.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &a, nil
}

func (s *Store) scanArtists(rows *sql.Rows) ([]domain.Artist, error) {
	var artists []domain.Artist
	for rows.Next() {
		var a domain.Artist
		var genresJSON, extIDsJSON, createdAt, updatedAt string
		if err := rows.Scan(&a.ID, &a.Name, &genresJSON, &a.Summary, &a.ThumbURL,
			&extIDsJSON, &createdAt, &updatedAt, &a.FirstAlbumID); err != nil {
			s.log.Error("scan artists failed", "error", err, "component", "lib_store")
			return nil, err
		}
		json.Unmarshal([]byte(genresJSON), &a.Genres)
		if extIDsJSON != "" {
			json.Unmarshal([]byte(extIDsJSON), &a.ExternalIDs)
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		a.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		artists = append(artists, a)
	}
	return artists, rows.Err()
}

func (s *Store) scanAlbum(row *sql.Row) (*domain.Album, error) {
	var a domain.Album
	var genresJSON, extIDsJSON, createdAt, updatedAt, albumType string
	err := row.Scan(&a.ID, &a.ArtistID, &a.Title, &a.Year, &genresJSON, &a.TrackCount,
		&a.Duration, &a.ThumbURL, &albumType, &a.ReleaseDate,
		&extIDsJSON, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		s.log.Error("scan album failed", "error", err, "component", "lib_store")
		return nil, err
	}
	json.Unmarshal([]byte(genresJSON), &a.Genres)
	if extIDsJSON != "" {
		json.Unmarshal([]byte(extIDsJSON), &a.ExternalIDs)
	}
	a.AlbumType = domain.AlbumType(albumType)
	a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	a.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &a, nil
}

func (s *Store) scanAlbums(rows *sql.Rows) ([]domain.Album, error) {
	var albums []domain.Album
	for rows.Next() {
		var a domain.Album
		var genresJSON, extIDsJSON, createdAt, updatedAt, albumType string
		if err := rows.Scan(&a.ID, &a.ArtistID, &a.Title, &a.Year, &genresJSON,
			&a.TrackCount, &a.Duration, &a.ThumbURL, &albumType, &a.ReleaseDate,
			&extIDsJSON, &createdAt, &updatedAt); err != nil {
			s.log.Error("scan albums failed", "error", err, "component", "lib_store")
			return nil, err
		}
		json.Unmarshal([]byte(genresJSON), &a.Genres)
		if extIDsJSON != "" {
			json.Unmarshal([]byte(extIDsJSON), &a.ExternalIDs)
		}
		a.AlbumType = domain.AlbumType(albumType)
		a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		a.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		albums = append(albums, a)
	}
	return albums, rows.Err()
}

func (s *Store) scanTrack(row *sql.Row) (*domain.Track, error) {
	var t domain.Track
	var extIDsJSON, createdAt, updatedAt string
	err := row.Scan(&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.TrackNumber,
		&t.DiscNumber, &t.Duration, &t.FilePath, &t.Bitrate, &t.FileSize,
		&extIDsJSON, &t.AcoustID, &t.ISRC, &t.QualityProfileID,
		&createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		s.log.Error("scan track failed", "error", err, "component", "lib_store")
		return nil, err
	}
	if extIDsJSON != "" {
		json.Unmarshal([]byte(extIDsJSON), &t.ExternalIDs)
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &t, nil
}

func (s *Store) scanTracks(rows *sql.Rows) ([]domain.Track, error) {
	var tracks []domain.Track
	for rows.Next() {
		var t domain.Track
		var extIDsJSON, createdAt, updatedAt string
		if err := rows.Scan(&t.ID, &t.AlbumID, &t.ArtistID, &t.Title, &t.TrackNumber,
			&t.DiscNumber, &t.Duration, &t.FilePath, &t.Bitrate, &t.FileSize,
			&extIDsJSON, &t.AcoustID, &t.ISRC, &t.QualityProfileID,
			&createdAt, &updatedAt); err != nil {
			s.log.Error("scan tracks failed", "error", err, "component", "lib_store")
			return nil, err
		}
		if extIDsJSON != "" {
			json.Unmarshal([]byte(extIDsJSON), &t.ExternalIDs)
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// ─── Playlist CRUD ────────────────────────────────────────────────────

func (s *Store) UpsertPlaylist(ctx context.Context, p *domain.Playlist) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	autoSync := 0
	if p.AutoSync {
		autoSync = 1
	}
	syncMode := string(p.SyncMode)
	if syncMode == "" {
		syncMode = "mirror"
	}

	if p.ID != 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE playlists SET name=?, description=?, track_count=?, cover_url=?,
			owner_name=?, is_public=?, auto_sync=?, sync_mode=?, synced_at=?, updated_at=?
			WHERE id=?`,
			p.Name, p.Description, p.TrackCount, p.CoverURL,
			p.OwnerName, boolToInt(p.IsPublic), autoSync, syncMode, p.SyncedAt, now, p.ID,
		)
		if err != nil {
			s.log.Error("upsert playlist update failed", "error", err, "component", "lib_store")
		}
		return p.ID, err
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO playlists (source, source_playlist_id, name, description,
			track_count, cover_url, owner_name, is_public, auto_sync, sync_mode, synced_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Source, p.SourcePlaylistID, p.Name, p.Description,
		p.TrackCount, p.CoverURL, p.OwnerName, boolToInt(p.IsPublic), autoSync,
		syncMode, p.SyncedAt, now, now,
	)
	if err != nil {
		s.log.Error("upsert playlist insert failed", "error", err, "component", "lib_store")
		return 0, err
	}

	id, _ := result.LastInsertId()
	if id != 0 {
		return id, nil
	}

	// Duplicate source+playlist ID — return existing.
	existing, err := s.GetPlaylistBySourceID(ctx, p.Source, p.SourcePlaylistID)
	if err != nil || existing == nil {
		if err != nil {
			s.log.Error("upsert playlist getBySourceID failed", "error", err, "component", "lib_store")
		}
		return 0, fmt.Errorf("playlist insert failed: %s/%s", p.Source, p.SourcePlaylistID)
	}
	return existing.ID, nil
}

func (s *Store) GetPlaylist(ctx context.Context, id int64) (*domain.Playlist, error) {
	p := &domain.Playlist{}
	var autoSync int
	var syncMode string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, source, source_playlist_id, name, description, track_count,
			cover_url, owner_name, is_public, auto_sync, sync_mode, synced_at, created_at, updated_at
		FROM playlists WHERE id=?`, id,
	).Scan(&p.ID, &p.Source, &p.SourcePlaylistID, &p.Name, &p.Description,
		&p.TrackCount, &p.CoverURL, &p.OwnerName, &p.IsPublic, &autoSync, &syncMode,
		&p.SyncedAt, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		s.log.Error("get playlist failed", "error", err, "component", "lib_store")
	}
	p.AutoSync = autoSync != 0
	p.SyncMode = domain.SyncMode(syncMode)
	return p, err
}

func (s *Store) GetPlaylistBySourceID(ctx context.Context, source, sourceID string) (*domain.Playlist, error) {
	p := &domain.Playlist{}
	var autoSync int
	var syncMode string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, source, source_playlist_id, name, description, track_count,
			cover_url, owner_name, is_public, auto_sync, sync_mode, synced_at, created_at, updated_at
		FROM playlists WHERE source=? AND source_playlist_id=?`,
		source, sourceID,
	).Scan(&p.ID, &p.Source, &p.SourcePlaylistID, &p.Name, &p.Description,
		&p.TrackCount, &p.CoverURL, &p.OwnerName, &p.IsPublic, &autoSync, &syncMode,
		&p.SyncedAt, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		s.log.Error("get playlist by source ID failed", "error", err, "component", "lib_store")
	}
	p.AutoSync = autoSync != 0
	p.SyncMode = domain.SyncMode(syncMode)
	return p, err
}

func (s *Store) ListPlaylists(ctx context.Context) ([]domain.Playlist, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source, source_playlist_id, name, description, track_count,
			cover_url, owner_name, is_public, auto_sync, sync_mode, synced_at, created_at, updated_at
		FROM playlists ORDER BY created_at DESC`)
	if err != nil {
		s.log.Error("list playlists failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()

	var out []domain.Playlist
	for rows.Next() {
		var p domain.Playlist
		var autoSync int
		var syncMode string
		if err := rows.Scan(&p.ID, &p.Source, &p.SourcePlaylistID, &p.Name, &p.Description,
			&p.TrackCount, &p.CoverURL, &p.OwnerName, &p.IsPublic, &autoSync, &syncMode,
			&p.SyncedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			s.log.Error("list playlists scan failed", "error", err, "component", "lib_store")
			return nil, err
		}
		p.AutoSync = autoSync != 0
		p.SyncMode = domain.SyncMode(syncMode)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePlaylist(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM playlists WHERE id=?`, id)
	if err != nil {
		s.log.Error("delete playlist failed", "error", err, "component", "lib_store")
	}
	return err
}

func (s *Store) UpsertPlaylistTrack(ctx context.Context, t *domain.PlaylistTrack) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO playlist_tracks (playlist_id, position, track_id,
			source_track_id, title, artist, album, duration_ms, isrc, added_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.PlaylistID, t.Position, t.TrackID, t.SourceTrackID,
		t.Title, t.Artist, t.Album, t.DurationMs, t.ISRC, now,
	)
	if err != nil {
		s.log.Error("upsert playlist track failed", "error", err, "component", "lib_store")
	}
	return err
}

func (s *Store) GetPlaylistTracks(ctx context.Context, playlistID int64) ([]domain.PlaylistTrack, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT playlist_id, position, track_id, source_track_id, title, artist,
			album, duration_ms, isrc
		FROM playlist_tracks WHERE playlist_id=? ORDER BY position`, playlistID)
	if err != nil {
		s.log.Error("get playlist tracks failed", "error", err, "component", "lib_store")
		return nil, err
	}
	defer rows.Close()

	var out []domain.PlaylistTrack
	for rows.Next() {
		var t domain.PlaylistTrack
		if err := rows.Scan(&t.PlaylistID, &t.Position, &t.TrackID, &t.SourceTrackID,
			&t.Title, &t.Artist, &t.Album, &t.DurationMs, &t.ISRC); err != nil {
			s.log.Error("get playlist tracks scan failed", "error", err, "component", "lib_store")
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeletePlaylistTracks(ctx context.Context, playlistID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM playlist_tracks WHERE playlist_id=?`, playlistID)
	if err != nil {
		s.log.Error("delete playlist tracks failed", "error", err, "component", "lib_store")
	}
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
