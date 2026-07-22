package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"

	_ "modernc.org/sqlite"
)

// openTestDB creates an in-memory SQLite database with the downloads and
// download_events tables.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1)

	// Enable foreign key enforcement (required for ON DELETE CASCADE).
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		t.Fatalf("pragma foreign_keys: %v", err)
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS downloads (
			id TEXT PRIMARY KEY,
			source_name TEXT NOT NULL DEFAULT '',
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
			playlist_id TEXT NOT NULL DEFAULT '',
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
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatalf("migrate: %v", err)
		}
	}

	t.Cleanup(func() { db.Close() })
	return db
}

func newTestRecord(id, source, filename, display string) *domain.DownloadRecord {
	return &domain.DownloadRecord{
		ID:          id,
		SourceName:  source,
		Filename:    filename,
		DisplayName: display,
	}
}

// ─── CRUD ─────────────────────────────────────────────────────────────

func TestStore_InsertAndGet(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	r := newTestRecord("dl-1", "soulseek", "track.flac", "Artist - Title")
	if err := store.Insert(ctx, r); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Verify state forced to queued.
	if r.State != "" {
		t.Errorf("Insert should not modify caller's record, got state=%q", r.State)
	}

	got, err := store.Get(ctx, "dl-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}
	if got.State != domain.DownloadQueued {
		t.Errorf("state = %q, want queued", got.State)
	}
	if got.SourceName != "soulseek" {
		t.Errorf("source_name = %q, want soulseek", got.SourceName)
	}
	if got.Progress != 0 {
		t.Errorf("progress = %f, want 0", got.Progress)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	got, err := store.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent record")
	}
}

func TestStore_Update(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	// Insert.
	if err := store.Insert(ctx, newTestRecord("dl-1", "deezer", "song.mp3", "Song")); err != nil {
		t.Fatal(err)
	}

	// Update mutable fields.
	update := &domain.DownloadRecord{
		ID:          "dl-1",
		State:       domain.DownloadDownloading,
		Progress:    42.5,
		Size:        1024000,
		Transferred: 512000,
		Speed:       102400,
		FilePath:    "/tmp/song.mp3",
		Error:       "",
	}
	if err := store.Update(ctx, update); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.Get(ctx, "dl-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.DownloadDownloading {
		t.Errorf("state = %q, want downloading", got.State)
	}
	if got.Progress != 42.5 {
		t.Errorf("progress = %f, want 42.5", got.Progress)
	}
	if got.Size != 1024000 {
		t.Errorf("size = %d", got.Size)
	}
	if got.Transferred != 512000 {
		t.Errorf("transferred = %d", got.Transferred)
	}
	if got.Speed != 102400 {
		t.Errorf("speed = %d", got.Speed)
	}
	if got.FilePath != "/tmp/song.mp3" {
		t.Errorf("file_path = %q", got.FilePath)
	}
}

func TestStore_UpdateNotFound(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	err := store.Update(ctx, &domain.DownloadRecord{ID: "no-such-id"})
	if err == nil {
		t.Error("expected error for nonexistent record")
	}
}

func TestStore_List(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	// Insert in order: dl-1, dl-2, dl-3. List should be DESC = dl-3, dl-2, dl-1.
	store.Insert(ctx, newTestRecord("dl-1", "a", "a", "a"))
	store.Insert(ctx, newTestRecord("dl-2", "b", "b", "b"))
	store.Insert(ctx, newTestRecord("dl-3", "c", "c", "c"))

	records, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	if records[0].ID != "dl-3" {
		t.Errorf("first = %q, want dl-3", records[0].ID)
	}
	if records[1].ID != "dl-2" {
		t.Errorf("second = %q, want dl-2", records[1].ID)
	}
	if records[2].ID != "dl-1" {
		t.Errorf("third = %q, want dl-1", records[2].ID)
	}
}

func TestStore_ListEmpty(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	records, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

// ─── Filtering ────────────────────────────────────────────────────────

func TestStore_ListByState(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	store.Insert(ctx, newTestRecord("dl-1", "a", "a", "a"))
	store.Insert(ctx, newTestRecord("dl-2", "b", "b", "b"))

	// Update one to downloading.
	store.Update(ctx, &domain.DownloadRecord{ID: "dl-2", State: domain.DownloadDownloading})

	queued, err := store.ListByState(ctx, domain.DownloadQueued)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].ID != "dl-1" {
		t.Errorf("queued list: got %d records", len(queued))
	}

	downloading, err := store.ListByState(ctx, domain.DownloadDownloading)
	if err != nil {
		t.Fatal(err)
	}
	if len(downloading) != 1 || downloading[0].ID != "dl-2" {
		t.Errorf("downloading list: got %d records", len(downloading))
	}

	// Empty state.
	imported, err := store.ListByState(ctx, domain.DownloadImported)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 0 {
		t.Errorf("imported list: expected 0, got %d", len(imported))
	}
}

func TestStore_ListActive(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	store.Insert(ctx, newTestRecord("dl-1", "a", "a", "a")) // queued
	store.Insert(ctx, newTestRecord("dl-2", "b", "b", "b")) // queued
	store.Insert(ctx, newTestRecord("dl-3", "c", "c", "c")) // queued

	// Make dl-2 imported (terminal), dl-3 failed (terminal).
	store.Update(ctx, &domain.DownloadRecord{ID: "dl-2", State: domain.DownloadImported})
	store.Update(ctx, &domain.DownloadRecord{ID: "dl-3", State: domain.DownloadFailed})

	active, err := store.ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active, got %d", len(active))
	}
	if active[0].ID != "dl-1" {
		t.Errorf("active = %q, want dl-1", active[0].ID)
	}
}

