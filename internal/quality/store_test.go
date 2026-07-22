package quality

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE quality_profiles (
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
		`CREATE TABLE tracks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quality_profile_id INTEGER
		)`,
		`CREATE TABLE downloads (
			id TEXT PRIMARY KEY,
			quality_profile_id INTEGER
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func seedProfile(t *testing.T, store *SQLiteProfileStore, name string, isDefault bool) int64 {
	t.Helper()
	ctx := context.Background()
	p := &QualityProfile{
		Name:            name,
		Description:     "test profile",
		RankedTargets:   RankedTargets{{Label: "FLAC", Format: "flac"}},
		FallbackEnabled: true,
		SearchMode:      SearchPriority,
		UpgradePolicy:   UpgradeAcceptable,
		IsDefault:       isDefault,
	}
	id, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("seedProfile %s: %v", name, err)
	}
	return id
}

func TestCreate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	p := &QualityProfile{
		Name:            "Audiophile",
		Description:     "FLAC only",
		RankedTargets:   RankedTargets{{Label: "FLAC", Format: "flac", MinBitDepth: 24}},
		FallbackEnabled: false,
		SearchMode:      SearchBestQuality,
		UpgradePolicy:   UpgradeUntilTop,
		IsDefault:       true,
	}

	id, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected id > 0, got %d", id)
	}
}

func TestGetByID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	id := seedProfile(t, store, "Test Profile", false)

	fetched, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if fetched == nil {
		t.Fatal("GetByID returned nil")
	}
	if fetched.Name != "Test Profile" {
		t.Errorf("expected name 'Test Profile', got %q", fetched.Name)
	}
	if fetched.Description != "test profile" {
		t.Errorf("expected description 'test profile', got %q", fetched.Description)
	}
	if len(fetched.RankedTargets) != 1 {
		t.Errorf("expected 1 ranked target, got %d", len(fetched.RankedTargets))
	}
}

func TestList(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	// Create profiles: second one is default, should appear first.
	seedProfile(t, store, "Profile A", false)
	seedProfile(t, store, "Profile B", true)
	seedProfile(t, store, "Profile C", false)

	profiles, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(profiles) != 3 {
		t.Fatalf("expected 3 profiles, got %d", len(profiles))
	}
	if !profiles[0].IsDefault {
		t.Error("expected default profile first in list")
	}
}

func TestUpdate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	id := seedProfile(t, store, "Original", false)

	// Fetch, update name, save.
	fetched, _ := store.GetByID(ctx, id)
	fetched.Name = "Updated"
	fetched.Description = "updated desc"
	if err := store.Update(ctx, fetched); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	updated, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if updated.Name != "Updated" {
		t.Errorf("expected name 'Updated', got %q", updated.Name)
	}
	if updated.Description != "updated desc" {
		t.Errorf("expected description 'updated desc', got %q", updated.Description)
	}
}

func TestDelete(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	id := seedProfile(t, store, "ToDelete", false)

	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	fetched, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	}
	if fetched != nil {
		t.Error("expected nil after delete")
	}
}

func TestSetDefault(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	id1 := seedProfile(t, store, "First", true)
	id2 := seedProfile(t, store, "Second", false)

	// Set second as default.
	if err := store.SetDefault(ctx, id2); err != nil {
		t.Fatalf("SetDefault failed: %v", err)
	}

	p1, _ := store.GetByID(ctx, id1)
	p2, _ := store.GetByID(ctx, id2)

	if p1.IsDefault {
		t.Error("expected p1 to no longer be default")
	}
	if !p2.IsDefault {
		t.Error("expected p2 to be default")
	}
}

func TestLoadProfileByID_Nil(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	seedProfile(t, store, "Default", true)
	seedProfile(t, store, "Other", false)

	// nil id should resolve to default.
	p, err := store.LoadProfileByID(ctx, nil)
	if err != nil {
		t.Fatalf("LoadProfileByID(nil) failed: %v", err)
	}
	if p.Name != "Default" {
		t.Errorf("expected 'Default', got %q", p.Name)
	}
}

func TestLoadProfileByID_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	seedProfile(t, store, "Default", true)

	// Non-existent id should fall back to default.
	nonExistent := int64(9999)
	p, err := store.LoadProfileByID(ctx, &nonExistent)
	if err != nil {
		t.Fatalf("LoadProfileByID(9999) failed: %v", err)
	}
	if p.Name != "Default" {
		t.Errorf("expected fallback to 'Default', got %q", p.Name)
	}
}

func TestLoadProfileByID_ExactMatch(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	seedProfile(t, store, "Default", true)
	id2 := seedProfile(t, store, "Specific", false)

	p, err := store.LoadProfileByID(ctx, &id2)
	if err != nil {
		t.Fatalf("LoadProfileByID(%d) failed: %v", id2, err)
	}
	if p.Name != "Specific" {
		t.Errorf("expected 'Specific', got %q", p.Name)
	}
}

func TestLoadProfileByID_NoProfiles(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	// No profiles exist — nil should error.
	_, err := store.LoadProfileByID(ctx, nil)
	if err == nil {
		t.Error("expected error when no profiles exist")
	}
}

func TestCreate_EmptyRankedTargets(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteProfileStore(db)
	ctx := context.Background()

	p := &QualityProfile{
		Name: "Empty Targets",
	}
	id, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create with empty targets: %v", err)
	}

	fetched, _ := store.GetByID(ctx, id)
	if fetched.RankedTargets == nil {
		t.Error("expected non-nil RankedTargets after fetch")
	}
}
