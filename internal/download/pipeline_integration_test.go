package download_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	dlsqlite "github.com/ramonskie/groovearr/internal/download/sqlite"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/library"
	libsqlite "github.com/ramonskie/groovearr/internal/library/sqlite"
)

// TestPlaylistDownloadPipeline verifies the full download→import→link pipeline.
func TestPlaylistDownloadPipeline(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Real SQLite library + download stores (shared connection).
	libStore, err := libsqlite.New(dbPath)
	if err != nil {
		t.Fatalf("create lib store: %v", err)
	}
	defer libStore.Close()

	dlStore := dlsqlite.New(libStore.DB())

	// Seed: artist, album, playlist, playlist track.
	_, err = libStore.UpsertArtist(context.Background(), &domain.Artist{Name: "Test Artist"})
	if err != nil {
		t.Fatalf("upsert artist: %v", err)
	}
	_, err = libStore.UpsertAlbum(context.Background(), &domain.Album{
		ArtistID: 1, Title: "Test Album", Year: 2024,
	})
	if err != nil {
		t.Fatalf("upsert album: %v", err)
	}
	if _, err := libStore.UpsertPlaylist(context.Background(), &domain.Playlist{
		Source: "deezer", SourcePlaylistID: "pl-123", Name: "Test Playlist", TrackCount: 1,
	}); err != nil {
		t.Fatalf("upsert playlist: %v", err)
	}
	if err := libStore.UpsertPlaylistTrack(context.Background(), &domain.PlaylistTrack{
		PlaylistID: 1, Position: 1, SourceTrackID: "src-001",
		Title: "Test Song", Artist: "Test Artist", Album: "Test Album",
	}); err != nil {
		t.Fatalf("upsert playlist track: %v", err)
	}

	// Create a minimal valid FLAC file.
	srcPath := filepath.Join(tmpDir, "downloads", "Test Song.flac")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	flacData := []byte{
		'f', 'L', 'a', 'C',
		0x80, 0x00, 0x00, 0x22,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00,
	}
	if err := os.WriteFile(srcPath, flacData, 0o644); err != nil {
		t.Fatalf("write flac: %v", err)
	}

	eventBus := events.NewInMemoryEventBus()

	// Insert (state forced to "queued" by store).
	downloadID := "test-dl-001"
	if err := dlStore.Insert(context.Background(), &domain.DownloadRecord{
		ID: downloadID, SourceName: "deezer", Filename: "Test Song.flac",
		Artist: "Test Artist", Album: "Test Album",
		Title: "Test Song", PlaylistID: "1",
	}); err != nil {
		t.Fatalf("insert download: %v", err)
	}

	// Simulate worker: queued → downloading, set file_path, downloading → importPending.
	if ok, _ := dlStore.TransitionState(context.Background(), downloadID,
		domain.DownloadQueued, domain.DownloadDownloading); !ok {
		t.Fatal("transition to downloading failed")
	}
	if err := dlStore.Update(context.Background(), &domain.DownloadRecord{
		ID: downloadID, FilePath: srcPath, State: domain.DownloadDownloading,
	}); err != nil {
		t.Fatalf("update file_path: %v", err)
	}
	if ok, _ := dlStore.TransitionState(context.Background(), downloadID,
		domain.DownloadDownloading, domain.DownloadImportPending); !ok {
		t.Fatal("transition to importPending failed")
	}

	// Build import chain.
	renamer := library.NewRenamer("{artist}/{album}/{title}", filepath.Join(tmpDir, "music"))
	_ = download.NewCompletedDownloadService(
		dlStore, eventBus,
		download.NewFileRenamerHandler(renamer, dlStore),
		download.NewCoverArtHandler(libStore),
		download.NewTagWriterHandler(),
		download.NewLibraryImporterHandler(libStore),
		download.NewPlaylistLinkerHandler(libStore),
	)

	// Fire download completed event.
	eventBus.Publish(context.Background(), events.TopicDownloadCompleted, &domain.DownloadRecord{
		ID: downloadID, State: domain.DownloadImportPending,
	})

	// Wait for async chain to finish.
	var dl *domain.DownloadRecord
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		dl, _ = dlStore.Get(context.Background(), downloadID)
		if dl != nil && dl.State.Terminal() {
			t.Logf("download %s terminal: %s (iter %d)", downloadID, dl.State, i)
			break
		}
		if i == 49 {
			t.Fatalf("download did not reach terminal state (state=%q, error=%s)", dl.State, dl.Error)
		}
	}

	// Assertions.
	if dl.State != domain.DownloadImported {
		t.Errorf("state = %q, want imported. Error: %s", dl.State, dl.Error)
	}
	// Note: LibraryTrackID is in-memory only (not persisted in downloads table).
	// Verify import success via the library and playlist link instead.
	if dl.LibraryTrackID != 0 {
		t.Logf("LibraryTrackID: %d (in-memory)", dl.LibraryTrackID)
	}

	track, _ := libStore.GetTrack(context.Background(), dl.LibraryTrackID)
	if track == nil {
		// LibraryTrackID not persisted — search by file path instead.
		renamed := filepath.Join(tmpDir, "music", "Test Artist", "Test Album", "Test Song.flac")
		track, _ = libStore.GetTrackByFilePath(context.Background(), renamed)
	}
	if track == nil {
		t.Fatal("library track not found — LibraryImporterHandler did not import")
	}
	t.Logf("library track: id=%d title=%q file=%s", track.ID, track.Title, track.FilePath)

	pts, _ := libStore.GetPlaylistTracks(context.Background(), 1)
	if len(pts) == 0 {
		t.Fatal("no playlist tracks")
	}
	if pts[0].TrackID == nil {
		t.Error("playlist track TrackID is nil — PlaylistLinkerHandler did not link")
	} else if *pts[0].TrackID != track.ID {
		t.Errorf("playlist track linked to %d, want %d", *pts[0].TrackID, track.ID)
	} else {
		t.Logf("✓ playlist linked: position=%d track_id=%d", pts[0].Position, *pts[0].TrackID)
	}
}