func TestStore_ListByPlaylist(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	// Insert directly into DB to set playlist_id (Insert doesn't set it).
	_, err := db.ExecContext(ctx,
		`INSERT INTO downloads (id, source_name, filename, display_name, state, playlist_id, created_at, updated_at)
		 VALUES ('dl-1', 'deezer', 't1.mp3', 'T1', 'queued', 'pl-abc', datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO downloads (id, source_name, filename, display_name, state, playlist_id, created_at, updated_at)
		 VALUES ('dl-2', 'deezer', 't2.mp3', 'T2', 'queued', 'pl-abc', datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO downloads (id, source_name, filename, display_name, state, playlist_id, created_at, updated_at)
		 VALUES ('dl-3', 'deezer', 't3.mp3', 'T3', 'queued', '', datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}

	records, err := store.ListByPlaylist(ctx, "pl-abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 playlist records, got %d", len(records))
	}

	empty, err := store.ListByPlaylist(ctx, "pl-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 for unknown playlist, got %d", len(empty))
	}
}

// ─── Events ────────────────────────────────────────────────────────────

func TestStore_RecordAndGetEvents(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	store.Insert(ctx, newTestRecord("dl-1", "soulseek", "t.flac", "T"))

	evt := &domain.DownloadEvent{
		DownloadID: "dl-1",
		Type:       domain.EventProgress,
		Payload:    []byte(`{"progress":50}`),
	}
	if err := store.RecordEvent(ctx, evt); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if evt.ID == "" {
		t.Error("expected event ID to be set")
	}

	evt2 := &domain.DownloadEvent{
		DownloadID: "dl-1",
		Type:       domain.EventCompleted,
	}
	if err := store.RecordEvent(ctx, evt2); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	events, err := store.GetEvents(ctx, "dl-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != domain.EventProgress {
		t.Errorf("first event = %q, want progress", events[0].Type)
	}
	if events[1].Type != domain.EventCompleted {
		t.Errorf("second event = %q, want completed", events[1].Type)
	}
}

func TestStore_GetEventsEmpty(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	events, err := store.GetEvents(ctx, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events for nonexistent download, got %d", len(events))
	}
}

// ─── DeleteTerminal ────────────────────────────────────────────────────

func TestStore_DeleteTerminal(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	store.Insert(ctx, newTestRecord("dl-1", "a", "a", "a")) // queued
	store.Insert(ctx, newTestRecord("dl-2", "b", "b", "b")) // queued
	store.Insert(ctx, newTestRecord("dl-3", "c", "c", "c")) // queued

	// Set dl-1 to imported, dl-2 to failed, dl-3 stays queued.
	store.Update(ctx, &domain.DownloadRecord{ID: "dl-1", State: domain.DownloadImported})
	store.Update(ctx, &domain.DownloadRecord{ID: "dl-2", State: domain.DownloadFailed})

	// Add events for dl-1 (should cascade delete).
	store.RecordEvent(ctx, &domain.DownloadEvent{DownloadID: "dl-1", Type: domain.EventCompleted})

	if err := store.DeleteTerminal(ctx); err != nil {
		t.Fatalf("DeleteTerminal: %v", err)
	}

	// dl-3 (queued) should remain.
	remaining, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining, got %d", len(remaining))
	}
	if remaining[0].ID != "dl-3" {
		t.Errorf("remaining = %q, want dl-3", remaining[0].ID)
	}

	// Events for dl-1 should be cascade-deleted.
	events, err := store.GetEvents(ctx, "dl-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events after cascade delete, got %d", len(events))
	}
}

// ─── Concurrent updates ────────────────────────────────────────────────

func TestStore_ConcurrentUpdates(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())
	ctx := context.Background()

	store.Insert(ctx, newTestRecord("dl-1", "soulseek", "t.flac", "T"))

	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(progress float64) {
			defer wg.Done()
			err := store.Update(ctx, &domain.DownloadRecord{
				ID:       "dl-1",
				Progress: progress,
				State:    domain.DownloadDownloading,
			})
			if err != nil {
				errs <- err
			}
		}(float64(i * 10))
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent update error: %v", err)
	}

	// Final record should exist and be in downloading state.
	got, err := store.Get(ctx, "dl-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("record vanished after concurrent updates")
	}
	if got.State != domain.DownloadDownloading {
		t.Errorf("state = %q after concurrent updates", got.State)
	}
}

// ─── Close ─────────────────────────────────────────────────────────────

func TestStore_Close(t *testing.T) {
	db := openTestDB(t)
	store := New(db, slog.Default())

	if err := store.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
