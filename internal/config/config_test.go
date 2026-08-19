package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
