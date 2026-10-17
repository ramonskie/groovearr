package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
)

func TestScanPathPopulatesDuration(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(albumDir, "01 - Track.wav")
	writeTestWAV(t, path, time.Second)

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}, thumbs: map[int64]string{}}
	scanner := NewScanner(store, testLogger())
	scanner.probeDuration = func(context.Context, string) (time.Duration, error) { return time.Second, nil }

	stats, err := scanner.ScanPath(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanPath: %v", err)
	}
	if stats.Imported != 1 || len(store.tracks) != 1 {
		t.Fatalf("imported=%d tracks=%d, want 1/1", stats.Imported, len(store.tracks))
	}
	assertNearSecond(t, store.tracks[0].Duration)
}

func TestScanPathImportsWithoutFFprobe(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(albumDir, "01 - Track.wav")
	writeTestWAV(t, path, time.Second)

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}, thumbs: map[int64]string{}}
	scanner := NewScanner(store, testLogger())
	scanner.probeDuration = func(context.Context, string) (time.Duration, error) {
		return 0, ErrDurationUnavailable
	}

	stats, err := scanner.ScanPath(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanPath: %v", err)
	}
	if stats.Imported != 1 || len(store.tracks) != 1 {
		t.Fatalf("imported=%d tracks=%d, want 1/1 even without ffprobe", stats.Imported, len(store.tracks))
	}
	if store.tracks[0].Duration != 0 {
		t.Fatalf("duration = %d, want 0 when ffprobe is missing", store.tracks[0].Duration)
	}
}

func TestScanPathBackfillsExistingDuration(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(albumDir, "01 - Track.wav")
	writeTestWAV(t, path, time.Second)

	store := &mockStore{
		artists: map[string]int64{},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
		tracks:  []domain.Track{{ID: 1, FilePath: path, Title: "Track"}},
	}
	scanner := NewScanner(store, testLogger())
	scanner.probeDuration = func(context.Context, string) (time.Duration, error) { return time.Second, nil }

	if _, err := scanner.ScanPath(context.Background(), root); err != nil {
		t.Fatalf("ScanPath: %v", err)
	}
	if len(store.tracks) != 1 {
		t.Fatalf("tracks = %d, want the existing row only", len(store.tracks))
	}
	if store.durationCalls != 1 || store.upsertCalls != 0 {
		t.Fatalf("duration calls=%d upsert calls=%d, want 1/0 (prefer duration-only update)",
			store.durationCalls, store.upsertCalls)
	}
	assertNearSecond(t, store.tracks[0].Duration)
}

func TestScanPathSkipsProbeWhenDurationSet(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(albumDir, "01 - Track.wav")
	writeTestWAV(t, path, time.Second)

	store := &mockStore{
		artists: map[string]int64{},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
		tracks:  []domain.Track{{ID: 1, FilePath: path, Title: "Track", Duration: 1234}},
	}
	scanner := NewScanner(store, testLogger())
	calls := 0
	scanner.probeDuration = func(context.Context, string) (time.Duration, error) {
		calls++
		return time.Second, nil
	}

	if _, err := scanner.ScanPath(context.Background(), root); err != nil {
		t.Fatalf("ScanPath: %v", err)
	}
	if calls != 0 {
		t.Fatalf("probe calls = %d, want 0 for a track that already has duration", calls)
	}
	if store.durationCalls != 0 || store.upsertCalls != 0 {
		t.Fatalf("writes = duration:%d upsert:%d, want none", store.durationCalls, store.upsertCalls)
	}
	if store.tracks[0].Duration != 1234 {
		t.Fatalf("duration = %d, want the existing 1234ms", store.tracks[0].Duration)
	}
}

func TestScanPathBackfillStoreErrorIsNonFatal(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(albumDir, "01 - Track.wav")
	writeTestWAV(t, path, time.Second)

	store := &mockStore{
		artists:     map[string]int64{},
		albums:      map[string]int64{},
		thumbs:      map[int64]string{},
		tracks:      []domain.Track{{ID: 1, FilePath: path, Title: "Track"}},
		durationErr: errors.New("db down"),
	}
	scanner := NewScanner(store, testLogger())
	scanner.probeDuration = func(context.Context, string) (time.Duration, error) { return time.Second, nil }

	stats, err := scanner.ScanPath(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanPath: %v", err)
	}
	if stats.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", stats.Skipped)
	}
	if store.durationCalls != 1 {
		t.Fatalf("duration calls = %d, want 1 preferred duration-only attempt", store.durationCalls)
	}
	if store.tracks[0].Duration != 0 {
		t.Fatalf("duration = %d, want 0 after a failed backfill", store.tracks[0].Duration)
	}
}

func assertNearSecond(t *testing.T, ms int64) {
	t.Helper()
	if ms < 950 || ms > 1050 {
		t.Fatalf("duration = %dms, want ~1000ms", ms)
	}
}
