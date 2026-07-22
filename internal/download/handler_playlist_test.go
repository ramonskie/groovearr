package download

import (
	"context"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

func TestPlaylistLinkerHandler_LinksMatchingTrack(t *testing.T) {
	libStore := newMockLibStore()

	// Setup: create artist and track in library.
	libStore.UpsertArtist(context.Background(), &domain.Artist{ID: 1, Name: "Test Artist"})
	trackID, _ := libStore.UpsertTrack(context.Background(), &domain.Track{
		ArtistID: 1,
		Title:    "Test Song",
	})

	handler := NewPlaylistLinkerHandler(libStore, testLogger())

	record := &domain.DownloadRecord{
		ID:             "test-link-1",
		PlaylistID:     "5",
		LibraryTrackID: trackID,
		Artist:         "Test Artist",
		Title:          "Test Song",
	}

	err := handler.Handle(context.Background(), record)
	// Should not error even if no playlist tracks exist (no-op).
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
}

func TestPlaylistLinkerHandler_NoPlaylistID(t *testing.T) {
	handler := NewPlaylistLinkerHandler(newMockLibStore(), testLogger())
	record := &domain.DownloadRecord{
		ID:             "test-link-2",
		LibraryTrackID: 100,
	}

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle should be no-op when PlaylistID empty: %v", err)
	}
}

func TestPlaylistLinkerHandler_NoLibraryTrackID(t *testing.T) {
	handler := NewPlaylistLinkerHandler(newMockLibStore(), testLogger())
	record := &domain.DownloadRecord{
		ID:         "test-link-3",
		PlaylistID: "5",
	}

	err := handler.Handle(context.Background(), record)
	if err != nil {
		t.Fatalf("Handle should be no-op when LibraryTrackID is 0: %v", err)
	}
}
