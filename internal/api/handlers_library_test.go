package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

func testAPILogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubLibraryStore embeds the nil library.Store interface and overrides only
// the methods the handlers under test call. Calling any other Store method
// panics, which is fine for these narrow handler tests.
type stubLibraryStore struct {
	library.Store
	artists []domain.Artist
	tracks  map[int64][]domain.Track
	merges  [][2]int64 // (keep, remove)
	renames []struct {
		id   int64
		name string
	}
}

func (s *stubLibraryStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	if offset >= len(s.artists) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.artists) {
		end = len(s.artists)
	}
	return s.artists[offset:end], nil
}

func (s *stubLibraryStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	for i := range s.artists {
		if s.artists[i].ID == id {
			return &s.artists[i], nil
		}
	}
	return nil, nil
}

func (s *stubLibraryStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	return s.tracks[artistID], nil
}

func (s *stubLibraryStore) MergeArtists(ctx context.Context, keepID, removeID int64) error {
	s.merges = append(s.merges, [2]int64{keepID, removeID})
	return nil
}

func (s *stubLibraryStore) RenameArtist(ctx context.Context, artistID int64, name string) error {
	s.renames = append(s.renames, struct {
		id   int64
		name string
	}{artistID, name})
	return nil
}

// stubNameProvider is a metadata provider whose canonical-name lookups come
// from a fixed map (keyed by normalized name), standing in for MusicBrainz.
type stubNameProvider struct {
	name  string // registry name (default "stubmb")
	names map[string]string
}

var _ metadata.Provider = (*stubNameProvider)(nil)
var _ metadata.ArtistNameProvider = (*stubNameProvider)(nil)

func (p *stubNameProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "stubmb"
}
func (p *stubNameProvider) DisplayName() string {
	if p.name != "" {
		return p.name
	}
	return "stubmb"
}
func (p *stubNameProvider) IsConfigured() bool        { return true }
func (p *stubNameProvider) IsMetadataAvailable() bool { return true }
func (p *stubNameProvider) CapabilityStatus() map[string]string {
	return map[string]string{"metadata": "connected"}
}
func (p *stubNameProvider) CheckConnection(_ context.Context) error { return nil }
func (p *stubNameProvider) Connected() bool                         { return true }
func (p *stubNameProvider) SearchCover(_ context.Context, _, _ string) (*metadata.CoverResult, error) {
	return nil, nil
}
func (p *stubNameProvider) SearchArtistImage(_ context.Context, _ string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}
func (p *stubNameProvider) SearchAlbum(_ context.Context, _, _ string) string { return "" }
func (p *stubNameProvider) EnrichTrack(_ context.Context, _ *domain.Track) (*metadata.TrackMetadata, error) {
	return nil, nil
}
func (p *stubNameProvider) CanonicalArtistName(_ context.Context, name string) (string, error) {
	return p.names[normalizeKey(name)], nil
}

// stubDiscoveryNameProvider is a metadata provider that also implements
// discovery.Provider, standing in for Deezer/Spotify/Tidal. SearchArtists
// returns canned results keyed by query.
type stubDiscoveryNameProvider struct {
	name  string
	names map[string][]discovery.ArtistSummary
}

var _ metadata.Provider = (*stubDiscoveryNameProvider)(nil)
var _ discovery.Provider = (*stubDiscoveryNameProvider)(nil)

