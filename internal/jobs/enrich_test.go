package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// scanStore is a minimal store for exercising the Scan runner through a real
// library.Scanner. It embeds the nil interface and overrides only the methods
// the scanner calls while walking and importing.
type enrichStore struct {
	library.Store
	track  *domain.Track
	artist *domain.Artist
	album  *domain.Album
}

func (s *enrichStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	if s.track == nil {
		return nil, nil
	}
	return []domain.Track{*s.track}, nil
}
func (s *enrichStore) GetTrack(ctx context.Context, id int64) (*domain.Track, error) {
	if s.track == nil || s.track.ID != id {
		return nil, nil
	}
	return s.track, nil
}
func (s *enrichStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	if s.artist == nil || s.artist.ID != id {
		return nil, nil
	}
	return s.artist, nil
}
func (s *enrichStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	if s.album == nil || s.album.ID != id {
		return nil, nil
	}
	return s.album, nil
}
func (s *enrichStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	return []domain.Track{*s.track}, nil
}
func (s *enrichStore) UpsertTrack(ctx context.Context, track *domain.Track) (int64, error) {
	s.track = track
	return track.ID, nil
}
func (s *enrichStore) UpsertAlbum(ctx context.Context, album *domain.Album) (int64, error) {
	s.album = album
	return album.ID, nil
}
func (s *enrichStore) SetArtistThumbURL(ctx context.Context, artistID int64, thumbURL string) error {
	return nil
}

// TestEnrichRunnerProcessesAndRecordsActivity exercises the Enrich runner with
// a real enrichment handler backed by an empty provider registry: the track is
// processed (registry empty → no provider fills anything), and an activity
// entry with a terminal outcome is recorded for the ring buffer.
func TestEnrichRunnerProcessesAndRecordsActivity(t *testing.T) {
	store := &enrichStore{
		track: &domain.Track{
			ID: 1, AlbumID: 10, ArtistID: 100,
			Title: "Test Track", FilePath: "/music/Artist/Album/01 - Track.flac",
		},
		artist: &domain.Artist{ID: 100, Name: "Test Artist"},
		album:  &domain.Album{ID: 10, ArtistID: 100, Title: "Test Album"},
	}
	reg := metadata.NewRegistry()
	handler := download.NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	runners := NewRunners(RunnerDeps{
		Log:        testLogger(),
		Store:      store,
		Config:     func() config.Config { return config.Config{} },
		Enrichment: handler,
	})

	var reports []Report
	if err := runners.Enrich()(context.Background(), func(r Report) { reports = append(reports, r) }); err != nil {
		t.Fatalf("Enrich: %v", err)
	}

	activity := runners.EnrichActivity()
	if len(activity) != 1 {
		t.Fatalf("activity = %v, want 1 recorded entry", activity)
	}
	if activity[0].TrackID != 1 {
		t.Errorf("activity[0].TrackID = %d, want 1", activity[0].TrackID)
	}
	if activity[0].Outcome == "" || activity[0].Outcome == EnrichOutcomeCancelled {
		t.Errorf("activity[0].Outcome = %q, want a terminal outcome", activity[0].Outcome)
	}
	if len(reports) == 0 {
		t.Error("expected progress reports")
	}
	last := reports[len(reports)-1]
	if last.Done != 1 || last.Total != 1 {
		t.Errorf("final report done/total = %d/%d, want 1/1", last.Done, last.Total)
	}
}

// TestEnrichRunnerEmptyLibrary verifies a no-tracks library is a fast no-op.
func TestEnrichRunnerEmptyLibrary(t *testing.T) {
	store := &enrichStore{}
	reg := metadata.NewRegistry()
	handler := download.NewMetadataEnrichmentHandler(reg, nil, store, testLogger())

	runners := NewRunners(RunnerDeps{
		Log:        testLogger(),
		Store:      store,
		Config:     func() config.Config { return config.Config{} },
		Enrichment: handler,
	})

	var reports []Report
	if err := runners.Enrich()(context.Background(), func(r Report) { reports = append(reports, r) }); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(runners.EnrichActivity()) != 0 {
		t.Error("expected no activity for an empty library")
	}
	if len(reports) != 0 {
		t.Errorf("expected no reports for empty library, got %d", len(reports))
	}
}

