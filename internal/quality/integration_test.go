package quality

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestProfileCRUD_Integration(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Create table (schema matches library/sqlite store init).
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

	// Create referenced tables needed for Delete cascade.
	_, err = db.Exec(`CREATE TABLE tracks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		quality_profile_id INTEGER REFERENCES quality_profiles(id)
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE downloads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		quality_profile_id INTEGER REFERENCES quality_profiles(id)
	)`)
	if err != nil {
		t.Fatal(err)
	}

	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	// ── Create profile ────────────────────────────────────────────────
	p := &QualityProfile{
		Name:        "Test Profile",
		Description: "Integration test",
		RankedTargets: RankedTargets{
			{Label: "FLAC", Format: "flac"},
			{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
		},
		FallbackEnabled: true,
		UpgradePolicy:   UpgradeAcceptable,
	}

	id, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != 1 {
		t.Errorf("expected id 1, got %d", id)
	}

	// ── Set as default ────────────────────────────────────────────────
	err = store.SetDefault(ctx, id)
	if err != nil {
		t.Fatalf("set default: %v", err)
	}

	// ── Verify default flag persisted ─────────────────────────────────
	loaded, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if !loaded.IsDefault {
		t.Error("expected is_default=true")
	}

	// ── LoadProfileByID(nil) → default ────────────────────────────────
	def, err := store.LoadProfileByID(ctx, nil)
	if err != nil {
		t.Fatalf("load default: %v", err)
	}
	if def.ID != id {
		t.Errorf("expected default id %d, got %d", id, def.ID)
	}

	// ── List ──────────────────────────────────────────────────────────
	profiles, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("expected 1 profile, got %d", len(profiles))
	}

	// ── Update ────────────────────────────────────────────────────────
	p.ID = id
	p.Description = "Updated description"
	err = store.Update(ctx, p)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	updated, _ := store.GetByID(ctx, id)
	if updated.Description != "Updated description" {
		t.Error("update didn't persist")
	}

	// ── Delete ────────────────────────────────────────────────────────
	err = store.Delete(ctx, id)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	p, _ = store.GetByID(ctx, id)
	if p != nil {
		t.Error("expected nil profile after delete")
	}
}

func TestFilterAndRank_EndToEnd(t *testing.T) {
	profile := QualityProfile{
		Name: "Test",
		RankedTargets: RankedTargets{
			{Label: "FLAC", Format: "flac"},
			{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
		},
		FallbackEnabled: true,
	}

	candidates := []AudioQuality{
		{Format: "mp3", Bitrate: 128},
		{Format: "flac", Bitrate: 1411, SampleRate: 44100, BitDepth: 16},
		{Format: "mp3", Bitrate: 320},
	}

	result := FilterByProfile(candidates, profile)
	if len(result) != 1 {
		t.Fatalf("expected 1 result in best group, got %d: %+v", len(result), result)
	}
	if result[0].Quality.Format != "flac" {
		t.Errorf("expected FLAC best, got %s", result[0].Quality.Format)
	}

	// ── Verify fallback behavior ──────────────────────────────────────
	profileNoFallback := QualityProfile{
		Name: "NoFallback",
		RankedTargets: RankedTargets{
			{Label: "FLAC", Format: "flac"},
		},
		FallbackEnabled: false,
	}

	lowQuality := []AudioQuality{
		{Format: "mp3", Bitrate: 128},
		{Format: "mp3", Bitrate: 64},
	}
	noMatch := FilterByProfile(lowQuality, profileNoFallback)
	if len(noMatch) != 0 {
		t.Errorf("expected 0 results with no match + no fallback, got %d", len(noMatch))
	}
}

func TestFilterAndRank_EmptyInput(t *testing.T) {
	result := FilterAndRank(nil, nil, true)
	if result != nil {
		t.Error("expected nil for empty input")
	}
}

func TestFilterAndRank_NoTargets(t *testing.T) {
	candidates := []AudioQuality{
		{Format: "mp3", Bitrate: 320},
	}
	result := FilterAndRank(candidates, nil, true)
	if len(result) != 1 {
		t.Fatalf("expected 1 result with no targets + fallback, got %d", len(result))
	}
	if result[0].Quality.Format != "mp3" {
		t.Errorf("expected mp3, got %s", result[0].Quality.Format)
	}
}