func (p *stubDiscoveryNameProvider) Name() string              { return p.name }
func (p *stubDiscoveryNameProvider) DisplayName() string       { return p.name }
func (p *stubDiscoveryNameProvider) IsConfigured() bool        { return true }
func (p *stubDiscoveryNameProvider) IsMetadataAvailable() bool { return true }
func (p *stubDiscoveryNameProvider) CapabilityStatus() map[string]string {
	return map[string]string{"metadata": "connected"}
}
func (p *stubDiscoveryNameProvider) CheckConnection(_ context.Context) error { return nil }
func (p *stubDiscoveryNameProvider) Connected() bool                         { return true }
func (p *stubDiscoveryNameProvider) SearchCover(_ context.Context, _, _ string) (*metadata.CoverResult, error) {
	return nil, nil
}
func (p *stubDiscoveryNameProvider) SearchArtistImage(_ context.Context, _ string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}
func (p *stubDiscoveryNameProvider) SearchAlbum(_ context.Context, _, _ string) string { return "" }
func (p *stubDiscoveryNameProvider) EnrichTrack(_ context.Context, _ *domain.Track) (*metadata.TrackMetadata, error) {
	return nil, nil
}
func (p *stubDiscoveryNameProvider) SearchArtists(_ context.Context, query string, _ int) ([]discovery.ArtistSummary, error) {
	return p.names[normalizeKey(query)], nil
}
func (p *stubDiscoveryNameProvider) GetArtistAlbums(_ context.Context, _ string, _ int) ([]discovery.AlbumResult, error) {
	return nil, nil
}
func (p *stubDiscoveryNameProvider) GetAlbumTracks(_ context.Context, _ string) ([]discovery.TrackInfo, error) {
	return nil, nil
}
func (p *stubDiscoveryNameProvider) SearchAlbums(_ context.Context, _ string, _ int) ([]discovery.AlbumResult, error) {
	return nil, nil
}

// stubDiscoveryFactory declares the discovery capability for a
// stubDiscoveryNameProvider so discoveryReg.Any() includes it.
type stubDiscoveryFactory struct {
	name  string
	names map[string][]discovery.ArtistSummary
}

var _ plugin.PluginFactory = (*stubDiscoveryFactory)(nil)

func (f *stubDiscoveryFactory) Name() string        { return f.name }
func (f *stubDiscoveryFactory) DisplayName() string { return f.name }
func (f *stubDiscoveryFactory) Capabilities() []string {
	return []string{"metadata", "discovery"}
}
func (f *stubDiscoveryFactory) Create(_ json.RawMessage, _ plugin.PluginResources) (plugin.BasePlugin, error) {
	return &stubDiscoveryNameProvider{name: f.name, names: f.names}, nil
}
func (f *stubDiscoveryFactory) ValidateConfig(_ json.RawMessage) error { return nil }
func (f *stubDiscoveryFactory) DefaultConfig() json.RawMessage         { return json.RawMessage("{}") }

func TestArtistThumbURLTransform(t *testing.T) {
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Local Singular", ThumbURL: "artist.jpg"},
			{ID: 2, Name: "Local Plural", ThumbURL: "artists.jpg"},
			{ID: 3, Name: "Remote", ThumbURL: "https://example.com/a.jpg"},
			{ID: 4, Name: "Various Artists", ThumbURL: "artist.jpg"},
		},
	}
	s := &Server{cfg: cfg, store: store, log: logger}

	t.Run("list transforms local thumbs, leaves remote", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/artists", nil)
		rec := httptest.NewRecorder()
		s.handleLibraryArtists(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		var got []domain.Artist
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		want := map[int64]string{
			1: "/api/artist-image/1",
			2: "/api/artist-image/2",
			3: "https://example.com/a.jpg",
			4: "", // compilation groupings show the placeholder avatar
		}
		for _, a := range got {
			if a.ThumbURL != want[a.ID] {
				t.Errorf("artist %d thumb_url = %q, want %q", a.ID, a.ThumbURL, want[a.ID])
			}
		}
	})

	t.Run("detail transforms local plural thumb", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/artists/2", nil)
		req.SetPathValue("artistID", "2")
		rec := httptest.NewRecorder()
		s.handleLibraryArtist(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		var got domain.Artist
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if got.ThumbURL != "/api/artist-image/2" {
			t.Errorf("artist 2 thumb_url = %q, want /api/artist-image/2", got.ThumbURL)
		}
	})

	t.Run("detail blanks compilation grouping portrait", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/library/artists/4", nil)
		req.SetPathValue("artistID", "4")
		rec := httptest.NewRecorder()
		s.handleLibraryArtist(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		var got domain.Artist
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if got.ThumbURL != "" {
			t.Errorf("Various Artists thumb_url = %q, want empty (placeholder)", got.ThumbURL)
		}
	})
}

