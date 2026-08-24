package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
)

func TestOrganizeRunnerDryRunAndRepair(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	flatPath := mk("Flat Artist/01 - Track.flac")
	nestedPath := mk("Nested Artist/Album (2020)/01 - Track.flac")
	vaPath := mk("Various Artists/Best of 90s/01 - 2Pac - California Love.flac")

	cfg, err := config.LoadOrCreate(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Library.LibraryPath = root
		c.Library.FolderTemplate = "{artist}/{album} ({year})/{track:02d} - {title}"
		c.Library.CompilationTemplate = "Various Artists/{album} ({year})/{track:02d}. {artist} - {title}"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store := &organizeRunnerStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Flat Artist"},
			{ID: 2, Name: "Nested Artist"},
			{ID: 3, Name: "2Pac"},
		},
		albums: map[int64][]domain.Album{
			1: {{ID: 11, Title: "Album", Year: 2020}},
			2: {{ID: 12, Title: "Album", Year: 2020}},
			3: {{ID: 13, Title: "Best of 90s", Year: 1995}},
		},
		tracks: map[int64][]domain.Track{
			11: {{ID: 101, Title: "Track", TrackNumber: 1, FilePath: flatPath}},
			12: {{ID: 102, Title: "Track", TrackNumber: 1, FilePath: nestedPath}},
			13: {{ID: 103, Title: "California Love", TrackNumber: 1, FilePath: vaPath}},
		},
		allTracks: []domain.Track{
			{ID: 101, Title: "Track", TrackNumber: 1, FilePath: flatPath},
			{ID: 102, Title: "Track", TrackNumber: 1, FilePath: nestedPath},
			{ID: 103, Title: "California Love", TrackNumber: 1, FilePath: vaPath},
		},
	}
	runners := newRunners(cfg, store)

	var reports []Report
	collect := func(r Report) { reports = append(reports, r) }

	// ── Dry run: nothing moves, the report lists what would. ──
	if err := runners.Organize(true)(context.Background(), collect); err != nil {
		t.Fatal(err)
	}
	rep := runners.OrganizeReport()
	if rep == nil {
		t.Fatal("dry run did not persist a report")
	}
	if rep.Summary.WouldMove != 2 {
		t.Errorf("dry-run would_move = %d, want 2 (flat + compilation)", rep.Summary.WouldMove)
	}
	if rep.Summary.InPlace != 1 {
		t.Errorf("dry-run in_place = %d, want 1", rep.Summary.InPlace)
	}
	if _, err := os.Stat(flatPath); err != nil {
		t.Errorf("dry run must not move files: %v", err)
	}
	if !hasReason(rep.Entries, "would move") {
		t.Error("dry-run report should contain 'would move' entries")
	}

	// ── Repair: files move, DB paths update. ──
	runners.SetOrganizeReport(nil)
	if err := runners.Organize(false)(context.Background(), collect); err != nil {
		t.Fatal(err)
	}
	rep = runners.OrganizeReport()
	if rep == nil || rep.Summary.Moved != 2 {
		t.Fatalf("repair moved = %+v, want 2", rep)
	}
	wantFlat := filepath.Join(root, "Flat Artist", "Album (2020)", "01 - Track.flac")
	wantVA := filepath.Join(root, "Various Artists", "Best of 90s (1995)", "01. 2Pac - California Love.flac")
	if _, err := os.Stat(wantFlat); err != nil {
		t.Errorf("flat track not organized: %v", err)
	}
	if _, err := os.Stat(wantVA); err != nil {
		t.Errorf("compilation track not kept under Various Artists: %v", err)
	}
	if _, err := os.Stat(flatPath); err == nil {
		t.Error("flat source file still present after repair")
	}
	if _, err := os.Stat(filepath.Join(root, "2Pac", "Best of 90s (1995)")); err == nil {
		t.Error("compilation must not land in a bogus 2Pac album folder")
	}
	// Final summary message is surfaced through the job report.
	if msg := reports[len(reports)-1].Message; msg == "" {
		t.Error("final job message missing")
	}
}

