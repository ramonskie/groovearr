package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if len(cfg.Sources) != 0 {
		t.Errorf("default Sources should be empty, got %d entries", len(cfg.Sources))
	}

	if cfg.Library.DownloadPath != "/downloads" {
		t.Errorf("default download_path = %q, want /downloads", cfg.Library.DownloadPath)
	}
	if cfg.Library.LibraryPath != "/music" {
		t.Errorf("default library_path = %q, want /music", cfg.Library.LibraryPath)
	}
	if cfg.Library.PlaylistPath != "/playlists" {
		t.Errorf("default playlist_path = %q, want /playlists", cfg.Library.PlaylistPath)
	}
}

func TestPersistenceLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	p, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := p.Get()
	// Docker-default paths are absolute and pass through unchanged.
	if cfg.Library.DownloadPath != "/downloads" {
		t.Errorf("download_path = %q, want /downloads", cfg.Library.DownloadPath)
	}

	soulseekJSON := `{"slskd_url":"http://slskd:5030","api_key":"secret123"}`
	deezerJSON := `{"arl":"arl_token"}`

	err = p.Update(func(cfg *Config) error {
		cfg.Sources["soulseek"] = json.RawMessage(soulseekJSON)
		cfg.Sources["deezer"] = json.RawMessage(deezerJSON)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	p2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := p2.Get()

	checkSource := func(t *testing.T, got json.RawMessage, wantCompact string, label string) {
		t.Helper()
		var gotBuf, wantBuf bytes.Buffer
		if err := json.Compact(&gotBuf, got); err != nil {
			t.Fatalf("%s: invalid saved JSON: %v", label, err)
		}
		if err := json.Compact(&wantBuf, []byte(wantCompact)); err != nil {
			t.Fatalf("%s: invalid expected JSON: %v", label, err)
		}
		if gotBuf.String() != wantBuf.String() {
			t.Errorf("%s source = %s, want %s", label, gotBuf.String(), wantBuf.String())
		}
	}
	checkSource(t, cfg2.Sources["soulseek"], soulseekJSON, "soulseek")
	checkSource(t, cfg2.Sources["deezer"], deezerJSON, "deezer")
}

func TestPersistenceUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	p, _ := LoadOrCreate(path)

	_ = p.Update(func(cfg *Config) error {
		cfg.Sources["soulseek"] = json.RawMessage(`{"slskd_url":"url1"}`)
		return nil
	})
	_ = p.Update(func(cfg *Config) error {
		cfg.Sources["deezer"] = json.RawMessage(`{"quality":"mp3_320"}`)
		return nil
	})

	cfg := p.Get()
	if string(cfg.Sources["soulseek"]) != `{"slskd_url":"url1"}` {
		t.Errorf("soulseek source = %s", cfg.Sources["soulseek"])
	}
	if string(cfg.Sources["deezer"]) != `{"quality":"mp3_320"}` {
		t.Errorf("deezer source = %s", cfg.Sources["deezer"])
	}
}

func TestPersistenceKeepsRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	p, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}

	diskPaths := func() Config {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		var c Config
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatalf("parse config: %v", err)
		}
		return c
	}

	// First-run docker defaults persist unchanged.
	if got := diskPaths().Library.DownloadPath; got != "/downloads" {
		t.Errorf("on-disk download_path after first run = %q, want /downloads", got)
	}

	// An unrelated update must not rewrite the persisted path.
	if err := p.Update(func(cfg *Config) error {
		cfg.Sources["soulseek"] = json.RawMessage(`{"slskd_url":"http://slskd:5030"}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := diskPaths().Library.DownloadPath; got != "/downloads" {
		t.Errorf("on-disk download_path after unrelated update = %q, want /downloads", got)
	}

	// A relative path set through Update stays relative on disk, while the
	// in-memory view is expanded to absolute.
	if err := p.Update(func(cfg *Config) error {
		cfg.Library.LibraryPath = "./music2"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := diskPaths().Library.LibraryPath; got != "./music2" {
		t.Errorf("on-disk library_path after relative update = %q, want ./music2", got)
	}
	wantAbs, _ := filepath.Abs("./music2")
	if got := p.Get().Library.LibraryPath; got != wantAbs {
		t.Errorf("in-memory library_path = %q, want %q (expanded)", got, wantAbs)
	}

	// The settings UI echoes the expanded absolute value back on save; that
	// echo must not rewrite the on-disk relative path.
	if err := p.Update(func(cfg *Config) error {
		cfg.Library.LibraryPath = wantAbs
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := diskPaths().Library.LibraryPath; got != "./music2" {
		t.Errorf("on-disk library_path after UI echo = %q, want ./music2 (kept relative)", got)
	}

	// An echo carrying a trailing slash (semantically the same path) still
	// contracts back to the relative form.
	if err := p.Update(func(cfg *Config) error {
		cfg.Library.LibraryPath = wantAbs + "/"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := diskPaths().Library.LibraryPath; got != "./music2" {
		t.Errorf("on-disk library_path after trailing-slash echo = %q, want ./music2", got)
	}

	// A genuinely new absolute path is preserved as written.
	newAbs := filepath.Join(dir, "elsewhere")
	if err := p.Update(func(cfg *Config) error {
		cfg.Library.LibraryPath = newAbs
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := diskPaths().Library.LibraryPath; got != newAbs {
		t.Errorf("on-disk library_path after absolute update = %q, want %q", got, newAbs)
	}
}

func TestEnvPathDefaultsOnFirstRun(t *testing.T) {
	// Local command-line run: env vars replace the docker defaults.
	t.Setenv("GROOVEARR_LIBRARY_PATH", "/srv/music")
	t.Setenv("GROOVEARR_DOWNLOAD_PATH", "/srv/dl")
	t.Setenv("GROOVEARR_PLAYLIST_PATH", "/srv/playlists")

	path := filepath.Join(t.TempDir(), "config.json")
	p, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := p.Get().Library.LibraryPath; got != "/srv/music" {
		t.Errorf("library_path = %q, want /srv/music (env override)", got)
	}
	// The env values are persisted for the first run.
	data, _ := os.ReadFile(path)
	var disk Config
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Library.LibraryPath != "/srv/music" || disk.Library.DownloadPath != "/srv/dl" {
		t.Errorf("on-disk env defaults not applied: library=%q download=%q", disk.Library.LibraryPath, disk.Library.DownloadPath)
	}

	// Reload: the persisted file wins even though env is still set.
	p2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := p2.Get().Library.LibraryPath; got != "/srv/music" {
		t.Errorf("after reload library_path = %q, want persisted /srv/music", got)
	}
}

func TestPersistenceDefaultsAfterCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	os.WriteFile(path, []byte("{not json}"), 0644)

	p, err := LoadOrCreate(path)
	if err == nil {
		t.Error("expected error for corrupt JSON")
		_ = p
	}
}

func TestValidateDefaults(t *testing.T) {
	cfg := DefaultConfig()
	errs := cfg.Validate()
	if len(errs) > 0 {
		t.Errorf("default config should be valid, got: %v", errs)
	}
}

func TestValidateNoTemplateTokens(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Library.FolderTemplate = "just-a-string"
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "folder_template") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected folder_template warning, got: %v", errs)
	}
}

func TestValidateInvalidSourceJSON(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sources["bad"] = json.RawMessage(`{not valid json}`)
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "sources.bad") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected sources.bad error, got: %v", errs)
	}
}

func TestValidateValidSourceJSON(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sources["soulseek"] = json.RawMessage(`{"slskd_url":"http://localhost:5030"}`)
	errs := cfg.Validate()
	if len(errs) > 0 {
		t.Errorf("valid source JSON should not produce errors, got: %v", errs)
	}
}

// TestValidateAuthMethod pins the accepted auth methods. "basic" was never
// implemented (phantom method) and must be rejected, not silently accepted.
func TestValidateAuthMethod(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		wantAccepted bool
	}{
		{name: "empty defaults to none", method: "", wantAccepted: true},
		{name: "none is accepted", method: "none", wantAccepted: true},
		{name: "forms is accepted", method: "forms", wantAccepted: true},
		{name: "basic is rejected", method: "basic", wantAccepted: false},
		{name: "unknown method is rejected", method: "oauth", wantAccepted: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Auth.Method = tt.method
			// Supply credentials so forms/method checks isolate the method error.
			cfg.Auth.Username = "admin"
			cfg.Auth.Password = "hash"

			errs := cfg.Validate()

			gotMethodErr := false
			for _, e := range errs {
				if strings.Contains(e, "auth.method") {
					gotMethodErr = true
				}
			}
			if gotMethodErr == tt.wantAccepted {
				t.Errorf("auth.method=%q accepted=%v, want accepted=%v (errs: %v)",
					tt.method, !gotMethodErr, tt.wantAccepted, errs)
			}
		})
	}
}

func TestMergeSources(t *testing.T) {
	cfg := DefaultConfig()
	partial := Config{
		Sources: map[string]json.RawMessage{
			"soulseek": json.RawMessage(`{"slskd_url":"http://slskd:5030"}`),
		},
		Library: LibraryConfig{DownloadPath: "/new/path"},
	}
	cfg.Merge(&partial)
	if string(cfg.Sources["soulseek"]) != `{"slskd_url":"http://slskd:5030"}` {
		t.Errorf("sources.soulseek not merged, got: %s", cfg.Sources["soulseek"])
	}
	if cfg.Library.DownloadPath != "/new/path" {
		t.Errorf("library.download_path not merged, got %s", cfg.Library.DownloadPath)
	}
}

func TestMergeAlbumSourcesPresenceBased(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AlbumSources = []string{"prowlarr"}

	// Absent field (nil) leaves the existing value untouched.
	cfg.Merge(&Config{})
	if len(cfg.AlbumSources) != 1 || cfg.AlbumSources[0] != "prowlarr" {
		t.Errorf("absent album_sources should be unchanged, got %v", cfg.AlbumSources)
	}

	// Explicit empty array clears the setting.
	cfg.Merge(&Config{AlbumSources: []string{}})
	if len(cfg.AlbumSources) != 0 {
		t.Errorf("empty album_sources should clear, got %v", cfg.AlbumSources)
	}

	// Non-empty value replaces.
	cfg.Merge(&Config{AlbumSources: []string{"prowlarr"}})
	if len(cfg.AlbumSources) != 1 || cfg.AlbumSources[0] != "prowlarr" {
		t.Errorf("album_sources not replaced, got %v", cfg.AlbumSources)
	}
}

func TestTrackingConfigDefaults(t *testing.T) {
	tests := []struct {
		name              string
		wantRefreshSet    bool
		wantRefreshMins   int
		wantAutoSearching bool
	}{
		{name: "defaults", wantRefreshSet: true, wantRefreshMins: 720, wantAutoSearching: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			if got := cfg.Tracking.RefreshMins != nil; got != tt.wantRefreshSet {
				t.Fatalf("tracking.refresh_mins set = %v, want %v", got, tt.wantRefreshSet)
			}
			if tt.wantRefreshSet && *cfg.Tracking.RefreshMins != tt.wantRefreshMins {
				t.Errorf("tracking.refresh_mins = %d, want %d", *cfg.Tracking.RefreshMins, tt.wantRefreshMins)
			}
			if cfg.Tracking.AutoSearchMissing == nil {
				t.Fatal("tracking.auto_search_missing = nil, want a default pointer")
			}
			if *cfg.Tracking.AutoSearchMissing != tt.wantAutoSearching {
				t.Errorf("tracking.auto_search_missing = %v, want %v", *cfg.Tracking.AutoSearchMissing, tt.wantAutoSearching)
			}
		})
	}
}

func TestMergeTrackingConfig(t *testing.T) {
	tests := []struct {
		name              string
		initialRefresh    *int
		initialAutoSearch *bool
		partial           TrackingConfig
		wantRefresh       *int
		wantAutoSearch    *bool
	}{
		{
			name:           "explicit override",
			initialRefresh: intPtr(720),
			partial:        TrackingConfig{RefreshMins: intPtr(60)},
			wantRefresh:    intPtr(60),
		},
		{
			name:           "nil preserves existing",
			initialRefresh: intPtr(720),
			partial:        TrackingConfig{},
			wantRefresh:    intPtr(720),
		},
		{
			name:           "explicit zero disables",
			initialRefresh: intPtr(720),
			partial:        TrackingConfig{RefreshMins: intPtr(0)},
			wantRefresh:    intPtr(0),
		},
		{
			name:              "auto-search true overrides",
			initialAutoSearch: boolPtr(false),
			partial:           TrackingConfig{AutoSearchMissing: boolPtr(true)},
			wantAutoSearch:    boolPtr(true),
		},
		{
			name:              "auto-search false clears",
			initialAutoSearch: boolPtr(true),
			partial:           TrackingConfig{AutoSearchMissing: boolPtr(false)},
			wantAutoSearch:    boolPtr(false),
		},
		{
			name:              "auto-search nil preserves",
			initialAutoSearch: boolPtr(true),
			partial:           TrackingConfig{},
			wantAutoSearch:    boolPtr(true),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Tracking: TrackingConfig{
				RefreshMins:       tt.initialRefresh,
				AutoSearchMissing: tt.initialAutoSearch,
			}}
			cfg.Merge(&Config{Tracking: tt.partial})

			if (cfg.Tracking.RefreshMins == nil) != (tt.wantRefresh == nil) {
				t.Fatalf("tracking.refresh_mins = %v, want %v", cfg.Tracking.RefreshMins, tt.wantRefresh)
			}
			if cfg.Tracking.RefreshMins != nil && *cfg.Tracking.RefreshMins != *tt.wantRefresh {
				t.Errorf("tracking.refresh_mins = %d, want %d", *cfg.Tracking.RefreshMins, *tt.wantRefresh)
			}
			if (cfg.Tracking.AutoSearchMissing == nil) != (tt.wantAutoSearch == nil) {
				t.Fatalf("tracking.auto_search_missing = %v, want %v", cfg.Tracking.AutoSearchMissing, tt.wantAutoSearch)
			}
			if cfg.Tracking.AutoSearchMissing != nil && *cfg.Tracking.AutoSearchMissing != *tt.wantAutoSearch {
				t.Errorf("tracking.auto_search_missing = %v, want %v", *cfg.Tracking.AutoSearchMissing, *tt.wantAutoSearch)
			}
		})
	}
}

func TestValidateTrackingRefreshMins(t *testing.T) {
	tests := []struct {
		name    string
		mins    *int
		wantErr bool
	}{
		{name: "nil is valid", mins: nil, wantErr: false},
		{name: "zero disables and is valid", mins: intPtr(0), wantErr: false},
		{name: "below minimum is invalid", mins: intPtr(1), wantErr: true},
		{name: "three minutes is invalid", mins: intPtr(3), wantErr: true},
		{name: "four minutes is invalid", mins: intPtr(4), wantErr: true},
		{name: "five minutes is valid", mins: intPtr(5), wantErr: false},
		{name: "positive is valid", mins: intPtr(720), wantErr: false},
		{name: "one year is valid", mins: intPtr(525600), wantErr: false},
		{name: "negative is invalid", mins: intPtr(-1), wantErr: true},
		{name: "above one year is invalid", mins: intPtr(525601), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Tracking.RefreshMins = tt.mins
			errs := cfg.Validate()

			got := false
			for _, e := range errs {
				if strings.Contains(e, "tracking.refresh_mins") {
					got = true
				}
			}
			if got != tt.wantErr {
				t.Errorf("tracking.refresh_mins validation = %v, want %v (errs: %v)", got, tt.wantErr, errs)
			}
		})
	}
}

func TestMaskHidesAPIKey(t *testing.T) {
	const rawKey = "s3cr3t-api-key-0123456789abcdef"

	tests := []struct {
		name       string
		apiKey     string
		wantKey    string
		wantHasKey bool
	}{
		{
			name:       "configured key is masked and flagged",
			apiKey:     rawKey,
			wantKey:    "********",
			wantHasKey: true,
		},
		{
			name:       "absent key stays empty and unflagged",
			apiKey:     "",
			wantKey:    "",
			wantHasKey: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Auth.APIKey = tt.apiKey

			masked := cfg.Mask()

			if masked.Auth.APIKey != tt.wantKey {
				t.Errorf("masked Auth.APIKey = %q, want %q", masked.Auth.APIKey, tt.wantKey)
			}
			if masked.Auth.HasAPIKey != tt.wantHasKey {
				t.Errorf("masked Auth.HasAPIKey = %v, want %v", masked.Auth.HasAPIKey, tt.wantHasKey)
			}

			// The derived flag must be present in serialized output so the UI
			// can tell whether a key exists without receiving it.
			out, err := json.Marshal(masked)
			if err != nil {
				t.Fatalf("marshal masked config: %v", err)
			}
			if !strings.Contains(string(out), `"has_api_key"`) {
				t.Errorf("masked output missing has_api_key: %s", out)
			}
			if tt.apiKey != "" && strings.Contains(string(out), tt.apiKey) {
				t.Errorf("raw API key leaked in masked output: %s", out)
			}

			// Mask must not mutate the original config (value receiver).
			if cfg.Auth.APIKey != tt.apiKey {
				t.Errorf("Mask mutated original API key: got %q, want %q", cfg.Auth.APIKey, tt.apiKey)
			}
		})
	}
}

// TestMaskSensitiveJSONNestedArray is the array/short-secret regression test:
// maskSensitiveJSON must recurse into []any and must mask a short value under a
// sensitive key (the old code only masked strings longer than 4 bytes and never
// descended into arrays).
func TestMaskSensitiveJSONNestedArray(t *testing.T) {
	raw := json.RawMessage(`{"accounts":[{"name":"main","api_key":"abcdef123456"},{"name":"backup","password":"xy"}]}`)

	masked := maskSensitiveJSON(raw)
	var got map[string]any
	if err := json.Unmarshal(masked, &got); err != nil {
		t.Fatalf("unmarshal masked JSON: %v (raw=%s)", err, masked)
	}
	accounts, ok := got["accounts"].([]any)
	if !ok || len(accounts) != 2 {
		t.Fatalf("accounts = %#v, want a 2-element array", got["accounts"])
	}

	first, ok := accounts[0].(map[string]any)
	if !ok {
		t.Fatalf("accounts[0] = %#v, want an object", accounts[0])
	}
	if first["name"] != "main" {
		t.Errorf("non-sensitive name changed: %v", first["name"])
	}
	if s, _ := first["api_key"].(string); strings.Contains(s, "abcdef") || s == "abcdef123456" {
		t.Errorf("nested api_key in array not masked: %q", s)
	}

	second, ok := accounts[1].(map[string]any)
	if !ok {
		t.Fatalf("accounts[1] = %#v, want an object", accounts[1])
	}
	if second["password"] == "xy" {
		t.Errorf("short sensitive value in array not masked: %q", second["password"])
	}
}

// TestMergePreservesNestedMaskedSecret is the round-trip guard for arrays:
// masking a secret nested in an array and echoing that payload back must not
// clobber the real value on merge (merge must descend into arrays too).
func TestMergePreservesNestedMaskedSecret(t *testing.T) {
	orig := json.RawMessage(`{"accounts":[{"name":"main","api_key":"realsecret"},{"name":"backup","password":"realpw"}]}`)

	masked := maskSensitiveJSON(orig)
	var sent map[string]any
	if err := json.Unmarshal(masked, &sent); err != nil {
		t.Fatalf("unmarshal masked: %v", err)
	}
	sentAccounts := sent["accounts"].([]any)
	if sentAccounts[0].(map[string]any)["api_key"] == "realsecret" {
		t.Fatal("precondition: api_key was not masked")
	}

	merged := mergeJSONPreservingSecrets(orig, masked)
	var got map[string]any
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("unmarshal merged: %v (merged=%s)", err, merged)
	}
	accounts := got["accounts"].([]any)
	first := accounts[0].(map[string]any)
	second := accounts[1].(map[string]any)
	if first["api_key"] != "realsecret" {
		t.Errorf("merged api_key = %v, want realsecret (masked value clobbered it)", first["api_key"])
	}
	if second["password"] != "realpw" {
		t.Errorf("merged password = %v, want realpw (masked value clobbered it)", second["password"])
	}
	if first["name"] != "main" {
		t.Errorf("merged name = %v, want main", first["name"])
	}
}

func TestMergePreservesMaskedAPIKey(t *testing.T) {
	const rawKey = "s3cr3t-api-key-0123456789abcdef"

	cfg := DefaultConfig()
	cfg.Auth.APIKey = rawKey

	// Simulate the settings UI echoing the masked GET /api/config payload back.
	masked := cfg.Mask()
	cfg.Merge(&masked)

	if cfg.Auth.APIKey != rawKey {
		t.Errorf("Merge clobbered the API key with a masked value: got %q, want %q", cfg.Auth.APIKey, rawKey)
	}
}

// TestCatalogConfigDefaults pins the catalog defaults that preserve current
// behavior for a legacy install with no "catalog" section: enabled with a 30-day
// soft TTL. The int tuning knobs stay zero so each engine applies its own
// documented default rather than 0.
func TestCatalogConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Catalog.Enabled == nil {
		t.Fatal("catalog.enabled = nil, want a default pointer (true)")
	}
	if !*cfg.Catalog.Enabled {
		t.Errorf("catalog.enabled = false, want true")
	}
	if cfg.Catalog.AlbumRefreshDays != 30 {
		t.Errorf("catalog.album_refresh_days = %d, want 30", cfg.Catalog.AlbumRefreshDays)
	}
	if cfg.Catalog.ArtistRefreshDays != 30 {
		t.Errorf("catalog.artist_refresh_days = %d, want 30", cfg.Catalog.ArtistRefreshDays)
	}
	if cfg.Catalog.BatchSize != 0 {
		t.Errorf("catalog.batch_size = %d, want 0 (engine default)", cfg.Catalog.BatchSize)
	}
	if cfg.Catalog.IntervalMinutes != 0 {
		t.Errorf("catalog.interval_minutes = %d, want 0 (engine default)", cfg.Catalog.IntervalMinutes)
	}
	if cfg.Catalog.Concurrency != 0 {
		t.Errorf("catalog.concurrency = %d, want 0 (reserved/unset)", cfg.Catalog.Concurrency)
	}
}

// TestCatalogConfigLegacyPreservesDefaults simulates an existing config file
// with no "catalog" key (and a partial section) decoded onto DefaultConfig: the
// defaults must survive for every omitted field.
func TestCatalogConfigLegacyPreservesDefaults(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantEnabled  bool
		wantAlbumDay int
		wantArtist   int
	}{
		{name: "no catalog key", raw: `{"library":{"max_download_workers":4}}`, wantEnabled: true, wantAlbumDay: 30, wantArtist: 30},
		{name: "empty catalog", raw: `{"catalog":{}}`, wantEnabled: true, wantAlbumDay: 30, wantArtist: 30},
		{name: "partial catalog keeps other defaults", raw: `{"catalog":{"enabled":false}}`, wantEnabled: false, wantAlbumDay: 30, wantArtist: 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			if err := json.Unmarshal([]byte(tt.raw), &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if cfg.Catalog.Enabled == nil {
				t.Fatal("catalog.enabled = nil after unmarshal, want a pointer")
			}
			if *cfg.Catalog.Enabled != tt.wantEnabled {
				t.Errorf("catalog.enabled = %v, want %v", *cfg.Catalog.Enabled, tt.wantEnabled)
			}
			if cfg.Catalog.AlbumRefreshDays != tt.wantAlbumDay {
				t.Errorf("catalog.album_refresh_days = %d, want %d", cfg.Catalog.AlbumRefreshDays, tt.wantAlbumDay)
			}
			if cfg.Catalog.ArtistRefreshDays != tt.wantArtist {
				t.Errorf("catalog.artist_refresh_days = %d, want %d", cfg.Catalog.ArtistRefreshDays, tt.wantArtist)
			}
		})
	}
}

// TestCatalogConfigJSONOverride verifies an explicit settings payload overrides
// every field, including an explicit false on the Enabled pointer.
func TestCatalogConfigJSONOverride(t *testing.T) {
	raw := `{"catalog":{"enabled":false,"album_refresh_days":7,"artist_refresh_days":14,"batch_size":25,"interval_minutes":5,"concurrency":4}}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Catalog.Enabled == nil || *cfg.Catalog.Enabled {
		t.Errorf("catalog.enabled = %v, want explicit false", cfg.Catalog.Enabled)
	}
	if cfg.Catalog.AlbumRefreshDays != 7 {
		t.Errorf("catalog.album_refresh_days = %d, want 7", cfg.Catalog.AlbumRefreshDays)
	}
	if cfg.Catalog.ArtistRefreshDays != 14 {
		t.Errorf("catalog.artist_refresh_days = %d, want 14", cfg.Catalog.ArtistRefreshDays)
	}
	if cfg.Catalog.BatchSize != 25 {
		t.Errorf("catalog.batch_size = %d, want 25", cfg.Catalog.BatchSize)
	}
	if cfg.Catalog.IntervalMinutes != 5 {
		t.Errorf("catalog.interval_minutes = %d, want 5", cfg.Catalog.IntervalMinutes)
	}
	if cfg.Catalog.Concurrency != 4 {
		t.Errorf("catalog.concurrency = %d, want 4", cfg.Catalog.Concurrency)
	}
}

// TestMergeCatalogConfig pins the partial-update semantics: Enabled is
// pointer-aware (nil preserves, explicit false disables) and the int fields merge
// only when positive so a partial update cannot silently zero a tuned value.
func TestMergeCatalogConfig(t *testing.T) {
	tests := []struct {
		name             string
		initial          CatalogConfig
		partial          CatalogConfig
		wantEnabled      *bool
		wantAlbumDays    int
		wantBatch        int
		wantIntervalMins int
	}{
		{
			name:        "nil enabled preserves existing true",
			initial:     CatalogConfig{Enabled: boolPtr(true), AlbumRefreshDays: 30},
			partial:     CatalogConfig{},
			wantEnabled: boolPtr(true), wantAlbumDays: 30,
		},
		{
			name:        "explicit false disables",
			initial:     CatalogConfig{Enabled: boolPtr(true)},
			partial:     CatalogConfig{Enabled: boolPtr(false)},
			wantEnabled: boolPtr(false), wantAlbumDays: 0,
		},
		{
			name:        "explicit true enables",
			initial:     CatalogConfig{Enabled: boolPtr(false)},
			partial:     CatalogConfig{Enabled: boolPtr(true)},
			wantEnabled: boolPtr(true), wantAlbumDays: 0,
		},
		{
			name:             "positive ints override",
			initial:          CatalogConfig{AlbumRefreshDays: 30, BatchSize: 8, IntervalMinutes: 1},
			partial:          CatalogConfig{AlbumRefreshDays: 7, BatchSize: 20, IntervalMinutes: 15},
			wantAlbumDays:    7,
			wantBatch:        20,
			wantIntervalMins: 15,
		},
		{
			name:             "zero ints do not clobber",
			initial:          CatalogConfig{AlbumRefreshDays: 30, BatchSize: 8, IntervalMinutes: 1},
			partial:          CatalogConfig{},
			wantAlbumDays:    30,
			wantBatch:        8,
			wantIntervalMins: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Catalog: tt.initial}
			cfg.Merge(&Config{Catalog: tt.partial})

			if (cfg.Catalog.Enabled == nil) != (tt.wantEnabled == nil) {
				t.Fatalf("catalog.enabled = %v, want %v", cfg.Catalog.Enabled, tt.wantEnabled)
			}
			if cfg.Catalog.Enabled != nil && *cfg.Catalog.Enabled != *tt.wantEnabled {
				t.Errorf("catalog.enabled = %v, want %v", *cfg.Catalog.Enabled, *tt.wantEnabled)
			}
			if cfg.Catalog.AlbumRefreshDays != tt.wantAlbumDays {
				t.Errorf("catalog.album_refresh_days = %d, want %d", cfg.Catalog.AlbumRefreshDays, tt.wantAlbumDays)
			}
			if cfg.Catalog.BatchSize != tt.wantBatch {
				t.Errorf("catalog.batch_size = %d, want %d", cfg.Catalog.BatchSize, tt.wantBatch)
			}
			if cfg.Catalog.IntervalMinutes != tt.wantIntervalMins {
				t.Errorf("catalog.interval_minutes = %d, want %d", cfg.Catalog.IntervalMinutes, tt.wantIntervalMins)
			}
		})
	}
}