func TestArtistDuplicatesAndMerge(t *testing.T) {
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en de Munnik"},
			{ID: 2, Name: "Acda en De Munnik"},
			{ID: 3, Name: "Aaliyah"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
		},
	}
	s := &Server{store: store, log: testAPILogger()}

	// Duplicates listing: the two case-variants group together, largest first.
	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 1 {
		t.Fatalf("expected 1 duplicate group, got %d", len(body.Groups))
	}
	g := body.Groups[0]
	if len(g.Artists) != 2 {
		t.Fatalf("expected 2 artists in group, got %d", len(g.Artists))
	}
	if g.Artists[0].ID != 1 || g.Artists[0].Tracks != 2 {
		t.Errorf("canonical artist should be the largest (%+v)", g.Artists[0])
	}

	// Merge request folds artist 2 into artist 1.
	req = httptest.NewRequest(http.MethodPost, "/api/library/artists/1/merge", strings.NewReader(`{"remove_id":2}`))
	req.SetPathValue("artistID", "1")
	rec = httptest.NewRecorder()
	s.handleLibraryArtistMerge(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(store.merges) != 1 || store.merges[0] != [2]int64{1, 2} {
		t.Errorf("merge not forwarded to store: %v", store.merges)
	}
}

func TestArtistDuplicatesPrefersCorrectCase(t *testing.T) {
	// The larger entry is misspelled ("De", "De" capitalized); the smaller
	// one follows the lowercase-particle convention. MusicBrainz returns the
	// canonical spelling, so the correctly-cased entry must be the keeper.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en De Munnik"},
			{ID: 2, Name: "Acda en de Munnik"},
			{ID: 3, Name: "Danny De Munk"},
			{ID: 4, Name: "Danny de Munk"},
			{ID: 5, Name: "No Variants"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 11}, {ID: 12}, {ID: 13}, {ID: 14}, {ID: 15}},
			2: {{ID: 21}},
			3: {{ID: 31}, {ID: 32}, {ID: 33}},
			4: {{ID: 41}},
		},
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(&stubNameProvider{names: map[string]string{
		"acdaendemunnik": "Acda en de Munnik",
		"dannydemunk":    "Danny de Munk",
	}})
	s := &Server{store: store, mdRegistry: reg, artistNames: newArtistNameCache(), log: testAPILogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 2 {
		t.Fatalf("expected 2 duplicate groups, got %d", len(body.Groups))
	}
	// Explicit keeper checks: correct casing wins even with fewer tracks.
	want := map[string]struct {
		id    int64
		canon string
	}{
		"acda en de munnik": {id: 2, canon: "Acda en de Munnik"},
		"danny de munk":     {id: 4, canon: "Danny de Munk"},
	}
	for _, g := range body.Groups {
		if len(g.Artists) != 2 {
			t.Fatalf("expected 2 artists in group %q, got %d", g.Name, len(g.Artists))
		}
		w, ok := want[g.Name]
		if !ok {
			t.Fatalf("unexpected group %q", g.Name)
		}
		if g.Canonical != w.canon {
			t.Errorf("canonical_name for %q = %q, want %q", g.Name, g.Canonical, w.canon)
		}
		if g.Artists[0].ID != w.id {
			t.Errorf("keeper for %q = id %d (%s), want id %d (%s)", g.Name, g.Artists[0].ID, g.Artists[0].Name, w.id, w.canon)
		}
	}
}

