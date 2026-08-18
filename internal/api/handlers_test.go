package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// ─── parsePagination ─────────────────────────────────────────────────

func TestParsePagination(t *testing.T) {
	tests := []struct {
		name       string
		qs         string
		wantQ      string
		wantOffset int
		wantLimit  int
	}{
		{name: "empty query", qs: "", wantQ: "", wantOffset: 0, wantLimit: 200},
		{name: "only q", qs: "q=beatles", wantQ: "beatles", wantOffset: 0, wantLimit: 200},
		{name: "full params", qs: "q=floyd&offset=20&limit=50", wantQ: "floyd", wantOffset: 20, wantLimit: 50},
		{name: "limit too high clamps", qs: "limit=5000", wantQ: "", wantOffset: 0, wantLimit: 200},
		{name: "limit zero clamps", qs: "limit=0", wantQ: "", wantOffset: 0, wantLimit: 200},
		{name: "limit negative clamps", qs: "limit=-5", wantQ: "", wantOffset: 0, wantLimit: 200},
		{name: "offset zero and limit ten", qs: "offset=0&limit=10", wantQ: "", wantOffset: 0, wantLimit: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?"+tt.qs, nil)
			q, offset, limit := parsePagination(r)
			if q != tt.wantQ {
				t.Errorf("q = %q, want %q", q, tt.wantQ)
			}
			if offset != tt.wantOffset {
				t.Errorf("offset = %d, want %d", offset, tt.wantOffset)
			}
			if limit != tt.wantLimit {
				t.Errorf("limit = %d, want %d", limit, tt.wantLimit)
			}
		})
	}
}

// ─── formatFromPath ──────────────────────────────────────────────────

func TestFormatFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/music/track.flac", "FLAC"},
		{"/music/track.FLAC", "FLAC"},
		{"/music/track.mp3", "MP3"},
		{"/music/track.m4a", "M4A"},
		{"/music/track.alac", "ALAC"},
		{"/music/track.aac", "AAC"},
		{"/music/track.ogg", "OGG"},
		{"/music/track.wav", "WAV"},
		{"/music/track.aif", "AIFF"},
		{"/music/track.aiff", "AIFF"},
		{"/music/track.wma", "WMA"},
		{"/music/track.opus", "OPUS"},
		{"/music/track.xyz", "XYZ"},
		{"/music/track", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := formatFromPath(tt.path)
			if got != tt.want {
				t.Errorf("formatFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ─── stringSlicesEqual ───────────────────────────────────────────────

func TestStringSlicesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []string{}, []string{}, true},
		{"same elements", []string{"a", "b"}, []string{"a", "b"}, true},
		{"different order", []string{"a", "b"}, []string{"b", "a"}, false},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different content", []string{"a", "b"}, []string{"a", "c"}, false},
		{"one nil one empty", nil, []string{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringSlicesEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("stringSlicesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// ─── mergeAvailableProviders ─────────────────────────────────────────

// mockProvider implements metadata.Provider for testing.
type mockProvider struct{ name string }

func (m *mockProvider) Name() string                          { return m.name }
func (m *mockProvider) DisplayName() string                   { return m.name }
func (m *mockProvider) IsConfigured() bool                    { return true }
func (m *mockProvider) IsMetadataAvailable() bool             { return true }
func (m *mockProvider) CheckConnection(context.Context) error { return nil }
func (m *mockProvider) Connected() bool                       { return true }
func (m *mockProvider) CapabilityStatus() map[string]string   { return nil }
func (m *mockProvider) SearchAlbum(ctx context.Context, artist, title string) string {
	return ""
}
func (m *mockProvider) SearchCover(ctx context.Context, artist, album string) (*metadata.CoverResult, error) {
	return nil, nil
}
func (m *mockProvider) SearchArtistImage(ctx context.Context, artist string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}
func (m *mockProvider) EnrichTrack(ctx context.Context, track *domain.Track) (*metadata.TrackMetadata, error) {
	return nil, nil
}

// Compile-time interface check.
var _ metadata.Provider = (*mockProvider)(nil)

func TestMergeAvailableProviders(t *testing.T) {
	providers := []metadata.Provider{
		&mockProvider{name: "deezer"},
		&mockProvider{name: "spotify"},
		&mockProvider{name: "lastfm"},
	}

	t.Run("empty order appends all", func(t *testing.T) {
		got := mergeAvailableProviders(nil, providers)
		want := []string{"deezer", "spotify", "lastfm"}
		if !stringSlicesEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("keeps existing order and appends new", func(t *testing.T) {
		got := mergeAvailableProviders([]string{"spotify", "deezer"}, providers)
		want := []string{"spotify", "deezer", "lastfm"}
		if !stringSlicesEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("drops stale entries", func(t *testing.T) {
		got := mergeAvailableProviders([]string{"soundcloud", "deezer"}, providers)
		want := []string{"deezer", "spotify", "lastfm"}
		if !stringSlicesEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("deduplicates", func(t *testing.T) {
		got := mergeAvailableProviders([]string{"deezer", "deezer", "spotify"}, providers)
		want := []string{"deezer", "spotify", "lastfm"}
		if !stringSlicesEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// ─── handleUpdateConfig ──────────────────────────────────────────────

func TestHandleUpdateConfig_InvalidJSON(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := testPersistence(t)
	srv := &Server{log: log, cfg: cfg}

	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	srv.handleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d. body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleUpdateConfig_EmptyMergeOK(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := testPersistence(t)

	// Empty merge writes no sources changes — reconcileAfterConfigUpdate
	// iterates over empty Sources map and returns safely.
	mdReg := metadata.NewRegistry()
	srv := &Server{
		log: log, cfg: cfg, bgCtx: context.Background(),
		mdRegistry:        mdReg,
		metadataResolver:  nil, // not used when no available providers
		enrichmentHandler: nil, // guarded by nil check
		orchestrator:      nil, // guarded by nil check
		playlistSvc:       nil, // guarded by nil check
		registry:          nil, // not accessed when Sources is empty
	}

	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	srv.handleUpdateConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d. body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func testPersistence(t *testing.T) *config.Persistence {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/config.json"
	p, err := config.LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeBasePlugin is a minimal plugin.PluginFactory-created plugin whose
// capabilities mirror either a capability-grouped source or a source with
// no declared capabilities (e.g. free-mode Spotify).
type fakeBasePlugin struct {
	name         string
	display      string
	cap          string
	access       map[string]string
	unconfigured bool
}

func (p *fakeBasePlugin) Name() string                              { return p.name }
func (p *fakeBasePlugin) DisplayName() string                       { return p.display }
func (p *fakeBasePlugin) IsConfigured() bool                        { return !p.unconfigured }
func (p *fakeBasePlugin) CheckConnection(ctx context.Context) error { return nil }
func (p *fakeBasePlugin) Connected() bool                           { return true }
func (p *fakeBasePlugin) CapabilityStatus() map[string]string {
	if p.cap == "" {
		return nil
	}
	return map[string]string{p.cap: "connected"}
}
func (p *fakeBasePlugin) CapabilityAccess() map[string]string { return p.access }

type fakeFactory struct {
	name         string
	display      string
	cap          string
	access       map[string]string
	unconfigured bool
}

func (f *fakeFactory) Name() string        { return f.name }
func (f *fakeFactory) DisplayName() string { return f.display }
func (f *fakeFactory) Capabilities() []string {
	if f.cap == "" {
		return nil
	}
	return []string{f.cap}
}
func (f *fakeFactory) Create(_ json.RawMessage, _ plugin.PluginResources) (plugin.BasePlugin, error) {
	return &fakeBasePlugin{name: f.name, display: f.display, cap: f.cap, access: f.access, unconfigured: f.unconfigured}, nil
}
func (f *fakeFactory) ValidateConfig(_ json.RawMessage) error { return nil }
func (f *fakeFactory) DefaultConfig() json.RawMessage         { return json.RawMessage(`{}`) }

func TestHandleGetSources_IncludesNoCapabilityPlugins(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	reg := plugin.NewRegistry()
	for _, f := range []plugin.PluginFactory{
		&fakeFactory{name: "downloadsrc", display: "DownloadSrc", cap: "download"},
		&fakeFactory{name: "nocapsrc", display: "NoCapSource", cap: ""},
	} {
		if err := reg.RegisterFactory(f); err != nil {
			t.Fatal(err)
		}
	}
	res := plugin.PluginResources{Logger: log}
	if err := reg.InitAll(map[string]json.RawMessage{
		"downloadsrc": json.RawMessage(`{}`),
		"nocapsrc":    json.RawMessage(`{}`),
	}, res); err != nil {
		t.Fatal(err)
	}

	srv := &Server{log: log, registry: download.NewRegistryFrom(reg)}

	req := httptest.NewRequest(http.MethodGet, "/api/config/sources", nil)
	rec := httptest.NewRecorder()
	srv.handleGetSources(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(got))
	for _, s := range got {
		names[s["name"].(string)] = true
	}
	if !names["downloadsrc"] {
		t.Error("capability-grouped plugin missing from /api/config/sources")
	}
	if !names["nocapsrc"] {
		t.Error("no-capability plugin (e.g. free-mode Spotify) missing from /api/config/sources")
	}
	if len(got) != 2 {
		t.Errorf("got %d sources, want 2 (deduplicated): %v", len(got), names)
	}
}

func TestHandleGetSources_SurfacesCapabilityAccess(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	reg := plugin.NewRegistry()
	// Deezer-like: discovery/metadata served via public API.
	f := &fakeFactory{
		name:    "pubsrc",
		display: "PublicSrc",
		cap:     "metadata",
		access:  map[string]string{"metadata": "public"},
	}
	if err := reg.RegisterFactory(f); err != nil {
		t.Fatal(err)
	}
	res := plugin.PluginResources{Logger: log}
	if err := reg.InitAll(map[string]json.RawMessage{"pubsrc": json.RawMessage(`{}`)}, res); err != nil {
		t.Fatal(err)
	}

	srv := &Server{log: log, registry: download.NewRegistryFrom(reg)}
	req := httptest.NewRequest(http.MethodGet, "/api/config/sources", nil)
	rec := httptest.NewRecorder()
	srv.handleGetSources(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources, want 1", len(got))
	}
	access, ok := got[0]["capability_access"].(map[string]any)
	if !ok {
		t.Fatalf("capability_access missing or wrong type: %v", got[0]["capability_access"])
	}
	if access["metadata"] != "public" {
		t.Errorf("capability_access.metadata = %v, want public", access["metadata"])
	}
}

func TestHandleTestConnection_PublicCapWithoutConfig(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	reg := plugin.NewRegistry()
	// Deezer-like: public metadata capability, never configured. The
	// unconfigured flag forces IsConfigured()==false so the public-cap gate
	// in handleTestConnection is actually exercised.
	f := &fakeFactory{
		name:         "pubsrc",
		display:      "PublicSrc",
		cap:          "metadata",
		access:       map[string]string{"metadata": "public"},
		unconfigured: true,
	}
	if err := reg.RegisterFactory(f); err != nil {
		t.Fatal(err)
	}
	res := plugin.PluginResources{Logger: log}
	if err := reg.InitAll(map[string]json.RawMessage{"pubsrc": json.RawMessage(`{}`)}, res); err != nil {
		t.Fatal(err)
	}
	srv := &Server{log: log, registry: download.NewRegistryFrom(reg), mdRegistry: metadata.NewRegistry()}

	// Public-capability source: test succeeds via CheckConnection (nil here).
	req := httptest.NewRequest(http.MethodPost, "/api/config/sources/pubsrc/test", nil)
	req.SetPathValue("source", "pubsrc")
	rec := httptest.NewRecorder()
	srv.handleTestConnection(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("public-cap source status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// No public capability: still rejected as not configured.
	noreg := plugin.NewRegistry()
	nf := &fakeFactory{name: "privsrc", display: "PrivSrc", cap: "download", unconfigured: true}
	if err := noreg.RegisterFactory(nf); err != nil {
		t.Fatal(err)
	}
	if err := noreg.InitAll(map[string]json.RawMessage{"privsrc": json.RawMessage(`{}`)}, res); err != nil {
		t.Fatal(err)
	}
	srv2 := &Server{log: log, registry: download.NewRegistryFrom(noreg), mdRegistry: metadata.NewRegistry()}
	req2 := httptest.NewRequest(http.MethodPost, "/api/config/sources/privsrc/test", nil)
	req2.SetPathValue("source", "privsrc")
	rec2 := httptest.NewRecorder()
	srv2.handleTestConnection(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("no-public-cap source status = %d, want 400", rec2.Code)
	}
}
