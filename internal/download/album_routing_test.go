package download

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// TestAlbumVsTrackRouting verifies the MonitoringService dispatches
// album records to DownloadClient and track records to MonitoredProvider.
func TestAlbumVsTrackRouting(t *testing.T) {
	store := newAlbumTrackStore()
	bus := events.NewInMemoryEventBus(nil)

	// Track plugin: MonitoredProvider with StartDownload/GetStatus.
	trackPlugin := &albumTrackMock{name: "mock", dlPath: t.TempDir()}

	// Download client: handles album URI downloads.
	albumClient := &albumClientMock{name: "qbittorrent", dlPath: t.TempDir()}

	// Build registries.
	pluginReg := plugin.NewRegistry()
	pluginReg.RegisterFactory(&albumClientFactory{client: albumClient})
	_ = pluginReg.InitAll(map[string]json.RawMessage{
		"qbittorrent": json.RawMessage(`{}`),
	}, plugin.PluginResources{})
	clientReg := NewDownloadClientRegistry(pluginReg)

	reg := NewRegistry()
	_ = reg.Inner().Register(trackPlugin)

	// Queue one album and one track record.
	svc := NewService(store, bus, nil)
	albumID, err := svc.QueueAlbum(context.Background(), domain.AlbumRelease{
		SourceName: "prowlarr", Artist: "Metallica", Album: "Test Album",
		MagnetURI: "magnet:?xt=urn:test", AlbumType: "Album",
	}, nil, "qbittorrent")
	if err != nil {
		t.Fatalf("QueueAlbum: %v", err)
	}

	trackID, err := svc.Queue(context.Background(), "mock", "user", "test.flac", 1000, Meta{Artist: "Test", Title: "Song"})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}

	// Create monitor with both registries.
	monitor := NewMonitoringService(store, reg, clientReg, t.TempDir(), bus, nil)
	monitor.ctx, monitor.cancel = context.WithCancel(context.Background())
	defer monitor.Shutdown()

	// Simulate one dispatch tick.
	monitor.startQueuedDownloads()

	// Verify album was routed to the download client.
	albumClient.mu.Lock()
	albumCalled := len(albumClient.downloads) > 0
	albumClient.mu.Unlock()
	if !albumCalled {
		t.Error("album record was not dispatched to DownloadClient")
	}

	// Verify track was routed to the monitored provider.
	trackPlugin.mu.Lock()
	trackCalled := len(trackPlugin.downloads) > 0
	trackPlugin.mu.Unlock()
	if !trackCalled {
		t.Error("track record was not dispatched to MonitoredProvider")
	}

	// Verify the records themselves.
	ar, _ := store.Get(context.Background(), albumID)
	if ar == nil || !ar.IsAlbum() {
		t.Error("album record missing or not flagged as album")
	}

	tr, _ := store.Get(context.Background(), trackID)
	if tr == nil || tr.IsAlbum() {
		t.Error("track record missing or incorrectly flagged as album")
	}

	_ = trackID
	_ = ar
	_ = tr
}

// ─── mocks ──────────────────────────────────────────────────────

type albumTrackStore struct {
	mu      sync.Mutex
	records map[string]*Record
}

func newAlbumTrackStore() *albumTrackStore {
	return &albumTrackStore{records: make(map[string]*Record)}
}

func (s *albumTrackStore) Insert(_ context.Context, r *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	s.records[r.ID] = &cp
	return nil
}

func (s *albumTrackStore) Update(_ context.Context, r *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ex, ok := s.records[r.ID]; ok {
		ex.State = r.State
		ex.FilePath = r.FilePath
		ex.FolderPath = r.FolderPath
		ex.ImportedTrackIDs = r.ImportedTrackIDs
	}
	return nil
}

func (s *albumTrackStore) UpdateProgress(_ context.Context, id string, state State, progress float64, size, transferred, speed int64, filePath, coverURL string) error {
	return nil
}

func (s *albumTrackStore) TransitionState(_ context.Context, id string, oldState, newState State) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[id]; ok && r.State == oldState {
		r.State = newState
		return true, nil
	}
	return false, nil
}