// TestCloneCatalogEnabledIndependent guards that Clone deep-copies the Enabled
// pointer so a trial-merge cannot mutate the live config.
func TestCloneCatalogEnabledIndependent(t *testing.T) {
	cfg := DefaultConfig()
	cl := Clone(cfg)
	if cl.Catalog.Enabled == nil {
		t.Fatal("Clone dropped catalog.enabled")
	}
	*cl.Catalog.Enabled = false
	if !*cfg.Catalog.Enabled {
		t.Error("Clone shares the catalog.enabled pointer with the original")
	}
}

// TestEffectiveProviderOrder pins the fallback precedence: provider_order is
// authoritative whenever it is set. An explicit empty provider_order means
// "registration order" and must NOT fall back to metadata_order; only a
// nil/unset provider_order falls back to the legacy metadata_order. Both
// unset means "registration order" (nil). This is the no-config-breakage
// contract for the D-001 §3 order unification.
func TestEffectiveProviderOrder(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "provider_order wins over metadata_order",
			cfg:  Config{ProviderOrder: []string{"musicbrainz", "deezer"}, MetadataOrder: []string{"deezer", "musicbrainz"}},
			want: []string{"musicbrainz", "deezer"},
		},
		{
			name: "unset provider_order falls back to legacy metadata_order",
			cfg:  Config{ProviderOrder: nil, MetadataOrder: []string{"deezer", "musicbrainz"}},
			want: []string{"deezer", "musicbrainz"},
		},
		{
			name: "explicitly empty provider_order means registration order, no fallback",
			cfg:  Config{ProviderOrder: []string{}, MetadataOrder: []string{"deezer"}},
			want: []string{},
		},
		{
			name: "both empty means registration order",
			cfg:  Config{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.EffectiveProviderOrder()
			if !slices.Equal(got, tt.want) {
				t.Errorf("EffectiveProviderOrder() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDefaultConfigProviderOrderFallback pins that the first-run bootstrap seed
// still reaches every path through the fallback while provider_order stays
// unset, so DefaultConfig's behavior is unchanged by the unification.
func TestDefaultConfigProviderOrderFallback(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ProviderOrder != nil {
		t.Errorf("default provider_order = %v, want nil (legacy metadata_order is the fallback)", cfg.ProviderOrder)
	}
	want := []string{"deezer", "musicbrainz", "discogs"}
	if got := cfg.EffectiveProviderOrder(); !slices.Equal(got, want) {
		t.Errorf("default EffectiveProviderOrder() = %v, want the metadata_order seed %v", got, want)
	}
}

// TestMergeProviderOrder pins presence-based merge semantics (matching
// metadata_order/download_order): absent (nil) preserves, an explicit empty
// array clears, a non-empty value replaces. metadata_order must remain
// independently mergeable so a legacy client that still writes it works.
func TestMergeProviderOrder(t *testing.T) {
	cfg := Config{ProviderOrder: []string{"alpha"}, MetadataOrder: []string{"legacy"}}

	// Absent provider_order leaves the canonical value untouched.
	cfg.Merge(&Config{})
	if !slices.Equal(cfg.ProviderOrder, []string{"alpha"}) {
		t.Errorf("absent provider_order changed it: %v", cfg.ProviderOrder)
	}

	// Non-empty replaces.
	cfg.Merge(&Config{ProviderOrder: []string{"beta", "gamma"}})
	if !slices.Equal(cfg.ProviderOrder, []string{"beta", "gamma"}) {
		t.Errorf("provider_order not replaced: %v", cfg.ProviderOrder)
	}

	// Explicit empty clears.
	cfg.Merge(&Config{ProviderOrder: []string{}})
	if len(cfg.ProviderOrder) != 0 {
		t.Errorf("empty provider_order should clear, got %v", cfg.ProviderOrder)
	}

	// metadata_order still merges independently (legacy clients keep working).
	cfg.Merge(&Config{MetadataOrder: []string{"deezer"}})
	if !slices.Equal(cfg.MetadataOrder, []string{"deezer"}) {
		t.Errorf("metadata_order not merged: %v", cfg.MetadataOrder)
	}
}

// TestCloneProviderOrderIndependent guards that Clone deep-copies provider_order
// so a trial-merge cannot mutate the live config's shared slice.
func TestCloneProviderOrderIndependent(t *testing.T) {
	cfg := Config{ProviderOrder: []string{"alpha"}}
	cl := Clone(cfg)
	if len(cl.ProviderOrder) != 1 {
		t.Fatalf("Clone dropped provider_order: %v", cl.ProviderOrder)
	}
	cl.ProviderOrder[0] = "mutated"
	if cfg.ProviderOrder[0] != "alpha" {
		t.Errorf("Clone shares the provider_order slice with the original: %v", cfg.ProviderOrder)
	}
}
