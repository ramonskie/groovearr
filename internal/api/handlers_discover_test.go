package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/user"
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
func (p *stubDiscoverProvider) SearchAlbums(context.Context, discovery.AlbumQuery, int) ([]discovery.AlbumResult, error) {
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

// TestHandleDiscoverAlbumDownload_CarriesRequester verifies the authenticated
// caller's user id is threaded from Identity into the persisted per-track
// records for DB-only attribution.
func TestHandleDiscoverAlbumDownload_CarriesRequester(t *testing.T) {
	cfg := testPersistence(t) // per-track path: no album source/client configured
	reg := newDiscoverRegistry(t, &stubDiscoverProvider{name: "stubdisco", tracks: sampleDiscoverTracks()})
	store := &stubDownloadStore{}
	s := newDiscoverServer(t, reg, store, &stubAlbumSearcher{}, cfg)

	req := newAlbumDownloadRequest("alb1", `{"artist_name":"A","album_name":"B"}`)
	req = req.WithContext(contextWithIdentity(req.Context(), Identity{
		UserID:   99,
		Username: "bob",
		Role:     user.RoleUser,
	}))

	rec := httptest.NewRecorder()
	s.handleDiscoverAlbumDownload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(store.records) != 2 {
		t.Fatalf("stored %d records, want 2 per-track records", len(store.records))
	}
	for _, r := range store.records {
		if r.RequestedByUserID != 99 {
			t.Errorf("record %q RequestedByUserID = %d, want 99", r.ID, r.RequestedByUserID)
		}
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

// ─── Discover artist-overview read path (catalog-backed) ─────────────

// stubArtistCatalog implements the discover artist read slice of catalog.Store.
// It serves canned warm data and records every materialization call so a test
// can prove the cold path wrote the materialized result, not a JSON blob.
type stubArtistCatalog struct {
	catalog.ArtistMetadataStore
	catalog.ArtistDiscographyStore
	meta       *catalog.ArtistMetadata
	releases   []catalog.ArtistDiscographyRelease
	getMetaErr error
	upserts    []catalog.ArtistMetadata
	replaced   []catalog.ArtistDiscographyRelease
	replacedID []int64
}

func (c *stubArtistCatalog) GetArtistMetadata(context.Context, int64) (*catalog.ArtistMetadata, error) {
	return c.meta, c.getMetaErr
}

func (c *stubArtistCatalog) UpsertArtistMetadata(_ context.Context, m catalog.ArtistMetadata) error {
	c.upserts = append(c.upserts, m)
	return nil
}

func (c *stubArtistCatalog) GetArtistDiscography(context.Context, int64) ([]catalog.ArtistDiscographyRelease, error) {
	return c.releases, nil
}

func (c *stubArtistCatalog) ReplaceArtistDiscography(_ context.Context, artistID int64, releases []catalog.ArtistDiscographyRelease) error {
	c.replacedID = append(c.replacedID, artistID)
	c.replaced = releases
	return nil
}

var _ artistCatalogReader = (*stubArtistCatalog)(nil)

// stubArtistLookupStore embeds the library.Store interface and overrides only
// the name lookup the discover overview path calls. Any other method panics,
// proving the handler touches nothing beyond GetArtistByName.
type stubArtistLookupStore struct {
	library.Store
	artists []domain.Artist
}

func (s *stubArtistLookupStore) GetArtistByName(context.Context, string) (*domain.Artist, error) {
	if len(s.artists) == 0 {
		return nil, nil
	}
	return &s.artists[0], nil
}

var _ library.Store = (*stubArtistLookupStore)(nil)

// recordingArtistProvider is a discovery.Provider + TopTrackProvider that
// records its calls and returns canned artist/album/track data.
type recordingArtistProvider struct {
	name        string
	searchCalls int
	albumCalls  int
	topCalls    int
	artist      discovery.ArtistSummary
	albums      []discovery.AlbumResult
	tracks      []discovery.TrackInfo
}

func (p *recordingArtistProvider) Name() string                          { return p.name }
func (p *recordingArtistProvider) DisplayName() string                   { return p.name }
func (p *recordingArtistProvider) IsConfigured() bool                    { return true }
func (p *recordingArtistProvider) CheckConnection(context.Context) error { return nil }
func (p *recordingArtistProvider) Connected() bool                       { return true }
func (p *recordingArtistProvider) CapabilityStatus() map[string]string {
	return map[string]string{"discovery": "connected"}
}
func (p *recordingArtistProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	p.searchCalls++
	a := p.artist
	if a.ProviderName == "" {
		a.ProviderName = p.name
	}
	return []discovery.ArtistSummary{a}, nil
}
func (p *recordingArtistProvider) GetArtistAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	p.albumCalls++
	return p.albums, nil
}
func (p *recordingArtistProvider) SearchAlbums(context.Context, discovery.AlbumQuery, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}
func (p *recordingArtistProvider) GetAlbumTracks(context.Context, string) ([]discovery.TrackInfo, error) {
	return nil, nil
}
func (p *recordingArtistProvider) GetArtistTopTracks(context.Context, string, int) ([]discovery.TrackInfo, error) {
	p.topCalls++
	return p.tracks, nil
}

var _ discovery.Provider = (*recordingArtistProvider)(nil)
var _ discovery.TopTrackProvider = (*recordingArtistProvider)(nil)

func newArtistOverviewServer(_ *testing.T, store library.Store, cat artistCatalogReader, reg *discovery.Registry) *Server {
	return &Server{log: testAPILogger(), store: store, artistCatalog: cat, discoveryReg: reg}
}

func newArtistOverviewRequest(name string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/discover/artists/overview?q="+name, nil)
}

// TestHandleDiscoverArtistOverviewWarmReadsCatalog covers the warm hit: the
// response is built from artist_metadata + artist_discography and the provider
// is never called. The registered provider fails the test if invoked.
func TestHandleDiscoverArtistOverviewWarmReadsCatalog(t *testing.T) {
	store := &stubArtistLookupStore{artists: []domain.Artist{{ID: 7, Name: "Artist", Genres: []string{"rock"}}}}
	cat := &stubArtistCatalog{
		meta: &catalog.ArtistMetadata{
			ArtistID:         7,
			ProviderName:     "deezer",
			ProviderArtistID: "dz-art-7",
			ImageURL:         "https://img/7.jpg",
			Genres:           []string{"rock", "pop"},
		},
		releases: []catalog.ArtistDiscographyRelease{
			{ArtistID: 7, Type: "album", Title: "A1"},
			{ArtistID: 7, Type: "album", Title: "A2"},
			{ArtistID: 7, Type: "single", Title: "S1"},
		},
	}
	reg := newDiscoverRegistry(t, &failingDiscoverProvider{t: t, name: "deezer"})
	s := newArtistOverviewServer(t, store, cat, reg)

	rec := httptest.NewRecorder()
	s.handleDiscoverArtistOverview(rec, newArtistOverviewRequest("Artist"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got artistOverviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if got.Artist.Name != "Artist" || got.Artist.ProviderID != "dz-art-7" || got.Artist.ProviderName != "deezer" {
		t.Errorf("artist = %+v, want catalog identity", got.Artist)
	}
	if got.Discography["album"] != 2 || got.Discography["single"] != 1 {
		t.Errorf("discography = %v, want album:2 single:1", got.Discography)
	}
	if got.MetadataPending {
		t.Errorf("metadata_pending = true on a warm read, want false")
	}
	if len(cat.upserts) != 0 || len(cat.replacedID) != 0 {
		t.Errorf("warm read must not materialize: upserts=%d replaced=%d", len(cat.upserts), len(cat.replacedID))
	}
}

// TestHandleDiscoverArtistOverviewColdMaterializes covers the cold path: with
// no artist_metadata row the handler fetches once from the provider and writes
// the result into the catalog (UpsertArtistMetadata + ReplaceArtistDiscography).
func TestHandleDiscoverArtistOverviewColdMaterializes(t *testing.T) {
	store := &stubArtistLookupStore{artists: []domain.Artist{{ID: 7, Name: "Artist"}}}
	cat := &stubArtistCatalog{} // no metadata → cold
	prov := &recordingArtistProvider{
		name:   "stubdisco",
		artist: discovery.ArtistSummary{ProviderID: "art-1", Name: "Artist"},
		albums: []discovery.AlbumResult{
			{ProviderID: "al-1", Title: "Album One", Year: 2020, Type: "album", TrackCount: 10},
			{ProviderID: "al-2", Title: "Single One", Year: 2021, Type: "single", TrackCount: 2},
		},
		tracks: []discovery.TrackInfo{{ProviderID: "t1", Title: "Hit", ArtistName: "Artist", AlbumTitle: "Album One"}},
	}
	reg := newDiscoverRegistry(t, prov)
	s := newArtistOverviewServer(t, store, cat, reg)

	rec := httptest.NewRecorder()
	s.handleDiscoverArtistOverview(rec, newArtistOverviewRequest("Artist"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if prov.searchCalls != 1 {
		t.Errorf("SearchArtists calls = %d, want 1", prov.searchCalls)
	}
	if len(cat.upserts) != 1 {
		t.Fatalf("UpsertArtistMetadata calls = %d, want 1", len(cat.upserts))
	}
	if got := cat.upserts[0]; got.ArtistID != 7 || got.ProviderArtistID != "art-1" || got.ProviderName != "stubdisco" {
		t.Errorf("materialized metadata = %+v, want artist 7 / art-1 / stubdisco", got)
	}
	if len(cat.replacedID) != 1 || cat.replacedID[0] != 7 {
		t.Errorf("ReplaceArtistDiscography calls = %v, want [7]", cat.replacedID)
	}
	if len(cat.replaced) != 2 {
		t.Errorf("materialized releases = %d, want 2", len(cat.replaced))
	}
	var got artistOverviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(got.TopTracks) != 1 {
		t.Errorf("top_tracks = %d, want 1", len(got.TopTracks))
	}
	if got.Discography["album"] != 1 || got.Discography["single"] != 1 {
		t.Errorf("discography = %v, want album:1 single:1", got.Discography)
	}
	if got.MetadataPending {
		t.Errorf("metadata_pending = true after materialization, want false")
	}
}

// TestHandleDiscoverArtistOverviewCatalogGate proves the catalog.enabled gate
// on the artist cold path: with the live gate false, provider search/albums/top
// are never called and the catalog is never written, and the read returns the
// pending shape (200). With the gate true through the same seam, the provider is
// called — proving the seam is actually consulted, not merely a default.
func TestHandleDiscoverArtistOverviewCatalogGate(t *testing.T) {
	newServer := func(t *testing.T) (*recordingArtistProvider, *stubArtistCatalog, *Server) {
		t.Helper()
		store := &stubArtistLookupStore{artists: []domain.Artist{{ID: 7, Name: "Artist"}}}
		cat := &stubArtistCatalog{} // no metadata → cold
		prov := &recordingArtistProvider{
			name:   "stubdisco",
			artist: discovery.ArtistSummary{ProviderID: "art-1", Name: "Artist"},
			albums: []discovery.AlbumResult{{ProviderID: "al-1", Title: "Album One", Type: "album"}},
			tracks: []discovery.TrackInfo{{ProviderID: "t1", Title: "Hit", ArtistName: "Artist"}},
		}
		reg := newDiscoverRegistry(t, prov)
		return prov, cat, newArtistOverviewServer(t, store, cat, reg)
	}

	t.Run("disabled skips provider and catalog writes", func(t *testing.T) {
		prov, cat, s := newServer(t)
		s.SetCatalogEnabled(func() bool { return false })

		rec := httptest.NewRecorder()
		s.handleDiscoverArtistOverview(rec, newArtistOverviewRequest("Artist"))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if prov.searchCalls != 0 || prov.albumCalls != 0 || prov.topCalls != 0 {
			t.Errorf("provider called with catalog disabled: search=%d albums=%d top=%d",
				prov.searchCalls, prov.albumCalls, prov.topCalls)
		}
		if len(cat.upserts) != 0 || len(cat.replacedID) != 0 {
			t.Errorf("catalog written with catalog disabled: upserts=%d replaced=%d",
				len(cat.upserts), len(cat.replacedID))
		}
		var got artistOverviewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if !got.MetadataPending {
			t.Errorf("disabled cold read must be metadata_pending")
		}
	})

	t.Run("enabled via seam still materializes", func(t *testing.T) {
		prov, cat, s := newServer(t)
		s.SetCatalogEnabled(func() bool { return true })

		rec := httptest.NewRecorder()
		s.handleDiscoverArtistOverview(rec, newArtistOverviewRequest("Artist"))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if prov.searchCalls != 1 {
			t.Errorf("SearchArtists calls = %d, want 1 when enabled", prov.searchCalls)
		}
		if len(cat.upserts) != 1 {
			t.Errorf("UpsertArtistMetadata calls = %d, want 1 when enabled", len(cat.upserts))
		}
	})
}

// TestHandleDiscoverArtistOverviewRequiresQuery preserves the 400 on a missing
// query parameter.
func TestHandleDiscoverArtistOverviewRequiresQuery(t *testing.T) {
	s := newArtistOverviewServer(t, &stubArtistLookupStore{}, &stubArtistCatalog{}, nil)

	rec := httptest.NewRecorder()
	s.handleDiscoverArtistOverview(rec, httptest.NewRequest(http.MethodGet, "/api/discover/artists/overview", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandlersDiscoverNoArtistOverviewCacheSQL guards acceptance criterion 2:
// the old 24h JSON-blob cache read/write is removed from the file, not merely
// bypassed. It reads the source from the package directory.
func TestHandlersDiscoverNoArtistOverviewCacheSQL(t *testing.T) {
	src, err := os.ReadFile("handlers_discover.go")
	if err != nil {
		t.Fatalf("read handlers_discover.go: %v", err)
	}
	if strings.Contains(string(src), "artist_overview_cache") {
		t.Errorf("handlers_discover.go still references artist_overview_cache; the cache read/write must be removed")
	}
}
