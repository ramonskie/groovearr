package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

// testCompilationTemplate mirrors the config default used across organizer tests.
const testCompilationTemplate = "Various Artists/{album} ({year})/{track:02d}. {artist} - {title}"

func flatTrack(root, name string) string {
	dir := filepath.Join(root, "Flat Artist")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, name)
	os.WriteFile(p, []byte("audio"), 0o644)
	return p
}

func TestOrganizerReportOnly(t *testing.T) {
	root := t.TempDir()
	oldPath := flatTrack(root, "01 - Track.flac")
	os.WriteFile(filepath.Join(filepath.Dir(oldPath), "cover.jpg"), []byte("cover"), 0o644)

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}, tracks: []domain.Track{
		{ID: 1, Title: "Track", TrackNumber: 1, FilePath: oldPath},
	}}
	org := NewOrganizer("{artist}/{album} ({year})/{track:02d} - {title}", testCompilationTemplate, root, store, testLogger())

	res, err := org.Organize(t.Context(), &store.tracks[0], "Flat Artist", "Album", 2020, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.WouldMove {
		t.Errorf("WouldMove = false, want true (dry-run should report the move)")
	}
	if res.Moved {
		t.Error("dry-run must not move")
	}
	// File untouched, DB path untouched.
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("source file gone in dry-run: %v", err)
	}
	if store.tracks[0].FilePath != oldPath {
		t.Errorf("DB path changed in dry-run: %q", store.tracks[0].FilePath)
	}
}

func TestOrganizerMovesTrackAndImages(t *testing.T) {
	root := t.TempDir()
	oldPath := flatTrack(root, "01 - Track.flac")
	oldDir := filepath.Dir(oldPath)
	os.WriteFile(filepath.Join(oldDir, "cover.jpg"), []byte("cover"), 0o644)
	os.WriteFile(filepath.Join(oldDir, "artist.jpg"), []byte("artist"), 0o644)

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}, tracks: []domain.Track{
		{ID: 1, Title: "Track", TrackNumber: 1, FilePath: oldPath},
	}}
	org := NewOrganizer("{artist}/{album} ({year})/{track:02d} - {title}", testCompilationTemplate, root, store, testLogger())

	res, err := org.Organize(t.Context(), &store.tracks[0], "Flat Artist", "Album", 2020, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Moved {
		t.Fatalf("Moved = false (skipped=%q)", res.Skipped)
	}
	want := filepath.Join(root, "Flat Artist", "Album (2020)", "01 - Track.flac")
	if res.To != want {
		t.Errorf("To = %q, want %q", res.To, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("moved file missing: %v", err)
	}
	if _, err := os.Stat(oldPath); err == nil {
		t.Error("source file still present after move")
	}
	// Cover moved into the new album folder.
	if _, err := os.Stat(filepath.Join(root, "Flat Artist", "Album (2020)", "cover.jpg")); err != nil {
		t.Errorf("cover not moved: %v", err)
	}
	// Artist portrait stays put (artist folder didn't change).
	if _, err := os.Stat(filepath.Join(root, "Flat Artist", "artist.jpg")); err != nil {
		t.Errorf("artist image lost: %v", err)
	}
	// DB path updated.
	if store.tracks[0].FilePath != want {
		t.Errorf("DB path = %q, want %q", store.tracks[0].FilePath, want)
	}
}

func TestOrganizerDryRunReportsCollision(t *testing.T) {
	root := t.TempDir()
	oldPath := flatTrack(root, "01 - Track.flac")
	target := filepath.Join(root, "Flat Artist", "Album (2020)", "01 - Track.flac")
	os.MkdirAll(filepath.Dir(target), 0o755)
	os.WriteFile(target, []byte("existing"), 0o644)

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}, tracks: []domain.Track{
		{ID: 1, Title: "Track", TrackNumber: 1, FilePath: oldPath},
	}}
	org := NewOrganizer("{artist}/{album} ({year})/{track:02d} - {title}", testCompilationTemplate, root, store, testLogger())

	// Dry-run must match what a real run would do: a track whose target is
	// already occupied is skipped, not reported as "would move".
	res, err := org.Organize(t.Context(), &store.tracks[0], "Flat Artist", "Album", 2020, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.WouldMove {
		t.Error("WouldMove = true for an already-occupied target; real run would skip it")
	}
	if res.Skipped != "target exists" {
		t.Errorf("Skipped = %q, want %q", res.Skipped, "target exists")
	}
}