func TestArtistMergeRenamesToCanonical(t *testing.T) {
	// Keeper (id 1) is the misspelled, larger variant. MusicBrainz returns
	// the canonical spelling, so the merge must rename the survivor to it.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en De Munnik"},
			{ID: 2, Name: "Acda en de Munnik"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}, {ID: 12}},
			2: {{ID: 13}},
		},
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(&stubNameProvider{names: map[string]string{
		"acdaendemunnik": "Acda en de Munnik",
	}})
	s := &Server{store: store, mdRegistry: reg, artistNames: newArtistNameCache(), log: testAPILogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/library/artists/1/merge", strings.NewReader(`{"remove_id":2}`))
	req.SetPathValue("artistID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryArtistMerge(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(store.merges) != 1 || store.merges[0] != [2]int64{1, 2} {
		t.Fatalf("merge not forwarded to store: %v", store.merges)
	}
	if len(store.renames) != 1 || store.renames[0].id != 1 || store.renames[0].name != "Acda en de Munnik" {
		t.Errorf("survivor not renamed to canonical spelling: %+v", store.renames)
	}
	var resp struct {
		Merged  bool   `json:"merged"`
		Renamed bool   `json:"renamed"`
		Canon   string `json:"canonical_name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if !resp.Merged || !resp.Renamed || resp.Canon != "Acda en de Munnik" {
		t.Errorf("response = %+v, want merged+renamed true with canonical name", resp)
	}
}

func TestArtistNameCacheCoalescesConcurrentLookups(t *testing.T) {
	c := newArtistNameCache()
	ctx := context.Background()
	const key = "acdaendemunnik"

	// Concurrent callers for the same key must share a single fn invocation.
	const callers = 16
	start := make(chan struct{})
	calls := make(chan int, callers)
	var wg sync.WaitGroup
	results := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			got, _ := c.getOrDo(ctx, key, func(_ context.Context) (string, error) {
				calls <- 1
				return "Acda en de Munnik", nil
			})
			results[idx] = got
		}(i)
	}
	close(start)
	wg.Wait()
	close(calls)

	total := 0
	for range calls {
		total++
	}
	if total != 1 {
		t.Errorf("fn invoked %d times, want 1 (single-flight)", total)
	}
	for i, r := range results {
		if r != "Acda en de Munnik" {
			t.Errorf("caller %d got %q, want canonical name", i, r)
		}
	}

	// Subsequent call hits the cache — fn not invoked again.
	got, _ := c.getOrDo(ctx, key, func(_ context.Context) (string, error) {
		t.Error("fn must not run when cached")
		return "", nil
	})
	if got != "Acda en de Munnik" {
		t.Errorf("cached lookup = %q, want canonical name", got)
	}
}

func TestArtistNameCacheCachesErrorsAndMisses(t *testing.T) {
	ctx := context.Background()
	const key = "acdaendemunnik"

	// A transient provider error is cached briefly: an immediate retry must
	// not re-invoke fn, and the error is surfaced again.
	err := errors.New("musicbrainz rate limited")
	c := newArtistNameCache()
	if _, gotErr := c.getOrDo(ctx, key, func(_ context.Context) (string, error) {
		return "", err
	}); gotErr != err {
		t.Fatalf("first lookup error = %v, want %v", gotErr, err)
	}
	if _, gotErr := c.getOrDo(ctx, key, func(_ context.Context) (string, error) {
		t.Error("fn must not run while the error is cached")
		return "", nil
	}); gotErr != err {
		t.Errorf("cached error = %v, want %v", gotErr, err)
	}

	// A genuine not-found is cached as a miss (fn not re-invoked).
	const missKey = "no-such-artist"
	c2 := newArtistNameCache()
	if got, gotErr := c2.getOrDo(ctx, missKey, func(_ context.Context) (string, error) {
		return "", nil
	}); got != "" || gotErr != nil {
		t.Fatalf("first miss = (%q, %v), want empty", got, gotErr)
	}
	if got, gotErr := c2.getOrDo(ctx, missKey, func(_ context.Context) (string, error) {
		t.Error("fn must not run for a cached miss")
		return "x", nil
	}); got != "" || gotErr != nil {
		t.Errorf("cached miss = (%q, %v), want empty", got, gotErr)
	}
}

func TestArtistImageServesFromArtistDir(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error {
		c.Library.LibraryPath = root
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	// Flat layout: artist folder holds tracks directly, with its own portrait.
	write("Flat Artist/01 - Track.mp3", "audio")
	write("Flat Artist/artist.jpg", "flat-image")
	// Nested layout.
	write("Nested Artist/Album/01 - Track.flac", "audio")
	write("Nested Artist/artist.jpg", "nested-image")
	// Compilation grouping: shared folder with a portrait that must not leak.
	write("Various Artists/Album/01 - 2Pac.flac", "audio")
	write("Various Artists/artist.jpg", "va-image")
	// Mismatched folder name (DB artist "AC/DC", folder "AC DC"): still the
	// track's real home and not a compilation — its portrait must serve.
	write("AC DC/Highway to Hell/01 - Highway to Hell.flac", "audio")
	write("AC DC/artist.jpg", "acdc-image")

	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Flat Artist"},
			{ID: 2, Name: "Nested Artist"},
			{ID: 3, Name: "2Pac"},
			{ID: 4, Name: "AC/DC"},
		},
		tracks: map[int64][]domain.Track{
			1: {{FilePath: filepath.Join(root, "Flat Artist", "01 - Track.mp3")}},
			2: {{FilePath: filepath.Join(root, "Nested Artist", "Album", "01 - Track.flac")}},
			3: {{FilePath: filepath.Join(root, "Various Artists", "Album", "01 - 2Pac.flac")}},
			4: {{FilePath: filepath.Join(root, "AC DC", "Highway to Hell", "01 - Highway to Hell.flac")}},
		},
	}
	s := &Server{cfg: cfg, store: store, log: logger}

	tests := []struct {
		name     string
		artistID string
		wantCode int
		wantBody string
	}{
		{"flat artist serves own folder image", "1", http.StatusOK, "flat-image"},
		{"nested artist serves own folder image", "2", http.StatusOK, "nested-image"},
		{"compilation artist must not leak grouping portrait", "3", http.StatusNotFound, ""},
		{"mismatched folder name still serves its own image", "4", http.StatusOK, "acdc-image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/artist-image/"+tt.artistID, nil)
			req.SetPathValue("artistID", tt.artistID)
			rec := httptest.NewRecorder()
			s.handleArtistImage(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" {
				if body := rec.Body.String(); body != tt.wantBody {
					t.Errorf("body = %q, want %q", body, tt.wantBody)
				}
			}
		})
	}
}

// artistOrderTestServer builds a Server with a "musicbrainz" ArtistNameProvider
// and a "deezer" discovery-backed provider, wired to the given metadata order.
func artistOrderTestServer(t *testing.T, order []string) (*Server, *stubLibraryStore) {
	t.Helper()
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Danny De Munk"},
			{ID: 2, Name: "Danny de Munk"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 11}, {ID: 12}, {ID: 13}},
			2: {{ID: 21}},
		},
	}
	pluginReg := plugin.NewRegistry()
	mb := &stubNameProvider{name: "musicbrainz", names: map[string]string{
		"dannydemunk": "Danny de Munk",
	}}
	_ = pluginReg.Register(mb)
	dz := &stubDiscoveryNameProvider{
		name: "deezer",
		names: map[string][]discovery.ArtistSummary{
			"dannydemunk": {{Name: "Danny De Munk"}},
		},
	}
	_ = pluginReg.Register(dz)
	_ = pluginReg.RegisterFactory(&stubDiscoveryFactory{name: "deezer"})

	reg := metadata.NewRegistryFrom(pluginReg)
	discReg := discovery.NewRegistry(pluginReg)
	resolver := metadata.NewMetadataResolver(reg, testAPILogger())
	resolver.SetProviderOrder(metadata.NewProviderOrder(func() []string { return order }))

	s := &Server{
		store:            store,
		mdRegistry:       reg,
		discoveryReg:     discReg,
		metadataResolver: resolver,
		artistNames:      newArtistNameCache(),
		log:              testAPILogger(),
	}
	return s, store
}

func TestArtistDuplicatesUsesDiscoveryProviderForName(t *testing.T) {
	// No MusicBrainz-style provider configured: the discovery-backed "deezer"
	// provider supplies the canonical name via SearchArtists.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Danny De Munk"},
			{ID: 2, Name: "Danny de Munk"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 11}, {ID: 12}, {ID: 13}},
			2: {{ID: 21}},
		},
	}
	pluginReg := plugin.NewRegistry()
	dz := &stubDiscoveryNameProvider{
		name: "deezer",
		names: map[string][]discovery.ArtistSummary{
			"dannydemunk": {{Name: "Danny De Munk"}},
		},
	}
	_ = pluginReg.Register(dz)
	_ = pluginReg.RegisterFactory(&stubDiscoveryFactory{name: "deezer"})

	s := &Server{
		store:        store,
		mdRegistry:   metadata.NewRegistryFrom(pluginReg),
		discoveryReg: discovery.NewRegistry(pluginReg),
		artistNames:  newArtistNameCache(),
		log:          testAPILogger(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 1 || len(body.Groups[0].Artists) != 2 {
		t.Fatalf("expected 1 duplicate group with 2 artists, got %+v", body.Groups)
	}
	g := body.Groups[0]
	if g.Canonical != "Danny De Munk" {
		t.Errorf("canonical_name = %q, want %q (from discovery provider)", g.Canonical, "Danny De Munk")
	}
	if g.Artists[0].ID != 1 {
		t.Errorf("keeper = id %d, want id 1 (largest, since canonical matches it)", g.Artists[0].ID)
	}
}

func TestArtistDuplicatesPrefersMusicBrainz(t *testing.T) {
	// "musicbrainz" returns the correctly-cased name; "deezer" (discovery)
	// returns the misspelled one. MusicBrainz is the authority for casing, so
	// its spelling wins regardless of the configured metadata order.
	for _, order := range [][]string{
		{"deezer", "musicbrainz"},
		{"musicbrainz", "deezer"},
	} {
		s, _ := artistOrderTestServer(t, order)
		req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
		rec := httptest.NewRecorder()
		s.handleLibraryArtistDuplicates(rec, req)
		var body struct {
			Groups []duplicateGroup `json:"groups"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("order %v: bad JSON: %v", order, err)
		}
		if len(body.Groups) != 1 {
			t.Fatalf("order %v: expected 1 group, got %d", order, len(body.Groups))
		}
		if body.Groups[0].Canonical != "Danny de Munk" {
			t.Errorf("order %v: canonical_name = %q, want musicbrainz's 'Danny de Munk'", order, body.Groups[0].Canonical)
		}
		if body.Groups[0].Artists[0].ID != 2 {
			t.Errorf("order %v: keeper = id %d, want id 2 (canonical casing)", order, body.Groups[0].Artists[0].ID)
		}
	}
}