func TestOrganizeRunnerEmptyLibraryPersistsReport(t *testing.T) {
	cfg, err := config.LoadOrCreate(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Library.LibraryPath = t.TempDir()
		c.Library.FolderTemplate = "{artist}/{album} ({year})/{track:02d} - {title}"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runners := newRunners(cfg, &organizeRunnerStore{})

	if err := runners.Organize(true)(context.Background(), func(Report) {}); err != nil {
		t.Fatal(err)
	}
	// An empty library must still replace any stale report, not leave it up.
	rep := runners.OrganizeReport()
	if rep == nil {
		t.Fatal("empty library run did not persist a report")
	}
	if rep.Mode != "dry run" {
		t.Errorf("report mode = %q, want dry run", rep.Mode)
	}
	if rep.Summary.Errors != 0 {
		t.Errorf("unexpected errors in empty run: %+v", rep.Summary)
	}
}

func hasReason(entries []OrganizeEntry, reason string) bool {
	for _, e := range entries {
		if e.Reason == reason {
			return true
		}
	}
	return false
}

func TestOrganizeArtistRunnerMovesMergedTracks(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "Removed Artist", "Album (2020)", "01 - Track.flac")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadOrCreate(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Library.LibraryPath = root
		c.Library.FolderTemplate = "{artist}/{album} ({year})/{track:02d} - {title}"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store := &organizeRunnerStore{
		artists: []domain.Artist{{ID: 1, Name: "Keep Artist"}},
		albums:  map[int64][]domain.Album{1: {{ID: 11, Title: "Album", Year: 2020, ArtistID: 1}}},
		tracks: map[int64][]domain.Track{
			11: {{ID: 101, AlbumID: 11, Title: "Track", TrackNumber: 1, FilePath: oldPath}},
		},
	}
	runners := newRunners(cfg, store)

	if err := runners.OrganizeArtist(1)(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("OrganizeArtist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Keep Artist", "Album (2020)", "01 - Track.flac")); err != nil {
		t.Errorf("track not moved into keeper folder: %v", err)
	}
	rep := runners.OrganizeReport()
	if rep == nil {
		t.Fatal("expected merge organize report to be persisted")
	}
	if rep.Mode != "repair (artist)" {
		t.Errorf("report mode = %q, want artist-scoped repair", rep.Mode)
	}
	if rep.Summary.Moved != 1 || rep.Summary.Errors != 0 {
		t.Errorf("report summary = %+v, want moved 1 and no errors", rep.Summary)
	}
}

func TestOrganizeArtistRunnerAlbumMissing(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "Keep Artist", "Album (2020)", "01 - Track.flac")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadOrCreate(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Library.LibraryPath = root
		c.Library.FolderTemplate = "{artist}/{album} ({year})/{track:02d} - {title}"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store := &albumlessStore{organizeRunnerStore: &organizeRunnerStore{
		artists: []domain.Artist{{ID: 1, Name: "Keep Artist"}},
		albums:  map[int64][]domain.Album{11: {{ID: 11, Title: "Album", Year: 2020, ArtistID: 1}}},
		tracks: map[int64][]domain.Track{
			11: {{ID: 101, AlbumID: 11, Title: "Track", TrackNumber: 1, FilePath: oldPath}},
		},
	}}
	runners := newRunners(cfg, store)

	if err := runners.OrganizeArtist(1)(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("OrganizeArtist: %v", err)
	}
	rep := runners.OrganizeReport()
	if rep == nil {
		t.Fatal("expected merge organize report to be persisted so the UI doesn't show stale results")
	}
	if rep.Summary.Errors != 1 {
		t.Errorf("report errors = %d, want 1", rep.Summary.Errors)
	}
}

func TestGroupTracksByAlbum(t *testing.T) {
	tracks := []domain.Track{
		{ID: 1, AlbumID: 10},
		{ID: 2, AlbumID: 10},
		{ID: 3, AlbumID: 20},
		{ID: 4, AlbumID: 10},
		{ID: 5, AlbumID: 30},
	}
	groups := groupTracksByAlbum(tracks)
	if len(groups) != 3 {
		t.Fatalf("want 3 album groups, got %d", len(groups))
	}
	if groups[0].AlbumID != 10 || groups[1].AlbumID != 20 || groups[2].AlbumID != 30 {
		t.Fatalf("unexpected group order: %+v", groups)
	}
	if len(groups[0].Tracks) != 3 {
		t.Fatalf("album 10: want 3 tracks, got %d", len(groups[0].Tracks))
	}
	ids := []int64{groups[0].Tracks[0].ID, groups[0].Tracks[1].ID, groups[0].Tracks[2].ID}
	if ids[0] != 1 || ids[1] != 2 || ids[2] != 4 {
		t.Fatalf("album 10 track order changed: %v", ids)
	}
}

func TestGroupTracksByAlbumEmpty(t *testing.T) {
	if groups := groupTracksByAlbum(nil); len(groups) != 0 {
		t.Fatalf("want no groups for empty input, got %d", len(groups))
	}
}

func TestGroupTracksByAlbumOrphans(t *testing.T) {
	tracks := []domain.Track{
		{ID: 1, AlbumID: 0},
		{ID: 2, AlbumID: 0},
		{ID: 3, AlbumID: 5},
	}
	groups := groupTracksByAlbum(tracks)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups (orphans + album 5), got %d", len(groups))
	}
	if len(groups[0].Tracks) != 2 {
		t.Fatalf("orphans should group together, got %d", len(groups[0].Tracks))
	}
	if groups[1].AlbumID != 5 {
		t.Fatalf("second group should be album 5, got %d", groups[1].AlbumID)
	}
}
