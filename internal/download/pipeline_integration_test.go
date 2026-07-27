package download_test

import (
	"context"
	"io"
	"log/slog"
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
	"github.com/ramonskie/groovearr/internal/sanitize"
)

// ─── Mock download plugin ────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type mockDLPlugin struct {
	mu      sync.Mutex
	records map[string]*download.Record
	dlPath  string
}

func newMockDLPlugin(dlPath string) *mockDLPlugin {
	return &mockDLPlugin{records: make(map[string]*download.Record), dlPath: dlPath}
}

func (m *mockDLPlugin) Name() string        { return "mock" }
func (m *mockDLPlugin) DisplayName() string { return "Mock" }
func (m *mockDLPlugin) IsConfigured() bool  { return true }
func (m *mockDLPlugin) Connected() bool     { return true }
func (m *mockDLPlugin) CapabilityStatus() map[string]string {
	return map[string]string{"download": "connected"}
}
func (m *mockDLPlugin) CheckConnection(context.Context) error { return nil }

func (m *mockDLPlugin) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	results := []domain.TrackResult{
		{SearchResult: domain.SearchResult{Username: "mock", Filename: "Take on Me.flac", Size: 28_000_000, Quality: "flac"}, Artist: "a-ha", Title: "Take on Me", Album: "Hunting High and Low"},
		{SearchResult: domain.SearchResult{Username: "mock", Filename: "Billie Jean.flac", Size: 30_000_000, Quality: "flac"}, Artist: "Michael Jackson", Title: "Billie Jean", Album: "Thriller"},
		{SearchResult: domain.SearchResult{Username: "mock", Filename: "Bohemian Rhapsody.flac", Size: 35_000_000, Quality: "flac"}, Artist: "Queen", Title: "Bohemian Rhapsody", Album: "A Night at the Opera"},
	}
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

// ─── MonitoredProvider implementation ──────────────────────────────

func (m *mockDLPlugin) StartDownload(ctx context.Context, meta download.Meta) (string, error) {
	filename := meta.Filename
	if filename == "" {
		filename = meta.Title
	}
	pluginID := "mock-dl-" + filename[:min(8, len(filename))]
	m.mu.Lock()
	m.records[pluginID] = &download.Record{
		ID: pluginID, State: download.StateDownloading, Filename: filename,
		Size: 100_000, Transferred: 0, Progress: 0,
	}
	m.mu.Unlock()

	go func() {
		time.Sleep(100 * time.Millisecond)
		outPath := filepath.Join(m.dlPath, filename)
		_ = os.WriteFile(outPath, validFLAC(), 0o644)

		// Simulate Deezer plugin's GetTrack enrichment: set metadata
		// before marking as DownloadImported so the monitor syncs it.
		meta := trackMeta(filename)
		m.mu.Lock()
		r := m.records[pluginID]
		r.State = download.StateImported
		r.Progress, r.FilePath = 100, outPath
		r.Transferred, r.Size = int64(len(validFLAC())), int64(len(validFLAC()))
		r.CoverURL, r.Artist, r.Album, r.Title = meta.coverURL, meta.artist, meta.album, meta.title
		m.mu.Unlock()
	}()
	return pluginID, nil
}

func (m *mockDLPlugin) GetStatus(ctx context.Context, providerID string) (*download.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[providerID]
	if !ok {
		return nil, os.ErrNotExist
	}
	cp := *r
	return &cp, nil
}

func (m *mockDLPlugin) GetProgress(ctx context.Context, providerID string) (*download.Progress, error) {
	return nil, nil
}

func (m *mockDLPlugin) Cancel(ctx context.Context, providerID string, remove bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.records[providerID]; ok {
		r.State = download.StateIgnored
	}
	return nil
}

func (m *mockDLPlugin) ActiveDownloads() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.records))
	for id := range m.records {
		ids = append(ids, id)
	}
	return ids
}

func (m *mockDLPlugin) MaxConcurrent() int             { return 0 }
func (m *mockDLPlugin) DownloadTimeout() time.Duration { return 30 * time.Second }

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

type metaInfo struct{ coverURL, artist, album, title string }

