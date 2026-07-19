package download_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	dlsqlite "github.com/ramonskie/groovearr/internal/download/sqlite"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/library"
	libsqlite "github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/playlist"
)

// ─── Mock download plugin ────────────────────────────────────────────

type mockDLPlugin struct {
	mu      sync.Mutex
	records map[string]*domain.DownloadRecord // pluginID → record
	dlPath  string
}

func newMockDLPlugin(dlPath string) *mockDLPlugin {
	return &mockDLPlugin{records: make(map[string]*domain.DownloadRecord), dlPath: dlPath}
}

func (m *mockDLPlugin) Name() string             { return "mock" }
func (m *mockDLPlugin) DisplayName() string      { return "Mock" }
func (m *mockDLPlugin) IsConfigured() bool       { return true }
func (m *mockDLPlugin) Connected() bool           { return true }
func (m *mockDLPlugin) CheckConnection(context.Context) error { return nil }

func (m *mockDLPlugin) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	// Return results matching playlist tracks by title.
	results := []domain.TrackResult{
		{SearchResult: domain.SearchResult{Username: "mock", Filename: "Take on Me.flac", Size: 28_000_000, Quality: "flac"}, Artist: "a-ha", Title: "Take on Me", Album: "Hunting High and Low"},
		{SearchResult: domain.SearchResult{Username: "mock", Filename: "Billie Jean.flac", Size: 30_000_000, Quality: "flac"}, Artist: "Michael Jackson", Title: "Billie Jean", Album: "Thriller"},
		{SearchResult: domain.SearchResult{Username: "mock", Filename: "Bohemian Rhapsody.flac", Size: 35_000_000, Quality: "flac"}, Artist: "Queen", Title: "Bohemian Rhapsody", Album: "A Night at the Opera"},
	}
	// Filter by query (case-insensitive substring).
	var filtered []domain.TrackResult
	q := strings.ToLower(query)
	for _, r := range results {
		if strings.Contains(strings.ToLower(r.Title), q) || strings.Contains(strings.ToLower(r.Artist), q) {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) == 0 {
		filtered = results
	}
	return filtered, nil, nil
}

func (m *mockDLPlugin) Download(ctx context.Context, username, filename string, fileSize int64) (string, error) {
	pluginID := "mock-dl-" + filename[:min(8, len(filename))]
	m.mu.Lock()
	m.records[pluginID] = &domain.DownloadRecord{
		ID:         pluginID,
		State:      domain.DownloadDownloading,
		Filename:   filename,
		Size:       100_000,
		Transferred: 0,
		Progress:   0,
	}
	m.mu.Unlock()

	// Simulate async download: write a small flac file + mark completed.
	go func() {
		time.Sleep(100 * time.Millisecond) // simulate network latency

		outPath := filepath.Join(m.dlPath, filename)
		_ = os.WriteFile(outPath, validFLAC(), 0o644)

		m.mu.Lock()
		r := m.records[pluginID]
		r.State = domain.DownloadImported
		r.Progress = 100
		r.FilePath = outPath
		r.Transferred = int64(len(validFLAC()))
		r.Size = r.Transferred
		m.mu.Unlock()
	}()

	return pluginID, nil
}

func (m *mockDLPlugin) GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[downloadID]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	cp := *r
	return &cp, nil
}

func (m *mockDLPlugin) GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.DownloadRecord
	for _, r := range m.records {
		out = append(out, *r)
	}
	return out, nil
}

func (m *mockDLPlugin) CancelDownload(ctx context.Context, downloadID string, remove bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.records[downloadID]; ok {
		r.State = domain.DownloadIgnored
	}
	return nil
}

func (m *mockDLPlugin) ClearCompleted(ctx context.Context) error { return nil }

// ─── Mock playlist source ─────────────────────────────────────────────

type mockPlaylistSrc struct{}

func (m *mockPlaylistSrc) Name() string        { return "mock" }
func (m *mockPlaylistSrc) DisplayName() string { return "Mock" }
func (m *mockPlaylistSrc) IsConfigured() bool  { return true }

