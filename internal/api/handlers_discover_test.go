package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// ─── Discover album-download handler test doubles ────────────────────

// stubDiscoverProvider is a minimal discovery.Provider returning canned
// album tracks, registered through a factory so the real registry routes it.
type stubDiscoverProvider struct {
	name   string
	tracks []discovery.TrackInfo
}

func (p *stubDiscoverProvider) Name() string        { return p.name }
func (p *stubDiscoverProvider) DisplayName() string { return p.name }
func (p *stubDiscoverProvider) IsConfigured() bool  { return true }
func (p *stubDiscoverProvider) CheckConnection(context.Context) error {
	return nil
}
func (p *stubDiscoverProvider) Connected() bool { return true }
func (p *stubDiscoverProvider) CapabilityStatus() map[string]string {
	return map[string]string{"discovery": "connected"}
}
func (p *stubDiscoverProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	return nil, nil
}
func (p *stubDiscoverProvider) GetArtistAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}
func (p *stubDiscoverProvider) SearchAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}
func (p *stubDiscoverProvider) GetAlbumTracks(context.Context, string) ([]discovery.TrackInfo, error) {
	if p.tracks == nil {
		return nil, nil // provider has no such album
	}
	return p.tracks, nil
}

var _ discovery.Provider = (*stubDiscoverProvider)(nil)

// stubDiscoverFactory wraps a stubDiscoverProvider for plugin registration.
type stubDiscoverFactory struct{ p discovery.Provider }

func (f *stubDiscoverFactory) Name() string                         { return f.p.Name() }
func (f *stubDiscoverFactory) DisplayName() string                  { return f.p.DisplayName() }
func (f *stubDiscoverFactory) Capabilities() []string               { return []string{"discovery"} }
func (f *stubDiscoverFactory) ValidateConfig(json.RawMessage) error { return nil }
func (f *stubDiscoverFactory) DefaultConfig() json.RawMessage       { return json.RawMessage(`{}`) }
func (f *stubDiscoverFactory) Create(json.RawMessage, plugin.PluginResources) (plugin.BasePlugin, error) {
	return f.p, nil
}

// stubDownloadStore records inserted download records. It embeds the
// download.Store interface and overrides only the methods the canonical
// album-acquisition policy calls.
type stubDownloadStore struct {
	download.Store
	records    []*download.Record
	failInsert bool
}

func (s *stubDownloadStore) Insert(_ context.Context, r *download.Record) error {
	if s.failInsert {
		return fmt.Errorf("insert boom")
	}
	s.records = append(s.records, r)
	return nil
}

func (s *stubDownloadStore) FindActiveByTitle(context.Context, string, string) (*download.Record, error) {
	return nil, nil
}

func (s *stubDownloadStore) FindActiveByISRC(context.Context, string) (*download.Record, error) {
	return nil, nil
}

// stubAlbumSearcher stands in for the orchestrator wired as the album searcher.
type stubAlbumSearcher struct {
	releases []domain.AlbumRelease
	err      error
	calls    int
}

func (s *stubAlbumSearcher) SearchAlbums(context.Context, string) ([]domain.AlbumRelease, error) {
	s.calls++
	return s.releases, s.err
}

// newDiscoverRegistry builds a real discovery.Registry around one stub provider.
func newDiscoverRegistry(t *testing.T, p discovery.Provider) *discovery.Registry {
	t.Helper()
	pr := plugin.NewRegistry()
	if err := pr.RegisterFactory(&stubDiscoverFactory{p: p}); err != nil {
		t.Fatalf("register discovery factory: %v", err)
	}
	if err := pr.InitAll(
		map[string]json.RawMessage{p.Name(): json.RawMessage(`{}`)},
		plugin.PluginResources{Logger: testAPILogger()},
	); err != nil {
		t.Fatalf("init discovery providers: %v", err)
	}
	return discovery.NewRegistry(pr)
}

