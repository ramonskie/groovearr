package sqlite

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ramonskie/groovearr/internal/metadata"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewStore(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestStoreCooldownRoundtrip(t *testing.T) {
	s := newTestStore(t)

	if err := s.SetCooldown("spotify", time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("set cooldown: %v", err)
	}
	entries, err := s.LoadCooldowns()
	if err != nil {
		t.Fatalf("load cooldowns: %v", err)
	}
	if len(entries) != 1 || entries[0].Provider != "spotify" {
		t.Fatalf("entries = %+v, want one spotify entry", entries)
	}
	// Upsert overwrites.
	if err := s.SetCooldown("spotify", time.Now().Add(30*time.Minute)); err != nil {
		t.Fatalf("update cooldown: %v", err)
	}
	entries, _ = s.LoadCooldowns()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (upsert, not duplicate)", len(entries))
	}
	if d := time.Until(entries[0].Until); d > 31*time.Minute || d < 29*time.Minute {
		t.Errorf("updated until = %v, want ~30m", d)
	}
}

func TestStoreRateLimitEvents(t *testing.T) {
	s := newTestStore(t)

	if err := s.LogRateLimitEvent("spotify", 2*time.Hour, "server+grace", 90*time.Second); err != nil {
		t.Fatalf("log event: %v", err)
	}
	if err := s.LogRateLimitEvent("deezer", time.Minute, "escalated", 0); err != nil {
		t.Fatalf("log event: %v", err)
	}

	events, err := s.LoadRateLimitEvents(10)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	// Chronological order: spotify first.
	if events[0].Provider != "spotify" || events[1].Provider != "deezer" {
		t.Errorf("order = %s, %s; want spotify, deezer (chronological)", events[0].Provider, events[1].Provider)
	}
	if events[0].Source != "server+grace" || events[0].Duration != 2*time.Hour || events[0].RetryAfter != 90*time.Second {
		t.Errorf("spotify event = %+v", events[0])
	}
	if events[1].Source != "escalated" || events[1].Duration != time.Minute {
		t.Errorf("deezer event = %+v", events[1])
	}

	// LoadRateLimitEvents roundtrip must satisfy the metadata.Store interface.
	var _ metadata.Store = (*Store)(nil)
}

func TestStorePersistenceAcrossInstances(t *testing.T) {
	// Same DB file, two store instances — simulates a restart.
	dbPath := filepath.Join(t.TempDir(), "persist.db")
	open := func() *Store {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { db.Close() })
		s, err := NewStore(db, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		return s
	}

	s1 := open()
	if err := s1.SetCooldown("tidal", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s1.LogRateLimitEvent("tidal", time.Hour, "server", 30*time.Minute); err != nil {
		t.Fatalf("log: %v", err)
	}

	s2 := open()
	entries, err := s2.LoadCooldowns()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 1 || entries[0].Provider != "tidal" {
		t.Fatalf("entries = %+v, want tidal", entries)
	}
	events, err := s2.LoadRateLimitEvents(10)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 1 || events[0].Provider != "tidal" {
		t.Fatalf("events = %+v, want tidal", events)
	}
}

func TestStoreDeleteCooldown(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetCooldown("spotify", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.DeleteCooldown("spotify"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	entries, _ := s.LoadCooldowns()
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none after delete", entries)
	}
}

func TestStorePrune(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "prune.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	s, err := NewStore(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s.SetCooldown("expired", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("set expired: %v", err)
	}
	if err := s.SetCooldown("active", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("set active: %v", err)
	}

	// Reopening runs init → prune: expired rows are dropped.
	s2, err := NewStore(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	entries, _ := s2.LoadCooldowns()
	if len(entries) != 1 || entries[0].Provider != "active" {
		t.Fatalf("entries = %+v, want only active after prune", entries)
	}
	_ = s
}
