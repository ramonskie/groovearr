package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// scanStore is a minimal store for exercising the Scan runner through a real
// library.Scanner. It embeds the nil interface and overrides only the methods
// the scanner calls while walking and importing.
type scanStore struct {
	library.Store
	imported []string // file paths handed to ImportTrack
}

func (s *scanStore) GetArtistByName(ctx context.Context, name string) (*domain.Artist, error) {
	return nil, nil
}
func (s *scanStore) GetTrackByFilePath(ctx context.Context, filePath string) (*domain.Track, error) {
	return nil, nil
}
func (s *scanStore) ImportTrack(ctx context.Context, track *domain.Track, artistName, albumTitle string, albumYear int, genres []string) (int64, error) {
	s.imported = append(s.imported, track.FilePath)
	return int64(len(s.imported)), nil
}
func (s *scanStore) SetArtistThumbURL(ctx context.Context, artistID int64, thumbURL string) error {
	return nil
}
func (s *scanStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	return nil, nil
}

// TestScanRunnerImportsAndReports exercises the Scan runner end to end: a real
// scanner against a temp library directory imports audio files, reports per-file
// progress, and finishes with the outcome summary.
func TestScanRunnerImportsAndReports(t *testing.T) {
	libDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(libDir, "01 - Track.flac"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(libDir, "Nested", "Album"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "Nested", "Album", "02 - Track.flac"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-audio file must be skipped.
	if err := os.WriteFile(filepath.Join(libDir, "cover.jpg"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &scanStore{}
	scanner := library.NewScanner(store, testLogger())

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

	runners := NewRunners(RunnerDeps{
		Log:     testLogger(),
		Store:   store,
		Config:  func() config.Config { return cfg.Get() },
		Scanner: scanner,
	})

	var reports []Report
	if err := runners.Scan()(context.Background(), func(r Report) { reports = append(reports, r) }); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(store.imported) != 2 {
		t.Errorf("imported %d files, want 2 (non-audio skipped): %v", len(store.imported), store.imported)
	}
	if len(reports) < 3 {
		t.Fatalf("expected progress reports, got %d", len(reports))
	}
	last := reports[len(reports)-1]
	if last.Message == "" {
		t.Error("final report should carry the outcome summary")
	}
}

// TestScanRunnerNilScanner verifies the nil-guard returns a clear error instead
// of panicking when the scanner dependency is absent.
func TestScanRunnerNilScanner(t *testing.T) {
	runners := NewRunners(RunnerDeps{
		Log:    testLogger(),
		Store:  &scanStore{},
		Config: func() config.Config { return config.Config{} },
	})
	err := runners.Scan()(context.Background(), func(Report) {})
	if err == nil {
		t.Fatal("expected error when Scanner is nil")
	}
}

// enrichStore is a minimal store for exercising the Enrich runner through a
// real MetadataEnrichmentHandler. The handler only reads the track, artist,
// and album it is asked to enrich.

// TestScanRunnerLayoutGuard verifies the scan job's layout guard: a library
// path that overlaps a download staging directory in either direction fails
// the job with ErrScanLayout (so the UI shows the misconfiguration instead of
// a misleading "scanned 0"), while a sibling layout scans normally.
func TestScanRunnerLayoutGuard(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name    string
		lib     string
		dl      string
		refused bool
	}{
		{"library under download root", filepath.Join(root, "downloads", "library"), filepath.Join(root, "downloads"), true},
		{"library equals download root", filepath.Join(root, "downloads"), filepath.Join(root, "downloads"), true},
		{"download nested under library", filepath.Join(root, "music"), filepath.Join(root, "music", "downloads"), true},
		{"case-insensitive overlap", filepath.Join(root, "MUSIC"), filepath.Join(root, "music"), true},
		{"sibling layout is fine", filepath.Join(root, "music"), filepath.Join(root, "downloads"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, d := range []string{tc.lib, tc.dl} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			os.WriteFile(filepath.Join(tc.lib, "01 - Track.flac"), []byte("fake"), 0o644)

			store := &scanStore{}
			scanner := library.NewScanner(store, testLogger())

			cfg, err := config.LoadOrCreate(filepath.Join(t.TempDir(), "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Update(func(c *config.Config) error {
				c.Library.LibraryPath = tc.lib
				c.Library.DownloadPath = tc.dl
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			runners := NewRunners(RunnerDeps{
				Log:     testLogger(),
				Store:   store,
				Config:  func() config.Config { return cfg.Get() },
				Scanner: scanner,
			})

			err = runners.Scan()(context.Background(), func(Report) {})
			if tc.refused {
				if err == nil {
					t.Fatal("expected scan job to fail on layout refusal")
				}
				if !errors.Is(err, ErrScanLayout) {
					t.Fatalf("errors.Is(err, ErrScanLayout) = false, err = %v", err)
				}
			} else if err != nil {
				t.Fatalf("scan should succeed for sibling layout, got %v", err)
			}
		})
	}
}

// TestScanRunnerRefusesSymlinkedLibraryPath verifies the layout guard resolves
// symlinks before comparing: a library path that is a symlink into a download
// staging dir must still be refused (both sides resolve to the same real
// directory).
func TestScanRunnerRefusesSymlinkedLibraryPath(t *testing.T) {
	root := t.TempDir()
	downloadDir := filepath.Join(root, "downloads")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(downloadDir, "01 - Track.flac"), []byte("fake"), 0o644)
	libLink := filepath.Join(root, "music")
	if err := os.Symlink(downloadDir, libLink); err != nil {
		t.Fatal(err)
	}

	store := &scanStore{}
	scanner := library.NewScanner(store, testLogger())

	cfg, err := config.LoadOrCreate(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Library.LibraryPath = libLink
		c.Library.DownloadPath = downloadDir
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	runners := NewRunners(RunnerDeps{
		Log:     testLogger(),
		Store:   store,
		Config:  func() config.Config { return cfg.Get() },
		Scanner: scanner,
	})

	err = runners.Scan()(context.Background(), func(Report) {})
	if err == nil {
		t.Fatal("expected scan job to fail for symlinked library path into staging")
	}
	if !errors.Is(err, ErrScanLayout) {
		t.Fatalf("errors.Is(err, ErrScanLayout) = false, err = %v", err)
	}
}