func (m *mockPlaylistSrc) GetUserPlaylists(ctx context.Context) ([]playlist.PlaylistInfo, error) {
	return []playlist.PlaylistInfo{{
		SourceID: "pl-1", Name: "Test Mix", TrackCount: 3,
	}}, nil
}

func (m *mockPlaylistSrc) GetPlaylistTracks(ctx context.Context, sourcePlaylistID string) ([]playlist.TrackInfo, string, error) {
	return []playlist.TrackInfo{
		{SourceTrackID: "t1", Title: "Take on Me", Artist: "a-ha", Album: "Hunting High and Low", DurationMs: 225000},
		{SourceTrackID: "t2", Title: "Billie Jean", Artist: "Michael Jackson", Album: "Thriller", DurationMs: 294000},
		{SourceTrackID: "t3", Title: "Bohemian Rhapsody", Artist: "Queen", Album: "A Night at the Opera", DurationMs: 355000},
	}, "Test Mix", nil
}

// ─── Helpers ──────────────────────────────────────────────────────────

func validFLAC() []byte {
	// Minimal valid FLAC: fLaC magic + streaminfo block + silence frame.
	streaminfo := make([]byte, 34)
	streaminfo[0] = 0x80 // last metadata block flag + STREAMINFO type
	streaminfo[1] = 0x00
	streaminfo[2] = 0x00
	streaminfo[3] = 0x22 // block length = 34
	// Minimum block size 4096, maximum 4096
	streaminfo[8] = 0x10
	streaminfo[9] = 0x00
	streaminfo[12] = 0x10
	streaminfo[13] = 0x00
	// Sample rate 44100
	streaminfo[18] = 0x0A
	streaminfo[19] = 0xC4
	streaminfo[20] = 0x42
	// Channels = 2, bps = 16, total samples = 44100
	streaminfo[21] = 0x0F
	streaminfo[22] = 0x00
	streaminfo[23] = 0xAC
	streaminfo[24] = 0x44
	streaminfo[25] = 0x00

	// Silence frame: sync code + blocking strategy + frame size + CRC8
	frame := []byte{0xFF, 0xF8, 0x00, 0x00, 0x00, 0x00}

	header := append([]byte("fLaC"), streaminfo...)
	return append(header, frame...)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Test ─────────────────────────────────────────────────────────────

func TestFullPlaylistPipeline(t *testing.T) {
	tmpDir := t.TempDir()
	dlPath := filepath.Join(tmpDir, "downloads")
	libPath := filepath.Join(tmpDir, "music")
	plPath := filepath.Join(tmpDir, "playlists")
	for _, d := range []string{dlPath, libPath, plPath} {
		os.MkdirAll(d, 0o755)
	}

	// Real SQLite stores.
	libStore, err := libsqlite.New(filepath.Join(tmpDir, "library.db"))
	if err != nil {
		t.Fatalf("lib store: %v", err)
	}
	defer libStore.Close()
	dlStore := dlsqlite.New(libStore.DB())

	// Mock plugin + registry.
	mockPlugin := newMockDLPlugin(dlPath)
	reg := download.NewRegistry()
	if err := reg.Register(mockPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Event bus.
	eventBus := events.NewInMemoryEventBus()

	// Download service + worker pool.
	downloadSvc := download.NewDownloadService(dlStore, eventBus)
	workerPool := download.NewWorkerPool(3, reg, dlStore, eventBus)
	downloadSvc.SetWorkerPool(workerPool)
	defer workerPool.Shutdown()

	// Renamer: library path.
	renamer := library.NewRenamer("{artist}/{album}/{title}", libPath)

	// Import handler chain.
	_ = download.NewCompletedDownloadService(
		dlStore, eventBus,
		download.NewFileRenamerHandler(renamer, dlStore),
		download.NewCoverArtHandler(libStore),
		download.NewTagWriterHandler(),
		download.NewLibraryImporterHandler(libStore),
		download.NewPlaylistLinkerHandler(libStore),
	)

	// Playlist service.
	plReg := playlist.NewRegistry()
	plReg.Register(&mockPlaylistSrc{})
	cfg := config.Config{
		Library: config.LibraryConfig{
			DownloadPath:     dlPath,
			LibraryPath:      libPath,
			PlaylistPath:     plPath,
			FolderTemplate:   "{artist}/{album}/{title}",
			PlaylistTemplate: "{position:02d} {artist} - {title}",
		},
	}
	plSvc := playlist.NewService(plReg, libStore, reg, downloadSvc, func() config.Config { return cfg })

	// ─── Step 1: Import playlist ────────────────────────────────────
	result, err := plSvc.ImportPlaylist(context.Background(), "mock", "pl-1")
	if err != nil {
		t.Fatalf("import playlist: %v", err)
	}
	t.Logf("imported: %d linked, %d unmatched", result.Linked, result.Unmatched)

	if result.Unmatched != 3 {
		t.Fatalf("expected 3 unmatched, got %d", result.Unmatched)
	}

	// ─── Step 2: Wait for downloads AND sync to complete ───────────
	deadline := time.Now().Add(30 * time.Second)
	var allTerminal, folderReady bool
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)

		// Check downloads.
		downloads, _ := downloadSvc.List(context.Background())
		pending := 0
		for _, d := range downloads {
			if !d.State.Terminal() {
				pending++
			}
		}
		allTerminal = pending == 0 && len(downloads) > 0

		// Check playlist folder (syncPlaylistGuarded builds this async).
		plDir := filepath.Join(plPath, "Test Mix")
		entries, _ := os.ReadDir(plDir)
		folderReady = len(entries) >= 3

		if allTerminal && folderReady {
			t.Logf("downloads terminal + playlist folder ready (%d files)", len(entries))
			break
		}
	}
	if !allTerminal {
		downloads, _ := downloadSvc.List(context.Background())
		for _, d := range downloads {
			if !d.State.Terminal() {
				t.Errorf("download %s stuck in state %s", d.ID, d.State)
			}
		}
	}
	if !folderReady {
		plDir := filepath.Join(plPath, "Test Mix")
		entries, _ := os.ReadDir(plDir)
		t.Errorf("playlist folder not ready: %d files (want 3)", len(entries))
	}

	// ─── Step 3: Verify download states ─────────────────────────────
	downloads, _ := downloadSvc.List(context.Background())
	for _, d := range downloads {
		id := d.ID
		if len(id) > 30 {
			id = id[:30]
		}
		t.Logf("  download %s: state=%s err=%s playlist_id=%s lib_track_id=%d",
			id, d.State, d.Error, d.PlaylistID, d.LibraryTrackID)
	}

	// ─── Step 4: Verify playlist folder ─────────────────────────────
	plDir := filepath.Join(plPath, "Test Mix")
	entries, _ := os.ReadDir(plDir)
	for _, e := range entries {
		fi, _ := e.Info()
		t.Logf("  playlist file: %s (%d bytes)", e.Name(), fi.Size())
	}
	if len(entries) < 3 {
		t.Errorf("expected 3 files in playlist dir, got %d", len(entries))
	}

	// ─── Step 5: Verify library tracks ──────────────────────────────
	tracks, _ := libStore.SearchTracks(context.Background(), "", 50)
	t.Logf("library tracks: %d", len(tracks))
	for _, tr := range tracks {
		t.Logf("  track id=%d title=%q file=%s", tr.ID, tr.Title, tr.FilePath)
	}
	if len(tracks) < 3 {
		t.Errorf("expected 3+ library tracks, got %d", len(tracks))
	}

	// ─── Step 6: Verify playlist tracks linked ──────────────────────
	pts, _ := libStore.GetPlaylistTracks(context.Background(), 1)
	linked := 0
	for _, pt := range pts {
		if pt.TrackID != nil {
			linked++
			t.Logf("  playlist track %d linked to library track %d", pt.Position, *pt.TrackID)
		} else {
			t.Logf("  playlist track %d unmatched: %s - %s", pt.Position, pt.Artist, pt.Title)
		}
	}
	if linked < 3 {
		t.Errorf("expected 3 linked playlist tracks, got %d", linked)
	}
}
