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

	if cfg.Library.DownloadPath != "./downloads" {
		t.Errorf("default download_path = %q, want ./downloads", cfg.Library.DownloadPath)
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
	if cfg.Library.DownloadPath != "./downloads" {
		t.Errorf("download_path = %q", cfg.Library.DownloadPath)
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

func TestValidateBadPreferredFormat(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Quality.PreferredFormat = "wav"
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "preferred_format") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected preferred_format error, got: %v", errs)
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