func TestArtistDuplicatesFallsBackToDiscoveryWhenMusicBrainzUnknown(t *testing.T) {
	// MusicBrainz is authoritative but has no entry for this artist; the
	// discovery-backed provider fills in the canonical spelling.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Danny De Munk"},
			{ID: 2, Name: "Danny de Munk"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 11}, {ID: 12}, {ID: 13}},
			2: {{ID: 21}},
		},
	}
	pluginReg := plugin.NewRegistry()
	_ = pluginReg.Register(&stubNameProvider{name: "musicbrainz"}) // returns "" (unknown)
	_ = pluginReg.Register(&stubDiscoveryNameProvider{
		name: "deezer",
		names: map[string][]discovery.ArtistSummary{
			"dannydemunk": {{Name: "Danny De Munk"}},
		},
	})
	_ = pluginReg.RegisterFactory(&stubDiscoveryFactory{name: "deezer"})

	s := &Server{
		store:        store,
		mdRegistry:   metadata.NewRegistryFrom(pluginReg),
		discoveryReg: discovery.NewRegistry(pluginReg),
		artistNames:  newArtistNameCache(),
		log:          testAPILogger(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/library/artists/duplicates", nil)
	rec := httptest.NewRecorder()
	s.handleLibraryArtistDuplicates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Groups []duplicateGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(body.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(body.Groups))
	}
	if body.Groups[0].Canonical != "Danny De Munk" {
		t.Errorf("canonical_name = %q, want deezer's 'Danny De Munk' (fallback)", body.Groups[0].Canonical)
	}
}
