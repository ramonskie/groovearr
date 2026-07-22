package download

import (
	"context"
	"database/sql"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/quality"

	_ "modernc.org/sqlite"
)

// mockLibraryReader implements LibraryTrackReader for testing.
type mockLibraryReader struct {
	tracks []domain.Track
}

func (m *mockLibraryReader) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	return m.tracks, nil
}

func TestUpgradeScanner_ScanLibrary_IdentifiesBelowCutoff(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Create profiles table.
	_, err = db.Exec(`CREATE TABLE quality_profiles (
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
	)`)
	if err != nil {
		t.Fatal(err)
	}

	store := quality.NewSQLiteProfileStore(db)

	// Create a profile: FLAC (top) → MP3 320 (cutoff), until_cutoff=0 (FLAC only acceptable).
	prof := &quality.QualityProfile{
		Name: "Hi-Fi",
		RankedTargets: quality.RankedTargets{
			{Label: "FLAC", Format: "flac"},
			{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
		},
		FallbackEnabled:    false,
		UpgradePolicy:      quality.UpgradeAcceptable,
		UpgradeCutoffIndex: 0,
	}
	profID, err := store.Create(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}

	// mock tracks: one FLAC (OK), one MP3 128 (below cutoff)
	reader := &mockLibraryReader{
		tracks: []domain.Track{
			{
				ID:               1,
				Title:            "Song FLAC",
				FilePath:         "/music/Artist A/Song.flac",
				Bitrate:          1411,
				QualityProfileID: intPtr(profID),
			},
			{
				ID:               2,
				Title:            "Song MP3",
				FilePath:         "/music/Artist B/Song.mp3",
				Bitrate:          128,
				QualityProfileID: intPtr(profID),
			},
		},
	}

	scanner := NewUpgradeScanner(nil)
	candidates, err := scanner.ScanLibrary(context.Background(), store, reader)
	if err != nil {
		t.Fatal(err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate below cutoff, got %d: %+v", len(candidates), candidates)
	}
	if candidates[0].TrackID != 2 {
		t.Errorf("expected track 2 (MP3 128) to need upgrade, got track %d", candidates[0].TrackID)
	}
	if candidates[0].CurrentFormat != "mp3" {
		t.Errorf("expected mp3, got %s", candidates[0].CurrentFormat)
	}
}

func TestUpgradeScanner_ScanLibrary_AllAcceptable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE quality_profiles (
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
	)`)
	if err != nil {
		t.Fatal(err)
	}

	store := quality.NewSQLiteProfileStore(db)

	// Acceptable policy: any match is fine.
	prof := &quality.QualityProfile{
		Name: "Standard",
		RankedTargets: quality.RankedTargets{
			{Label: "FLAC", Format: "flac"},
			{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
		},
		FallbackEnabled:    true,
		UpgradePolicy:      quality.UpgradeAcceptable,
		UpgradeCutoffIndex: 0,
	}
	profID, err := store.Create(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}

	reader := &mockLibraryReader{
		tracks: []domain.Track{
			{
				ID:               1,
				Title:            "Song FLAC",
				FilePath:         "/music/Artist A/Song.flac",
				Bitrate:          1411,
				QualityProfileID: intPtr(profID),
			},
			{
				ID:               2,
				Title:            "Song MP3 320",
				FilePath:         "/music/Artist B/Song.mp3",
				Bitrate:          320,
				QualityProfileID: intPtr(profID),
			},
		},
	}

	scanner := NewUpgradeScanner(nil)
	candidates, err := scanner.ScanLibrary(context.Background(), store, reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates (all acceptable), got %d", len(candidates))
	}
}

func intPtr(i int64) *int64 {
	return &i
}