func TestOrganizerRollsBackOnDBError(t *testing.T) {
	root := t.TempDir()
	oldPath := flatTrack(root, "01 - Track.flac")
	target := filepath.Join(root, "Flat Artist", "Album (2020)", "01 - Track.flac")

	store := &mockStore{
		artists:   map[string]int64{},
		albums:    map[string]int64{},
		tracks:    []domain.Track{{ID: 1, Title: "Track", TrackNumber: 1, FilePath: oldPath}},
		upsertErr: errors.New("disk full"),
	}
	org := NewOrganizer("{artist}/{album} ({year})/{track:02d} - {title}", testCompilationTemplate, root, store, testLogger())

	if _, err := org.Organize(t.Context(), &store.tracks[0], "Flat Artist", "Album", 2020, "", false); err == nil {
		t.Fatal("expected error when the DB update fails")
	}
	// The file must be back at its original location so the DB and disk agree.
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("file not rolled back to %q: %v", oldPath, err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("file still present at target after rollback")
	}
	if store.tracks[0].FilePath != oldPath {
		t.Errorf("DB path = %q, want %q (unchanged)", store.tracks[0].FilePath, oldPath)
	}
}

func TestOrganizerDoesNotMoveRootCover(t *testing.T) {
	root := t.TempDir()
	// Track loose in the library root: its album dir IS the root.
	oldPath := filepath.Join(root, "01 - Track.flac")
	os.WriteFile(oldPath, []byte("audio"), 0o644)
	os.WriteFile(filepath.Join(root, "cover.jpg"), []byte("root-cover"), 0o644)

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}, tracks: []domain.Track{
		{ID: 1, Title: "Track", TrackNumber: 1, FilePath: oldPath},
	}}
	org := NewOrganizer("{artist}/{album} ({year})/{track:02d} - {title}", testCompilationTemplate, root, store, testLogger())

	if _, err := org.Organize(t.Context(), &store.tracks[0], "Artist", "Album", 2020, "", false); err != nil {
		t.Fatal(err)
	}
	// A stray root cover.jpg must not be dragged into the new album folder.
	if _, err := os.Stat(filepath.Join(root, "cover.jpg")); err != nil {
		t.Errorf("root cover.jpg moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Artist", "Album (2020)", "cover.jpg")); err == nil {
		t.Error("root cover.jpg was copied into the new album folder")
	}
}

func TestOrganizerInPlaceAndCollision(t *testing.T) {
	root := t.TempDir()
	// Already in template layout.
	inPlace := filepath.Join(root, "Artist", "Album (2020)", "01 - Track.flac")
	os.MkdirAll(filepath.Dir(inPlace), 0o755)
	os.WriteFile(inPlace, []byte("audio"), 0o644)

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}, tracks: []domain.Track{
		{ID: 1, Title: "Track", TrackNumber: 1, FilePath: inPlace},
	}}
	org := NewOrganizer("{artist}/{album} ({year})/{track:02d} - {title}", testCompilationTemplate, root, store, testLogger())

	if res, _ := org.Organize(t.Context(), &store.tracks[0], "Artist", "Album", 2020, "", false); res.Moved || res.WouldMove {
		t.Errorf("in-place track should not move (res=%+v)", res)
	}

	// Collision: target already exists.
	oldPath := flatTrack(root, "01 - Track.flac")
	target := filepath.Join(root, "Flat Artist", "Album (2020)", "01 - Track.flac")
	os.MkdirAll(filepath.Dir(target), 0o755)
	os.WriteFile(target, []byte("existing"), 0o644)
	track := domain.Track{ID: 2, Title: "Track", TrackNumber: 1, FilePath: oldPath}

	res, err := org.Organize(t.Context(), &track, "Flat Artist", "Album", 2020, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved {
		t.Error("must not overwrite an existing target")
	}
	if res.Skipped != "target exists" {
		t.Errorf("Skipped = %q, want %q", res.Skipped, "target exists")
	}
	if data, _ := os.ReadFile(target); string(data) != "existing" {
		t.Errorf("existing target was clobbered: %q", data)
	}
}

func TestOrganizerDoesNotMoveRootArtistImage(t *testing.T) {
	root := t.TempDir()
	oldPath := flatTrack(root, "01 - Track.flac")
	os.WriteFile(filepath.Join(root, "artist.jpg"), []byte("root-artist"), 0o644)

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}, tracks: []domain.Track{
		{ID: 1, Title: "Track", TrackNumber: 1, FilePath: oldPath},
	}}
	org := NewOrganizer("{artist}/{album} ({year})/{track:02d} - {title}", testCompilationTemplate, root, store, testLogger())

	if _, err := org.Organize(t.Context(), &store.tracks[0], "Flat Artist", "Album", 2020, "", false); err != nil {
		t.Fatal(err)
	}
	// A flat track's artist dir is the library root — its portrait must not be
	// dragged into the new artist folder.
	if _, err := os.Stat(filepath.Join(root, "artist.jpg")); err != nil {
		t.Errorf("root artist.jpg moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Flat Artist", "artist.jpg")); err == nil {
		t.Error("root artist.jpg was copied into the artist folder")
	}
}
