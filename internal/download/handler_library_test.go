package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

func TestLibraryImporterHandler_CreatesArtistAlbumTrack(t *testing.T) {
	tmpDir := t.TempDir()
	artistDir := filepath.Join(tmpDir, "Test Artist")
	albumDir := filepath.Join(artistDir, "Test Album (2024)")
	os.MkdirAll(albumDir, 0o755)
	trackFile := filepath.Join(albumDir, "01 Test Song.mp3")
	os.WriteFile(trackFile, []byte("audio"), 0o644)

	libStore := newMockLibStore()
	handler := NewLibraryImporterHandler(libStore, nil)

	record := &Record{
		ID:          "test-lib-1",
		SourceName:  "deezer",
		FilePath:    trackFile,
		Artist:      "Test Artist",
		Album:       "Test Album",
		Title:       "Test Song",
		TrackNumber: 1,
		Year:        2024,
		TrackID:     "123456",
	}

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Verify artist was created.
	artist, _ := libStore.GetArtistByName(context.Background(), "Test Artist")
	if artist == nil {
		t.Fatal("artist should be created")
	}

	// Verify album was created.
	albums, _ := libStore.SearchAlbums(context.Background(), "Test Album", 1)
	if len(albums) == 0 {
		t.Fatal("album should be created")
	}

	// Verify library track ID was set.
	if record.LibraryTrackID == 0 {
		t.Error("LibraryTrackID should be set after import")
	}
}

func TestLibraryImporterHandler_SkipsExistingByPath(t *testing.T) {
	tmpDir := t.TempDir()
	existingPath := filepath.Join(tmpDir, "existing_track.mp3")
	os.WriteFile(existingPath, []byte("audio"), 0o644)

	libStore := newMockLibStore()
	handler := NewLibraryImporterHandler(libStore, nil)

	// Pre-populate with an existing track at this path.
	libStore.UpsertTrack(context.Background(), &domain.Track{
		ID:       42,
		FilePath: existingPath,
	})

	record := &Record{
		ID:       "test-lib-2",
		FilePath: existingPath,
		Artist:   "Test Artist",
		Title:    "Track",
	}

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if record.LibraryTrackID != 42 {
		t.Errorf("expected LibraryTrackID 42, got %d", record.LibraryTrackID)
	}
}

func TestLibraryImporterHandler_NoFilePath(t *testing.T) {
	handler := NewLibraryImporterHandler(newMockLibStore(), nil)
	record := &Record{ID: "test-lib-3"}

	err := handler.Handle(context.Background(), record)
	if err == nil {
		t.Error("expected error for missing file path")
	}
}

// TestLibraryImporterHandler_StampsRequesterAttribution verifies a download
// queued by a user produces a library track carrying that user's id + name
// snapshot (DB-only attribution).
func TestLibraryImporterHandler_StampsRequesterAttribution(t *testing.T) {
	tmpDir := t.TempDir()
	trackFile := filepath.Join(tmpDir, "01 Song.mp3")
	os.WriteFile(trackFile, []byte("audio"), 0o644)

	libStore := newMockLibStore()
	handler := NewLibraryImporterHandler(libStore, nil)

	record := &Record{
		ID:                  "test-lib-attr",
		SourceName:          "deezer",
		FilePath:            trackFile,
		Artist:              "Test Artist",
		Album:               "Test Album",
		Title:               "Test Song",
		TrackNumber:         1,
		RequestedByUserID:   42,
		RequestedByUsername: "alice",
	}

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	stored, err := libStore.GetTrackByFilePath(context.Background(), trackFile)
	if err != nil || stored == nil {
		t.Fatalf("imported track not found: %v", err)
	}
	if stored.AddedByUserID != 42 {
		t.Errorf("AddedByUserID = %d, want 42", stored.AddedByUserID)
	}
	if stored.AddedByUsername != "alice" {
		t.Errorf("AddedByUsername = %q, want alice", stored.AddedByUsername)
	}
}

// TestLibraryImporterHandler_SystemLeavesAttributionBlank verifies a download
// with no requester (system/scanner path) stamps zero/blank attribution.
func TestLibraryImporterHandler_SystemLeavesAttributionBlank(t *testing.T) {
	tmpDir := t.TempDir()
	trackFile := filepath.Join(tmpDir, "02 Song.mp3")
	os.WriteFile(trackFile, []byte("audio"), 0o644)

	libStore := newMockLibStore()
	handler := NewLibraryImporterHandler(libStore, nil)

	record := &Record{
		ID:       "test-lib-system",
		FilePath: trackFile,
		Artist:   "Test Artist",
		Title:    "Test Song",
	}

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	stored, err := libStore.GetTrackByFilePath(context.Background(), trackFile)
	if err != nil || stored == nil {
		t.Fatalf("imported track not found: %v", err)
	}
	if stored.AddedByUserID != 0 || stored.AddedByUsername != "" {
		t.Errorf("system attribution = (%d, %q), want (0, \"\")",
			stored.AddedByUserID, stored.AddedByUsername)
	}
}