// newDiscoverServer wires a Server with the given discovery registry and
// canonical download service. A nil searcher leaves the album-first leg
// un-wired, exactly as an unconfigured orchestrator would.
func newDiscoverServer(t *testing.T, reg *discovery.Registry, store download.Store, searcher download.AlbumSearcher, cfg *config.Persistence) *Server {
	t.Helper()
	svc := download.NewService(store, events.NewInMemoryEventBus(testAPILogger()), testAPILogger())
	if searcher != nil {
		svc.SetAlbumSearcher(searcher)
	}
	return &Server{cfg: cfg, log: testAPILogger(), discoveryReg: reg, downloadSvc: svc}
}

func sampleDiscoverTracks() []discovery.TrackInfo {
	return []discovery.TrackInfo{
		{ProviderID: "t1", ArtistName: "A", AlbumTitle: "B", Title: "T1", TrackNumber: 1, DiscNumber: 1, ISRC: "ISRC1"},
		{ProviderID: "t2", ArtistName: "A", AlbumTitle: "B", Title: "T2", TrackNumber: 2, DiscNumber: 1, ISRC: "ISRC2"},
	}
}

func newAlbumDownloadRequest(albumID, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(http.MethodPost, "/api/discover/albums/"+albumID+"/download", nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/api/discover/albums/"+albumID+"/download", strings.NewReader(body))
	}
	req.SetPathValue("id", albumID)
	return req
}

