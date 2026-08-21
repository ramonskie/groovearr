package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/sse"
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

func (s *organizeRunnerStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	for i := range s.artists {
		if s.artists[i].ID == id {
			return &s.artists[i], nil
		}
	}
	return nil, nil
}

func (s *organizeRunnerStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	var out []domain.Track
	for _, albums := range s.albums {
		for _, a := range albums {
			if a.ArtistID == artistID {
				out = append(out, s.tracks[a.ID]...)
			}
		}
	}
	return out, nil
}

func (s *organizeRunnerStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	for _, albums := range s.albums {
		for _, a := range albums {
			if a.ID == id {
				cp := a
				return &cp, nil
			}
		}
	}
	return nil, nil
}

// albumlessStore is organizeRunnerStore with GetAlbum stubbed to always miss,
// exercising the runner's album-lookup failure path.
type albumlessStore struct {
	*organizeRunnerStore
}

func (*albumlessStore) GetAlbum(context.Context, int64) (*domain.Album, error) {
	return nil, nil
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

func TestDuplicatesRunnerPersistsCanonicals(t *testing.T) {
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en De Munnik"},
			{ID: 2, Name: "Acda en de Munnik"},
			{ID: 3, Name: "Aaliyah"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
		},
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(&stubNameProvider{name: "musicbrainz", names: map[string]string{
		"acdaendemunnik": "Acda en de Munnik",
	}})
	s := &Server{store: store, mdRegistry: reg, log: testAPILogger()}

	var reports []jobs.Report
	if err := s.duplicatesRunner(context.Background(), func(r jobs.Report) { reports = append(reports, r) }); err != nil {
		t.Fatalf("duplicatesRunner: %v", err)
	}

	if len(store.scan) != 1 || store.scan["acda en de munnik"] != "Acda en de Munnik" {
		t.Errorf("scan = %v, want only the duplicate group with the resolved canonical", store.scan)
	}
	if len(reports) == 0 {
		t.Error("expected progress reports")
	}
}

func TestDuplicatesRunnerNoDuplicates(t *testing.T) {
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Aaliyah"},
			{ID: 2, Name: "Radiohead"},
		},
		scan: map[string]string{"stale group": "Stale"},
	}
	s := &Server{store: store, log: testAPILogger()}

	if err := s.duplicatesRunner(context.Background(), func(jobs.Report) {}); err != nil {
		t.Fatalf("duplicatesRunner: %v", err)
	}
	if len(store.scan) != 0 {
		t.Errorf("expected empty scan for no duplicates, got %v", store.scan)
	}
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
	s := &Server{cfg: cfg, store: store, log: testAPILogger()}

	if err := s.organizeArtistRunner(1)(context.Background(), func(jobs.Report) {}); err != nil {
		t.Fatalf("organizeArtistRunner: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Keep Artist", "Album (2020)", "01 - Track.flac")); err != nil {
		t.Errorf("track not moved into keeper folder: %v", err)
	}
	if s.organizeReport == nil {
		t.Fatal("expected merge organize report to be persisted")
	}
	if s.organizeReport.Mode != "repair (artist)" {
		t.Errorf("report mode = %q, want artist-scoped repair", s.organizeReport.Mode)
	}
	if s.organizeReport.Summary.Moved != 1 || s.organizeReport.Summary.Errors != 0 {
		t.Errorf("report summary = %+v, want moved 1 and no errors", s.organizeReport.Summary)
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

	// The track references an album that's gone missing (e.g. a merged album
	// whose rows are still mid-write). The runner must count it as an error,
	// persist a report, and keep going rather than crash.
	store := &albumlessStore{organizeRunnerStore: &organizeRunnerStore{
		artists: []domain.Artist{{ID: 1, Name: "Keep Artist"}},
		albums:  map[int64][]domain.Album{11: {{ID: 11, Title: "Album", Year: 2020, ArtistID: 1}}},
		tracks: map[int64][]domain.Track{
			11: {{ID: 101, AlbumID: 11, Title: "Track", TrackNumber: 1, FilePath: oldPath}},
		},
	}}
	s := &Server{cfg: cfg, store: store, log: testAPILogger()}

	if err := s.organizeArtistRunner(1)(context.Background(), func(jobs.Report) {}); err != nil {
		t.Fatalf("organizeArtistRunner: %v", err)
	}
	if s.organizeReport == nil {
		t.Fatal("expected merge organize report to be persisted so the UI doesn't show stale results")
	}
	if s.organizeReport.Summary.Errors != 1 {
		t.Errorf("report errors = %d, want 1", s.organizeReport.Summary.Errors)
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
	// First-seen (rowid) order preserved: album 10, then 20, then 30.
	if groups[0].AlbumID != 10 || groups[1].AlbumID != 20 || groups[2].AlbumID != 30 {
		t.Fatalf("unexpected group order: %+v", groups)
	}
	// Same-album tracks stay contiguous and in original order.
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
	// Tracks with AlbumID 0 form a single group, owned by one worker (the
	// same tradeoff as a large album: one worker churns the group while the
	// rest of the pool picks up other albums).
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

func TestEnrichOutcomeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want enrichOutcome
	}{
		{"completed", nil, enrichOutcomeCompleted},
		{"deadline", context.DeadlineExceeded, enrichOutcomeTimeout},
		{"wrapped deadline", errors.Join(context.DeadlineExceeded, errors.New("x")), enrichOutcomeTimeout},
		{"generic", errors.New("boom"), enrichOutcomeFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := enrichOutcomeFor(c.err); got != c.want {
				t.Fatalf("enrichOutcomeFor(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

func TestEnrichActivityRingBuffer(t *testing.T) {
	s := &Server{}
	for i := 0; i < enrichActivityMax+50; i++ {
		s.recordEnrichActivity(enrichActivity{
			At:      time.Now().UTC(),
			TrackID: int64(i),
		})
	}
	s.enrichMu.Lock()
	n := len(s.enrichActivity)
	first := s.enrichActivity[0].TrackID
	last := s.enrichActivity[n-1].TrackID
	s.enrichMu.Unlock()
	if n != enrichActivityMax {
		t.Fatalf("ring buffer: want %d entries, got %d", enrichActivityMax, n)
	}
	if first != 50 {
		t.Fatalf("ring buffer should have dropped the oldest 50, first track = %d", first)
	}
	if last != enrichActivityMax+49 {
		t.Fatalf("ring buffer should keep the newest entry, last track = %d", last)
	}
}

func waitJobState(t *testing.T, s *Server, want jobs.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j := s.jobs.Current()
		if j != nil && j.State == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not reach %s, last state: %+v", want, j)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func activityResp(t *testing.T, s *Server) (job *jobs.Job, activity []enrichActivity) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/activity", nil)
	rec := httptest.NewRecorder()
	s.handleJobActivity(rec, req)
	var resp struct {
		Job      *jobs.Job        `json:"job"`
		Activity []enrichActivity `json:"activity"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode activity response: %v", err)
	}
	return resp.Job, resp.Activity
}

func TestHandleJobActivityScopedToEnrichRun(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := context.Background()

	// Enrich run: a stale entry recorded before the job started must be
	// filtered out; the in-run entry must be returned.
	s := &Server{jobs: jobs.NewManager(sse.NewSSEHub(logger), base, logger)}
	s.recordEnrichActivity(enrichActivity{At: time.Now().UTC().Add(-time.Hour), TrackID: 1, Title: "stale"})
	if _, err := s.jobs.Start("enrich", func(ctx context.Context, report func(jobs.Report)) error {
		s.recordEnrichActivity(enrichActivity{At: time.Now().UTC(), TrackID: 2, Title: "current"})
		return nil
	}); err != nil {
		t.Fatalf("start enrich job: %v", err)
	}
	waitJobState(t, s, jobs.StateCompleted)

	job, activity := activityResp(t, s)
	if job == nil || job.Type != "enrich" {
		t.Fatalf("want enrich job, got %+v", job)
	}
	if len(activity) != 1 {
		t.Fatalf("want 1 in-run activity entry, got %d: %+v", len(activity), activity)
	}
	if activity[0].TrackID != 2 {
		t.Fatalf("want in-run entry track 2, got %d", activity[0].TrackID)
	}

	// A non-enrich job must not expose the enrich buffer at all.
	s2 := &Server{jobs: jobs.NewManager(sse.NewSSEHub(logger), base, logger)}
	s2.recordEnrichActivity(enrichActivity{At: time.Now().UTC(), TrackID: 9, Title: "unused"})
	if _, err := s2.jobs.Start("scan", func(ctx context.Context, report func(jobs.Report)) error {
		return nil
	}); err != nil {
		t.Fatalf("start scan job: %v", err)
	}
	waitJobState(t, s2, jobs.StateCompleted)

	job2, activity2 := activityResp(t, s2)
	if job2 == nil || job2.Type != "scan" {
		t.Fatalf("want scan job, got %+v", job2)
	}
	if len(activity2) != 0 {
		t.Fatalf("scan job must not expose enrich activity, got %d entries", len(activity2))
	}
}

func TestHandleJobActivityNoJob(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{jobs: jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger)}
	// A seeded buffer is irrelevant when no job has ever run.
	s.recordEnrichActivity(enrichActivity{At: time.Now().UTC(), TrackID: 7})
	job, activity := activityResp(t, s)
	if job != nil {
		t.Fatalf("want nil job, got %+v", job)
	}
	if len(activity) != 0 {
		t.Fatalf("want empty activity with no job, got %d entries", len(activity))
	}
}
