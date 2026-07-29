package download

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/plugin"
)

func TestRecordIsAlbum(t *testing.T) {
	tests := []struct {
		name      string
		albumType string
		want      bool
	}{
		{"empty string", "", false},
		{"Album", "Album", true},
		{"Compilation", "Compilation", true},
		{"EP", "EP", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Record{AlbumType: tt.albumType}
			if got := r.IsAlbum(); got != tt.want {
				t.Errorf("IsAlbum() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecordIsCompilation(t *testing.T) {
	tests := []struct {
		name       string
		albumType  string
		tracks     []domain.ExpectedTrack
		want       bool
	}{
		{"explicit compilation", "compilation", nil, true},
		{"explicit Compilation", "Compilation", nil, true},
		{"single artist", "Album", []domain.ExpectedTrack{
			{Artist: "Metallica"}, {Artist: "Metallica"}}, false},
		{"multi artist VA", "Album", []domain.ExpectedTrack{
			{Artist: "Angerfist"}, {Artist: "Miss K8"}}, true},
		{"empty artist rows (same as album)", "Album", []domain.ExpectedTrack{
			{Artist: ""}, {Artist: ""}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Record{AlbumType: tt.albumType, AlbumTracks: tt.tracks}
			if got := r.IsCompilation(); got != tt.want {
				t.Errorf("IsCompilation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractTrackNumber(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     int
	}{
		{"standard dash", "01 - Title.flac", 1},
		{"dot separator", "1. Title.flac", 1},
		{"space separator", "01 Title.flac", 1},
		{"underscore", "03_Title.flac", 3},
		{"two digits", "12 - Song.flac", 12},
		{"no number", "Title.flac", 0},
		{"numeric title 1979", "1979 - Smashing Pumpkins.flac", 0},
		{"numeric title 99 red", "99 Red Balloons.flac", 99},
		{"track 100", "100 - Deep Cut.flac", 0},
		{"track zero", "0 - Intro.flac", 0},
		{"three digits prefix then space", "001 - Title.flac", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractTrackNumber(tt.filename); got != tt.want {
				t.Errorf("extractTrackNumber(%q) = %d, want %d", tt.filename, got, tt.want)
			}
		})
	}
}

func TestDownloadClientRegistry(t *testing.T) {
	reg := NewDownloadClientRegistry()

	// Empty registry.
	if dc := reg.Get("nonexistent"); dc != nil {
		t.Error("expected nil for unregistered client")
	}

	// Register and retrieve.
	f := &stubDC{}
	reg.RegisterFactory(f)
	if err := reg.InitAll(map[string]json.RawMessage{"stub": json.RawMessage(`{}`)}, plugin.PluginResources{}); err != nil {
		t.Fatalf("InitAll: %v", err)
	}
	if dc := reg.Get("stub"); dc == nil {
		t.Error("expected client after registration")
	}
}

type stubDC struct{}

func (s *stubDC) Name() string                                      { return "stub" }
func (s *stubDC) DisplayName() string                               { return "Stub" }
func (s *stubDC) Capabilities() []string                            { return []string{"download"} }
func (s *stubDC) Create(raw json.RawMessage, r plugin.PluginResources) (plugin.BasePlugin, error) { return &stubDCPlugin{}, nil }
func (s *stubDC) ValidateConfig(raw json.RawMessage) error         { return nil }
func (s *stubDC) DefaultConfig() json.RawMessage                   { return json.RawMessage(`{}`) }

type stubDCPlugin struct{}

func (s *stubDCPlugin) Name() string           { return "stub" }
func (s *stubDCPlugin) DisplayName() string    { return "Stub" }
func (s *stubDCPlugin) IsConfigured() bool     { return true }
func (s *stubDCPlugin) CheckConnection(ctx context.Context) error { return nil }
func (s *stubDCPlugin) Connected() bool        { return false }
func (s *stubDCPlugin) CapabilityStatus() map[string]string { return nil }
func (s *stubDCPlugin) AddDownload(ctx context.Context, uri, category, savepath string) (string, error) { return "", nil }
func (s *stubDCPlugin) GetStatus(ctx context.Context, providerID string) (*Record, error) { return nil, nil }
func (s *stubDCPlugin) GetProgress(ctx context.Context, providerID string) (*Progress, error) { return nil, nil }
func (s *stubDCPlugin) Cancel(ctx context.Context, providerID string, remove bool) error { return nil }
func (s *stubDCPlugin) MaxConcurrent() int { return 0 }
func (s *stubDCPlugin) DownloadTimeout() time.Duration { return 0 }

func TestQueueAlbum_CreatesAlbumRecord(t *testing.T) {
	store := newMockAlbumStore()
	bus := events.NewInMemoryEventBus(nil)
	svc := NewService(store, bus, nil)

	release := domain.AlbumRelease{
		SourceName: "prowlarr",
		Artist:     "Metallica",
		Album:      "Master of Puppets",
		Year:       1986,
		MagnetURI:  "magnet:?xt=urn:btih:ABC",
		Size:       350_000_000,
		AlbumType:  "Album",
	}
	tracks := []domain.ExpectedTrack{
		{TrackNumber: 1, Artist: "Metallica", Title: "Battery"},
		{TrackNumber: 2, Artist: "Metallica", Title: "Master of Puppets"},
	}

	id, err := svc.QueueAlbum(context.Background(), release, tracks, "qbittorrent")
	if err != nil {
		t.Fatalf("QueueAlbum: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	rec, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !rec.IsAlbum() {
		t.Error("expected IsAlbum() = true")
	}
	if rec.AlbumType != "Album" {
		t.Errorf("AlbumType = %q, want Album", rec.AlbumType)
	}
	if rec.Artist != "Metallica" {
		t.Errorf("Artist = %q, want Metallica", rec.Artist)
	}
	if rec.Album != "Master of Puppets" {
		t.Errorf("Album = %q, want Master of Puppets", rec.Album)
	}
	if rec.MagnetURI != "magnet:?xt=urn:btih:ABC" {
		t.Errorf("MagnetURI = %q", rec.MagnetURI)
	}
	if rec.DownloadClient != "qbittorrent" {
		t.Errorf("DownloadClient = %q, want qbittorrent", rec.DownloadClient)
	}
	if len(rec.AlbumTracks) != 2 {
		t.Errorf("AlbumTracks count = %d, want 2", len(rec.AlbumTracks))
	}
	if rec.Title != "Master of Puppets" { // used for dedup
		t.Errorf("Title = %q, want Master of Puppets (for dedup)", rec.Title)
	}
}

func TestQueueAlbum_DefaultsAlbumType(t *testing.T) {
	store := newMockAlbumStore()
	bus := events.NewInMemoryEventBus(nil)
	svc := NewService(store, bus, nil)

	release := domain.AlbumRelease{SourceName: "prowlarr", Artist: "Test", Album: "Test"}
	id, err := svc.QueueAlbum(context.Background(), release, nil, "qbittorrent")
	if err != nil {
		t.Fatalf("QueueAlbum: %v", err)
	}
	rec, _ := store.Get(context.Background(), id)
	if rec.AlbumType != "Album" {
		t.Errorf("default AlbumType = %q, want Album", rec.AlbumType)
	}
}

func TestQueueAlbum_Dedup(t *testing.T) {
	store := newMockAlbumStore()
	bus := events.NewInMemoryEventBus(nil)
	svc := NewService(store, bus, nil)

	release := domain.AlbumRelease{SourceName: "prowlarr", Artist: "Metallica", Album: "Ride the Lightning"}
	id1, _ := svc.QueueAlbum(context.Background(), release, nil, "qbittorrent")
	id2, _ := svc.QueueAlbum(context.Background(), release, nil, "qbittorrent")

	if id1 != id2 {
		t.Error("expected dedup to return same ID")
	}
}

// mockAlbumStore extends the existing mock approach for album-specific fields.
type mockAlbumStore struct {
	records map[string]*Record
}

func newMockAlbumStore() *mockAlbumStore {
	return &mockAlbumStore{records: make(map[string]*Record)}
}

func (m *mockAlbumStore) Insert(_ context.Context, r *Record) error {
	cp := *r
	m.records[r.ID] = &cp
	return nil
}
func (m *mockAlbumStore) Update(_ context.Context, r *Record) error {
	if ex, ok := m.records[r.ID]; ok {
		ex.State = r.State
		ex.FilePath = r.FilePath
		ex.ImportedTrackIDs = r.ImportedTrackIDs
	}
	return nil
}
func (m *mockAlbumStore) UpdateProgress(_ context.Context, id string, state State, progress float64, size, transferred, speed int64, filePath, coverURL string) error {
	return nil
}
func (m *mockAlbumStore) TransitionState(_ context.Context, id string, oldState, newState State) (bool, error) {
	return true, nil
}
func (m *mockAlbumStore) Get(_ context.Context, id string) (*Record, error) {
	r, ok := m.records[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}
func (m *mockAlbumStore) List(_ context.Context) ([]Record, error)                    { return nil, nil }
func (m *mockAlbumStore) ListByState(_ context.Context, state State) ([]Record, error) { return nil, nil }
func (m *mockAlbumStore) ListActive(_ context.Context) ([]Record, error)              { return nil, nil }
func (m *mockAlbumStore) ListByPlaylist(_ context.Context, playlistID string) ([]Record, error) { return nil, nil }
func (m *mockAlbumStore) FindActiveByTitle(_ context.Context, artist, title string) (*Record, error) {
	for _, r := range m.records {
		if r.Artist == artist && r.Title == title && !r.State.Terminal() {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}
func (m *mockAlbumStore) RecordEvent(_ context.Context, event *Event) error { return nil }
func (m *mockAlbumStore) GetEvents(_ context.Context, downloadID string) ([]Event, error) { return nil, nil }
func (m *mockAlbumStore) DeleteTerminal(_ context.Context) error           { return nil }
func (m *mockAlbumStore) Close() error                                     { return nil }