func trackMeta(filename string) metaInfo {
	switch filename {
	case "Take on Me.flac":
		return metaInfo{"local://cover", "a-ha", "Hunting High and Low", "Take on Me"}
	case "Billie Jean.flac":
		return metaInfo{"local://cover", "Michael Jackson", "Thriller", "Billie Jean"}
	case "Bohemian Rhapsody.flac":
		return metaInfo{"local://cover", "Queen", "A Night at the Opera", "Bohemian Rhapsody"}
	}
	return metaInfo{}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// validFLAC returns a minimal valid FLAC file that go-flac v2 can parse.
// 2 channels, 16-bit, 44100 Hz, 4096 silent samples.
func validFLAC() []byte {
	var flac []byte
	flac = append(flac, 'f', 'L', 'a', 'C') // magic

	// STREAMINFO block: last-block + type(0) + length(34).
	streaminfo := make([]byte, 38)
	streaminfo[0] = 0x80 // is_last = 1, type = 0
	streaminfo[1] = 0x00 // length[23:16]
	streaminfo[2] = 0x00 // length[15:8]
	streaminfo[3] = 0x22 // length[7:0] = 34
	// min block size = 4096
	streaminfo[4] = 0x10
	streaminfo[5] = 0x00
	// max block size = 4096
	streaminfo[6] = 0x10
	streaminfo[7] = 0x00
	// min frame size = 0
	// max frame size = 0
	// sample rate = 44100
	streaminfo[10] = 0x0A
	streaminfo[11] = 0xC4
	streaminfo[12] = 0x42
	// channels = 2, bps = 16, total samples = 4096
	streaminfo[13] = 0x2F // 2 ch (3 bits) << 4 | 16 bps (5 bits) → 0010 1111
	streaminfo[14] = 0x00
	streaminfo[15] = 0x00
	streaminfo[16] = 0x10
	streaminfo[17] = 0x00
	// MD5: 00... (not checked by go-flac for metadata-only parse)
	flac = append(flac, streaminfo...)

	// Audio frame: sync + header + CRC-8 + 2 subframes (constant 0), padded.
	// Frame: 0xFFF8 0008 00 [crc8] [subframe0] [subframe1] [padding]
	frame := []byte{
		0xFF, 0xF8, // sync 0x3FFE, reserved=0, blocking=0 (fixed)
		0x00, 0x08, // block=0000(streaminfo), sr=0000(streaminfo), ch=0001(2ch), bps=0100(16), rsv=0
		0x00, // frame number = 0 (UTF-8)
		0x00, // CRC-8 placeholder (go-flac v2 doesn't verify CRC during parse)
	}
	// Subframe 0 (left): constant 0
	frame = append(frame, 0x20, 0x00, 0x00) // type=constant (000010), wasted=0, value=0 (16-bit signed)
	// Subframe 1 (right): constant 0
	frame = append(frame, 0x20, 0x00, 0x00)
	// Pad to byte boundary (already aligned)
	flac = append(flac, frame...)

	return flac
}

// validJPEG returns a minimal valid JPEG (1x1 white pixel).
func validJPEG() []byte {
	return []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xDB, 0x00, 0x43, 0x00, // DQT marker + length
		0x10, 0x0B, 0x0C, 0x0E, 0x0C, 0x0A, 0x10, 0x0E,
		0x0E, 0x0E, 0x12, 0x12, 0x10, 0x14, 0x18, 0x28,
		0x1A, 0x18, 0x16, 0x16, 0x18, 0x32, 0x24, 0x26,
		0x1E, 0x28, 0x3A, 0x34, 0x3E, 0x3C, 0x3A, 0x34,
		0x38, 0x38, 0x40, 0x48, 0x5C, 0x4E, 0x40, 0x44,
		0x58, 0x46, 0x38, 0x38, 0x50, 0x6E, 0x52, 0x58,
		0x60, 0x62, 0x68, 0x68, 0x68, 0x3E, 0x4E, 0x72,
		0x7A, 0x70, 0x64, 0x78, 0x5C, 0x66, 0x68, 0x64,
		0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00, // SOF0
		0xFF, 0xC4, 0x00, 0x1F, 0x00, 0x00, 0x01, 0x05, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, // DHT
		0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00, 0x3F, 0x00, // SOS
		0xFF, 0xD9, // EOI
	}
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

	// Pre-create cover.jpg in each album directory.
	// CoverArtHandler will skip (cover already exists).
	// TagWriterHandler will embed cover into FLAC files.
	albums := []struct{ artist, album string }{
		{"a-ha", "Hunting High and Low"},
		{"Michael Jackson", "Thriller"},
		{"Queen", "A Night at the Opera"},
	}
	jpg := validJPEG()
	for _, a := range albums {
		dir := filepath.Join(libPath, sanitize.PathSegment(a.artist), sanitize.PathSegment(a.album))
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "cover.jpg"), jpg, 0o644)
	}

	// Real SQLite stores.
	libStore, err := libsqlite.New(filepath.Join(tmpDir, "library.db"), testLogger())
	if err != nil {
		t.Fatalf("lib store: %v", err)
	}
	defer libStore.Close()
	dlStore := dlsqlite.New(libStore.DB(), testLogger())

	// Mock plugin + registry.
	mockPlugin := newMockDLPlugin(dlPath)
	reg := download.NewRegistry()
	if err := reg.Register(mockPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Event bus.
	eventBus := events.NewInMemoryEventBus(testLogger())

	// Download service — MonitoringService handles dispatch externally.
	downloadSvc := download.NewDownloadService(dlStore, eventBus, testLogger())

	// Monitoring service — drives the state machine by polling providers.
	monitor := download.NewMonitoringService(dlStore, reg, eventBus, testLogger())
	monitor.Start(context.Background())
	defer monitor.Shutdown()

	// Renamer: library path.
	renamer := library.NewRenamer("{artist}/{album}/{title}", libPath, nil)

	// Import handler chain.
	_ = download.NewCompletedDownloadService(
		dlStore, eventBus, testLogger(),
		download.NewFileRenamerHandler(renamer, dlStore, nil),
		download.NewCoverArtHandler(libStore, testLogger()),
		download.NewTagWriterHandler(testLogger()),
		download.NewLibraryImporterHandler(libStore, nil),
		download.NewPlaylistLinkerHandler(libStore, testLogger()),
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
	plSvc := playlist.NewService(plReg, libStore, reg, downloadSvc, func() config.Config { return cfg }, nil, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// ─── Step 1: Import playlist ────────────────────────────────────
	result, err := plSvc.ImportPlaylist(context.Background(), "mock", "pl-1")
	if err != nil {
		t.Fatalf("import playlist: %v", err)
	}
	t.Logf("imported: %d linked, %d unmatched", result.Linked, result.Unmatched)
	if result.Unmatched != 3 {
		t.Fatalf("expected 3 unmatched, got %d", result.Unmatched)
	}

	// ─── Step 2: Wait for downloads + sync ──────────────────────────
	deadline := time.Now().Add(30 * time.Second)
	var allTerminal, folderReady bool
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)

		downloads, _ := downloadSvc.List(context.Background())
		pending := 0
		for _, d := range downloads {
			if !d.State.Terminal() {
				pending++
			}
		}
		allTerminal = pending == 0 && len(downloads) > 0

		entries, _ := os.ReadDir(filepath.Join(plPath, "Test Mix"))
		folderReady = len(entries) >= 3

		if allTerminal && folderReady {
			t.Logf("ready: %d downloads terminal, %d playlist files", len(downloads), len(entries))
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
		entries, _ := os.ReadDir(filepath.Join(plPath, "Test Mix"))
		t.Errorf("playlist folder not ready: %d files (want 3)", len(entries))
	}

	// ─── Step 3: Verify download states + metadata sync ─────────────
	downloads, _ := downloadSvc.List(context.Background())
	for _, d := range downloads {
		t.Logf("  download %s: state=%s cover_url=%s artist=%s album=%s",
			d.ID[:min(30, len(d.ID))], d.State,
			truncate(d.CoverURL, 20), d.Artist, d.Album)
		if d.CoverURL == "" {
			t.Errorf("download %s: CoverURL not synced from plugin to store", d.ID)
		}
	}

	// ─── Step 4: Verify playlist folder ─────────────────────────────
	plDir := filepath.Join(plPath, "Test Mix")
	entries, _ := os.ReadDir(plDir)
	for _, e := range entries {
		fi, _ := e.Info()
		t.Logf("  playlist: %s (%d bytes)", e.Name(), fi.Size())
	}
	if len(entries) < 3 {
		t.Errorf("expected 3 playlist files, got %d", len(entries))
	}

	// ─── Step 5: Verify library tracks ──────────────────────────────
	tracks, _ := libStore.SearchTracks(context.Background(), "", 50)
	t.Logf("library: %d tracks", len(tracks))
	for _, tr := range tracks {
		t.Logf("  library: id=%d %q", tr.ID, tr.Title)
	}
	if len(tracks) < 3 {
		t.Errorf("expected 3 library tracks, got %d", len(tracks))
	}

	// ─── Step 6: Verify playlist links ──────────────────────────────
	pts, _ := libStore.GetPlaylistTracks(context.Background(), 1)
	linked := 0
	for _, pt := range pts {
		if pt.TrackID != nil {
			linked++
			t.Logf("  link: pos=%d → track %d", pt.Position, *pt.TrackID)
		}
	}
	if linked < 3 {
		t.Errorf("expected 3 linked, got %d", linked)
	}

	// ─── Step 7: Verify cover art ───────────────────────────────────
	rawSize := int64(len(validFLAC()))
	coverSize := int64(len(jpg))
	for _, tr := range tracks {
		fi, err := os.Stat(tr.FilePath)
		if err != nil {
			t.Errorf("stat %s: %v", tr.FilePath, err)
			continue
		}
		// After tag writer embeds cover + vorbis tags, file should be larger.
		if fi.Size() <= rawSize+coverSize/2 {
			t.Errorf("%q: size=%d (raw=%d + cover=%d) — cover not embedded",
				tr.Title, fi.Size(), rawSize, coverSize)
		} else {
			t.Logf("  cover ✓ %q: %d bytes (raw=%d + cover=%d embedded)",
				tr.Title, fi.Size(), rawSize, coverSize)
		}
	}

	// ─── Step 8: Playlist files inherit covers ──────────────────────
	for _, e := range entries {
		path := filepath.Join(plDir, e.Name())
		fi, _ := os.Stat(path)
		if fi.Size() <= rawSize+coverSize/2 {
			t.Errorf("playlist %q: size=%d — cover not inherited", e.Name(), fi.Size())
		} else {
			t.Logf("  playlist cover ✓ %q: %d bytes", e.Name(), fi.Size())
		}
	}
}