// TestHandleDiscoverAlbumDownload_AlbumMode covers the album-first shape: the
// canonical policy returns an album result and the handler emits the album JSON.
func TestHandleDiscoverAlbumDownload_AlbumMode(t *testing.T) {
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.AlbumSources = []string{"prowlarr"}
		c.DownloadClient = "qbit"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reg := newDiscoverRegistry(t, &stubDiscoverProvider{name: "stubdisco", tracks: sampleDiscoverTracks()})
	store := &stubDownloadStore{}
	searcher := &stubAlbumSearcher{releases: []domain.AlbumRelease{
		{SourceName: "prowlarr", Artist: "A", Album: "B", MagnetURI: "magnet:x"},
	}}
	s := newDiscoverServer(t, reg, store, searcher, cfg)

	rec := httptest.NewRecorder()
	s.handleDiscoverAlbumDownload(rec, newAlbumDownloadRequest("alb1", `{"artist_name":"A","album_name":"B"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if got["mode"] != "album" {
		t.Errorf("mode = %v, want album", got["mode"])
	}
	if id, _ := got["download_id"].(string); id == "" {
		t.Errorf("download_id missing/empty: %v", got["download_id"])
	}
	if got["artist"] != "A" || got["album"] != "B" {
		t.Errorf("artist/album = %v/%v, want A/B", got["artist"], got["album"])
	}
	if searcher.calls != 1 {
		t.Errorf("album search calls = %d, want 1", searcher.calls)
	}
	if len(store.records) != 1 {
		t.Fatalf("stored %d records, want 1 album record", len(store.records))
	}
	if r := store.records[0]; r.Artist != "A" || r.Album != "B" || r.DownloadClient != "qbit" {
		t.Errorf("album record = artist %q album %q client %q, want A/B/qbit", r.Artist, r.Album, r.DownloadClient)
	}
}

// TestHandleDiscoverAlbumDownload_TrackMode covers the per-track shape when no
// album source/client is configured.
func TestHandleDiscoverAlbumDownload_TrackMode(t *testing.T) {
	cfg := testPersistence(t) // no AlbumSources / DownloadClient
	reg := newDiscoverRegistry(t, &stubDiscoverProvider{name: "stubdisco", tracks: sampleDiscoverTracks()})
	store := &stubDownloadStore{}
	searcher := &stubAlbumSearcher{} // wired but must never be consulted
	s := newDiscoverServer(t, reg, store, searcher, cfg)

	rec := httptest.NewRecorder()
	s.handleDiscoverAlbumDownload(rec, newAlbumDownloadRequest("alb1", `{"artist_name":"A","album_name":"B"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if got["mode"] != "track" {
		t.Errorf("mode = %v, want track", got["mode"])
	}
	if got["queued"] != float64(2) || got["total"] != float64(2) {
		t.Errorf("queued/total = %v/%v, want 2/2", got["queued"], got["total"])
	}
	if searcher.calls != 0 {
		t.Errorf("album search calls = %d, want 0 (no album source configured)", searcher.calls)
	}
	if len(store.records) != 2 {
		t.Fatalf("stored %d records, want 2 per-track records", len(store.records))
	}
	if r := store.records[0]; r.ISRC != "ISRC1" || r.TrackNumber != 1 || r.DiscNumber != 1 {
		t.Errorf("track record metadata = %+v, want ISRC1 track 1 disc 1", r)
	}
}

// TestHandleDiscoverAlbumDownload_TrackFallback covers the shape when album
// mode is eligible but the search yields no release: the canonical policy falls
// through to per-track, and the handler still emits the track JSON.
func TestHandleDiscoverAlbumDownload_TrackFallback(t *testing.T) {
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.AlbumSources = []string{"prowlarr"}
		c.DownloadClient = "qbit"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reg := newDiscoverRegistry(t, &stubDiscoverProvider{name: "stubdisco", tracks: sampleDiscoverTracks()})
	store := &stubDownloadStore{}
	searcher := &stubAlbumSearcher{} // returns no releases
	s := newDiscoverServer(t, reg, store, searcher, cfg)

	rec := httptest.NewRecorder()
	s.handleDiscoverAlbumDownload(rec, newAlbumDownloadRequest("alb1", `{"artist_name":"A","album_name":"B"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if got["mode"] != "track" || got["queued"] != float64(2) || got["total"] != float64(2) {
		t.Errorf("response = %v, want track 2/2", got)
	}
	if searcher.calls != 1 {
		t.Errorf("album search calls = %d, want 1", searcher.calls)
	}
	if len(store.records) != 2 {
		t.Fatalf("stored %d records, want 2 per-track records", len(store.records))
	}
}

// TestHandleDiscoverAlbumDownload_AlbumQueueError preserves the 500 on a real
// album queue failure (wrapped as "queue album: ...").
func TestHandleDiscoverAlbumDownload_AlbumQueueError(t *testing.T) {
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.AlbumSources = []string{"prowlarr"}
		c.DownloadClient = "qbit"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reg := newDiscoverRegistry(t, &stubDiscoverProvider{name: "stubdisco", tracks: sampleDiscoverTracks()})
	store := &stubDownloadStore{failInsert: true}
	searcher := &stubAlbumSearcher{releases: []domain.AlbumRelease{
		{SourceName: "prowlarr", Artist: "A", Album: "B", MagnetURI: "magnet:x"},
	}}
	s := newDiscoverServer(t, reg, store, searcher, cfg)

	rec := httptest.NewRecorder()
	s.handleDiscoverAlbumDownload(rec, newAlbumDownloadRequest("alb1", `{"artist_name":"A","album_name":"B"}`))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "queue album") {
		t.Errorf("body = %s, want wrapped 'queue album' error", rec.Body.String())
	}
}

// TestHandleDiscoverAlbumDownload_AlbumNotFound preserves the 404 when the
// provider has no such album.
func TestHandleDiscoverAlbumDownload_AlbumNotFound(t *testing.T) {
	cfg := testPersistence(t)
	reg := newDiscoverRegistry(t, &stubDiscoverProvider{name: "stubdisco"}) // no tracks
	store := &stubDownloadStore{}
	s := newDiscoverServer(t, reg, store, &stubAlbumSearcher{}, cfg)

	rec := httptest.NewRecorder()
	s.handleDiscoverAlbumDownload(rec, newAlbumDownloadRequest("missing", ""))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleDiscoverAlbumDownload_NoProviders preserves the "no discovery
// providers configured" response.
func TestHandleDiscoverAlbumDownload_NoProviders(t *testing.T) {
	cfg := testPersistence(t)
	store := &stubDownloadStore{}
	s := newDiscoverServer(t, nil, store, &stubAlbumSearcher{}, cfg)

	rec := httptest.NewRecorder()
	s.handleDiscoverAlbumDownload(rec, newAlbumDownloadRequest("alb1", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Mode   string   `json:"mode"`
		Queued int      `json:"queued"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if got.Mode != "track" || got.Queued != 0 || len(got.Errors) != 1 {
		t.Errorf("response = %+v, want track/0/one error", got)
	}
	if len(store.records) != 0 {
		t.Errorf("stored %d records, want 0", len(store.records))
	}
}
