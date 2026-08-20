package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/library"
)

// organizeRunnerStore is a minimal store for exercising organizeRunner: it
// pages artists, resolves albums/tracks, and supports in-place UpsertTrack.
type organizeRunnerStore struct {
	library.Store
	artists   []domain.Artist
	albums    map[int64][]domain.Album
	tracks    map[int64][]domain.Track
	allTracks []domain.Track
}

func (s *organizeRunnerStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	if offset >= len(s.artists) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.artists) {
		end = len(s.artists)
	}
	return s.artists[offset:end], nil
}

func (s *organizeRunnerStore) GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error) {
	return s.albums[artistID], nil
}

func (s *organizeRunnerStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	return s.tracks[albumID], nil
}

func (s *organizeRunnerStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	return s.allTracks, nil
}

func (s *organizeRunnerStore) UpsertTrack(ctx context.Context, t *domain.Track) (int64, error) {
	for i := range s.allTracks {
		if s.allTracks[i].ID == t.ID {
			s.allTracks[i] = *t
			return t.ID, nil
		}
	}
	s.allTracks = append(s.allTracks, *t)
	return t.ID, nil
}

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
	s := &Server{cfg: cfg, store: store, log: testAPILogger()}

	var reports []jobs.Report
	collect := func(r jobs.Report) { reports = append(reports, r) }

	// ── Dry run: nothing moves, the report lists what would. ──
	if err := s.organizeRunner(true)(context.Background(), collect); err != nil {
		t.Fatal(err)
	}
	if s.organizeReport == nil {
		t.Fatal("dry run did not persist a report")
	}
	if s.organizeReport.Summary.WouldMove != 2 {
		t.Errorf("dry-run would_move = %d, want 2 (flat + compilation)", s.organizeReport.Summary.WouldMove)
	}
	if s.organizeReport.Summary.InPlace != 1 {
		t.Errorf("dry-run in_place = %d, want 1", s.organizeReport.Summary.InPlace)
	}
	if _, err := os.Stat(flatPath); err != nil {
		t.Errorf("dry run must not move files: %v", err)
	}
	if !hasReason(s.organizeReport.Entries, "would move") {
		t.Error("dry-run report should contain 'would move' entries")
	}

	// ── Repair: files move, DB paths update. ──
	s.setOrganizeReport(nil)
	if err := s.organizeRunner(false)(context.Background(), collect); err != nil {
		t.Fatal(err)
	}
	if s.organizeReport == nil || s.organizeReport.Summary.Moved != 2 {
		t.Fatalf("repair moved = %+v, want 2", s.organizeReport)
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
	s := &Server{cfg: cfg, store: &organizeRunnerStore{}, log: testAPILogger()}

	if err := s.organizeRunner(true)(context.Background(), func(jobs.Report) {}); err != nil {
		t.Fatal(err)
	}
	// An empty library must still replace any stale report, not leave it up.
	if s.organizeReport == nil {
		t.Fatal("empty library run did not persist a report")
	}
	if s.organizeReport.Mode != "dry run" {
		t.Errorf("report mode = %q, want dry run", s.organizeReport.Mode)
	}
	if s.organizeReport.Summary.Errors != 0 {
		t.Errorf("unexpected errors in empty run: %+v", s.organizeReport.Summary)
	}
}

func hasReason(entries []organizeEntry, reason string) bool {
	for _, e := range entries {
		if e.Reason == reason {
			return true
		}
	}
	return false
}

func TestOrganizeReportEndpoint(t *testing.T) {
	s := &Server{}

	// No report yet → null body.
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/organize/report", nil)
	rec := httptest.NewRecorder()
	s.handleOrganizeReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "null\n" && got != "null" {
		t.Errorf("empty report body = %q, want null", got)
	}

	// A stored report is returned as-is.
	rep := &organizeReport{
		Mode:  "dry run",
		RanAt: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
		Summary: organizeSummary{
			Moved: 0, WouldMove: 2, InPlace: 5, Skipped: 1, Errors: 0,
		},
		Entries: []organizeEntry{
			{TrackID: 7, From: "/music/a/01.flac", To: "/music/a/b/01.flac", Reason: "would move"},
		},
	}
	s.setOrganizeReport(rep)

	req = httptest.NewRequest(http.MethodGet, "/api/jobs/organize/report", nil)
	rec = httptest.NewRecorder()
	s.handleOrganizeReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got organizeReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if got.Mode != "dry run" || got.Summary.WouldMove != 2 || len(got.Entries) != 1 {
		t.Errorf("unexpected report round-trip: %+v", got)
	}
}