// TestEnrichRunnerNilEnrichment verifies a nil enrichment dependency is a
// silent no-op (matches the other nil-dep guards).
func TestEnrichRunnerNilEnrichment(t *testing.T) {
	runners := NewRunners(RunnerDeps{
		Log:    testLogger(),
		Store:  &enrichStore{},
		Config: func() config.Config { return config.Config{} },
	})
	if err := runners.Enrich()(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("Enrich with nil handler: %v", err)
	}
}

// TestRunnersStateAccessors covers the small state accessors that the API layer
// uses: boot job snapshot, organize report, divergence flags, activity reset.
func TestRunnersStateAccessors(t *testing.T) {
	logger := testLogger()
	runners := NewRunners(RunnerDeps{
		Log:    logger,
		Store:  &enrichStore{},
		Config: func() config.Config { return config.Config{} },
	})

	if runners.Logger() != logger {
		t.Error("Logger should return the configured logger")
	}

	if runners.BootJob() != nil {
		t.Errorf("BootJob = %v, want nil initially", runners.BootJob())
	}
	j := &Job{Type: "scan", State: "running", Progress: 10}
	runners.SetBootJob(j)
	if runners.BootJob() != j {
		t.Errorf("BootJob = %v, want the set job", runners.BootJob())
	}

	if runners.OrganizeReport() != nil {
		t.Errorf("OrganizeReport = %v, want nil initially", runners.OrganizeReport())
	}
	rep := &OrganizeReport{Mode: "dry run", RanAt: time.Now()}
	runners.SetOrganizeReport(rep)
	if runners.OrganizeReport() != rep {
		t.Errorf("OrganizeReport = %v, want the set report", runners.OrganizeReport())
	}

	if runners.OrganizeDivergencePossible() {
		t.Error("OrganizeDivergencePossible should be false initially (no interrupted organize in the boot snapshot)")
	}
	// Arm the trigger: boot snapshot shows an interrupted organize.
	runners.SetBootJob(&Job{Type: "organize", State: StateInterrupted})
	if !runners.OrganizeDivergencePossible() {
		t.Error("OrganizeDivergencePossible should be true when boot snapshot shows an interrupted organize")
	}
	runners.MarkDivergenceRepairDone()
	if runners.OrganizeDivergencePossible() {
		t.Error("OrganizeDivergencePossible should be false after MarkDivergenceRepairDone")
	}

	runners.RecordEnrichActivity(EnrichActivity{At: time.Now().UTC(), TrackID: 1})
	runners.ResetEnrichActivity()
	if len(runners.EnrichActivity()) != 0 {
		t.Error("EnrichActivity should be empty after ResetEnrichActivity")
	}
}

// TestScanRunnerReconcilesDivergedPaths verifies the Scan runner arms the
// killed-organize reconcile when the boot snapshot shows an interrupted
// organize job, and disarms it after the pass. The reconcile itself is a
// no-op on an empty library, but the trigger/arm/complete lifecycle is what
// the runner owns.
func TestScanRunnerReconcilesDivergedPaths(t *testing.T) {
	libDir := t.TempDir()
	cfg, err := config.LoadOrCreate(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Library.LibraryPath = libDir
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store := &scanStore{}
	scanner := library.NewScanner(store, testLogger())

	runners := NewRunners(RunnerDeps{
		Log:     testLogger(),
		Store:   store,
		Config:  func() config.Config { return cfg.Get() },
		Scanner: scanner,
	})

	// Arm the divergence trigger.
	runners.SetBootJob(&Job{Type: "organize", State: StateInterrupted})
	if !runners.OrganizeDivergencePossible() {
		t.Fatal("precondition: divergence trigger should be armed")
	}

	if err := runners.Scan()(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if runners.OrganizeDivergencePossible() {
		t.Error("divergence trigger should be disarmed after a completed scan")
	}
}
