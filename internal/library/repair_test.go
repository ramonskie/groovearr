package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

// repairStore is a minimal Store for exercising RepairDivergedPaths.
type repairStore struct {
	Store
	tracks  []domain.Track
	artists map[int64]*domain.Artist
	albums  map[int64]*domain.Album
}

func (s *repairStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	return s.tracks, nil
}

func (s *repairStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	return s.artists[id], nil
}

func (s *repairStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) {
	return s.albums[id], nil
}

func (s *repairStore) UpsertTrack(ctx context.Context, t *domain.Track) (int64, error) {
	for i := range s.tracks {
		if s.tracks[i].ID == t.ID {
			s.tracks[i] = *t
			return t.ID, nil
		}
	}
	s.tracks = append(s.tracks, *t)
	return t.ID, nil
}

func repairTestOrganizer(root string, store *repairStore) *Organizer {
	return NewOrganizer("{artist}/{album} ({year})/{track:02d} - {title}", testCompilationTemplate, root, store, testLogger())
}

// TestRepairDivergedPathsAdoptsMovedTarget simulates the organize kill window:
// the DB still points at the pre-organize path (missing on disk) while the
// file sits at the organized target. The repair must adopt the target.
func TestRepairDivergedPathsAdoptsMovedTarget(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "Flat Artist", "Flat Album", "01 - Track.flac") // stale, does not exist
	target := filepath.Join(root, "Flat Artist", "Album (2020)", "01 - Track.flac")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("audio bytes")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}

	store := &repairStore{
		tracks: []domain.Track{
			{ID: 1, Title: "Track", TrackNumber: 1, ArtistID: 1, AlbumID: 1, FilePath: oldPath, FileSize: int64(len(content))},
		},
		artists: map[int64]*domain.Artist{1: {ID: 1, Name: "Flat Artist"}},
		albums:  map[int64]*domain.Album{1: {ID: 1, Title: "Album", Year: 2020, ArtistID: 1}},
	}
	org := repairTestOrganizer(root, store)

	n, err := org.RepairDivergedPaths(context.Background(), nil)
	if err != nil {
		t.Fatalf("RepairDivergedPaths: %v", err)
	}
	if n != 1 {
		t.Fatalf("repaired = %d, want 1", n)
	}
	if store.tracks[0].FilePath != target {
		t.Errorf("DB path = %q, want %q", store.tracks[0].FilePath, target)
	}
}

// TestRepairDivergedPathsSizeMismatch: a file at the target with a different
// size must not be adopted (could be a different file entirely).
func TestRepairDivergedPathsSizeMismatch(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "Flat Artist", "Flat Album", "01 - Track.flac")
	target := filepath.Join(root, "Flat Artist", "Album (2020)", "01 - Track.flac")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("audio bytes")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}

	store := &repairStore{
		tracks: []domain.Track{
			// FileSize does not match the file on disk.
			{ID: 1, Title: "Track", TrackNumber: 1, ArtistID: 1, AlbumID: 1, FilePath: oldPath, FileSize: 99999},
		},
		artists: map[int64]*domain.Artist{1: {ID: 1, Name: "Flat Artist"}},
		albums:  map[int64]*domain.Album{1: {ID: 1, Title: "Album", Year: 2020, ArtistID: 1}},
	}
	org := repairTestOrganizer(root, store)

	n, err := org.RepairDivergedPaths(context.Background(), nil)
	if err != nil {
		t.Fatalf("RepairDivergedPaths: %v", err)
	}
	if n != 0 {
		t.Fatalf("repaired = %d, want 0 (size mismatch)", n)
	}
	if store.tracks[0].FilePath != oldPath {
		t.Errorf("DB path changed despite size mismatch: %q", store.tracks[0].FilePath)
	}
}

// TestRepairDivergedPathsNoTarget: DB path missing and nothing at the target —
// nothing to adopt.
func TestRepairDivergedPathsNoTarget(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "Flat Artist", "Flat Album", "01 - Track.flac")

	store := &repairStore{
		tracks: []domain.Track{
			{ID: 1, Title: "Track", TrackNumber: 1, ArtistID: 1, AlbumID: 1, FilePath: oldPath, FileSize: 100},
		},
		artists: map[int64]*domain.Artist{1: {ID: 1, Name: "Flat Artist"}},
		albums:  map[int64]*domain.Album{1: {ID: 1, Title: "Album", Year: 2020, ArtistID: 1}},
	}
	org := repairTestOrganizer(root, store)

	n, err := org.RepairDivergedPaths(context.Background(), nil)
	if err != nil {
		t.Fatalf("RepairDivergedPaths: %v", err)
	}
	if n != 0 {
		t.Fatalf("repaired = %d, want 0 (no target)", n)
	}
}

// TestRepairDivergedPathsLeavesValidTracksAlone: a track whose DB path exists
// on disk must not be touched.
func TestRepairDivergedPathsLeavesValidTracksAlone(t *testing.T) {
	root := t.TempDir()
	validPath := filepath.Join(root, "Flat Artist", "Album (2020)", "01 - Track.flac")
	if err := os.MkdirAll(filepath.Dir(validPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(validPath, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &repairStore{
		tracks: []domain.Track{
			{ID: 1, Title: "Track", TrackNumber: 1, ArtistID: 1, AlbumID: 1, FilePath: validPath, FileSize: 5},
		},
		artists: map[int64]*domain.Artist{1: {ID: 1, Name: "Flat Artist"}},
		albums:  map[int64]*domain.Album{1: {ID: 1, Title: "Album", Year: 2020, ArtistID: 1}},
	}
	org := repairTestOrganizer(root, store)

	n, err := org.RepairDivergedPaths(context.Background(), nil)
	if err != nil {
		t.Fatalf("RepairDivergedPaths: %v", err)
	}
	if n != 0 {
		t.Fatalf("repaired = %d, want 0", n)
	}
}