func (s *albumTrackStore) Get(_ context.Context, id string) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (s *albumTrackStore) ListByState(_ context.Context, state State) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, r := range s.records {
		if r.State == state {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (s *albumTrackStore) List(_ context.Context) ([]Record, error)               { return nil, nil }
func (s *albumTrackStore) ListActive(_ context.Context) ([]Record, error)          { return nil, nil }
func (s *albumTrackStore) ListByPlaylist(_ context.Context, pid string) ([]Record, error) { return nil, nil }
func (s *albumTrackStore) FindActiveByTitle(_ context.Context, artist, title string) (*Record, error) {
	return nil, nil
}
func (s *albumTrackStore) RecordEvent(_ context.Context, event *Event) error       { return nil }
func (s *albumTrackStore) GetEvents(_ context.Context, id string) ([]Event, error)  { return nil, nil }
func (s *albumTrackStore) Delete(ctx context.Context, id string) error {
	delete(s.records, id)
	return nil
}

func (s *albumTrackStore) DeleteTerminal(_ context.Context) error                  { return nil }
func (s *albumTrackStore) Close() error                                            { return nil }

// ─── album client mock (DownloadClient) ──────────────────────

type albumClientMock struct {
	name      string
	dlPath    string
	mu        sync.Mutex
	downloads map[string]string
	status    map[string]*Record
}

func (m *albumClientMock) Name() string                              { return m.name }
func (m *albumClientMock) DisplayName() string                       { return m.name }
func (m *albumClientMock) IsConfigured() bool                        { return true }
func (m *albumClientMock) Connected() bool                           { return false }
func (m *albumClientMock) CapabilityStatus() map[string]string       { return nil }
func (m *albumClientMock) CheckConnection(_ context.Context) error   { return nil }
func (m *albumClientMock) AddDownload(_ context.Context, uri, category, savepath string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.downloads == nil {
		m.downloads = make(map[string]string)
	}
	id := "hash-" + uri[len(uri)-4:]
	m.downloads[id] = savepath
	return id, nil
}
func (m *albumClientMock) GetStatus(_ context.Context, providerID string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &Record{ID: providerID, State: StateDownloading, Progress: 50}, nil
}
func (m *albumClientMock) GetProgress(_ context.Context, providerID string) (*Progress, error) {
	return &Progress{Transferred: 50000, Total: 100000, Speed: 1000}, nil
}
func (m *albumClientMock) Cancel(_ context.Context, providerID string, remove bool) error { return nil }
func (m *albumClientMock) MaxConcurrent() int           { return 1 }
func (m *albumClientMock) DownloadTimeout() time.Duration { return 10 * time.Minute }
func (m *albumClientMock) DownloadBasePath() string      { return m.dlPath }

type albumClientFactory struct {
	client *albumClientMock
}

func (f *albumClientFactory) Name() string                                          { return "qbittorrent" }
func (f *albumClientFactory) DisplayName() string                                   { return "qBittorrent" }
func (f *albumClientFactory) Capabilities() []string                                { return []string{"download"} }
func (f *albumClientFactory) Create(raw json.RawMessage, r plugin.PluginResources) (plugin.BasePlugin, error) {
	return f.client, nil
}
func (f *albumClientFactory) ValidateConfig(raw json.RawMessage) error             { return nil }
func (f *albumClientFactory) DefaultConfig() json.RawMessage                       { return json.RawMessage(`{}`) }

// ─── track mock (MonitoredProvider) ────────────────────────

type albumTrackMock struct {
	name      string
	dlPath    string
	mu        sync.Mutex
	downloads map[string]string
}

func (m *albumTrackMock) Name() string                           { return m.name }
func (m *albumTrackMock) DisplayName() string                    { return m.name }
func (m *albumTrackMock) IsConfigured() bool                     { return true }
func (m *albumTrackMock) Connected() bool                        { return false }
func (m *albumTrackMock) CapabilityStatus() map[string]string    { return nil }
func (m *albumTrackMock) CheckConnection(_ context.Context) error { return nil }
func (m *albumTrackMock) Search(_ context.Context, q string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	return nil, nil, nil
}
func (m *albumTrackMock) StartDownload(_ context.Context, meta Meta) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.downloads == nil {
		m.downloads = make(map[string]string)
	}
	id := "track-" + meta.Filename
	m.downloads[id] = meta.Filename
	return id, nil
}
func (m *albumTrackMock) GetStatus(_ context.Context, id string) (*Record, error) {
	return &Record{ID: id, State: StateDownloading}, nil
}
func (m *albumTrackMock) GetProgress(_ context.Context, id string) (*Progress, error) {
	return nil, nil
}
func (m *albumTrackMock) Cancel(_ context.Context, id string, remove bool) error    { return nil }
func (m *albumTrackMock) ActiveDownloads() []string                                { return nil }
func (m *albumTrackMock) MaxConcurrent() int                                       { return 1 }
func (m *albumTrackMock) DownloadTimeout() time.Duration                            { return 10 * time.Minute }

