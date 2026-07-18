package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Soulseek.SlskdURL != "" {
		t.Error("default slskd_url should be empty")
	}
	if cfg.Soulseek.DownloadPath != "./downloads" {
		t.Errorf("default download_path = %q, want ./downloads", cfg.Soulseek.DownloadPath)
	}
	if cfg.Deezer.Quality != "flac" {
		t.Errorf("default deezer quality = %q, want flac", cfg.Deezer.Quality)
	}
	if cfg.Deezer.ARL != "" {
		t.Error("default deezer ARL should be empty")
	}
}

func TestPersistenceLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// First load creates defaults.
	p, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := p.Get()
	if cfg.Soulseek.DownloadPath != "./downloads" {
		t.Errorf("download_path = %q", cfg.Soulseek.DownloadPath)
	}

	// Update and verify persistence.
	err = p.Update(func(cfg *Config) error {
		cfg.Soulseek.SlskdURL = "http://slskd:5030"
		cfg.Soulseek.APIKey = "secret123"
		cfg.Deezer.ARL = "arl_token"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reload from disk.
	p2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := p2.Get()
	if cfg2.Soulseek.SlskdURL != "http://slskd:5030" {
		t.Errorf("slskd_url = %q", cfg2.Soulseek.SlskdURL)
	}
	if cfg2.Soulseek.APIKey != "secret123" {
		t.Errorf("api_key = %q", cfg2.Soulseek.APIKey)
	}
	if cfg2.Deezer.ARL != "arl_token" {
		t.Errorf("arl = %q", cfg2.Deezer.ARL)
	}
}

func TestPersistenceUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	p, _ := LoadOrCreate(path)

	// Multiple updates should accumulate.
	_ = p.Update(func(cfg *Config) error { cfg.Soulseek.SlskdURL = "url1"; return nil })
	_ = p.Update(func(cfg *Config) error { cfg.Soulseek.APIKey = "key1"; return nil })
	_ = p.Update(func(cfg *Config) error { cfg.Deezer.Quality = "mp3_320"; return nil })

	cfg := p.Get()
	if cfg.Soulseek.SlskdURL != "url1" {
		t.Errorf("slskd_url = %q", cfg.Soulseek.SlskdURL)
	}
	if cfg.Soulseek.APIKey != "key1" {
		t.Errorf("api_key = %q", cfg.Soulseek.APIKey)
	}
	if cfg.Deezer.Quality != "mp3_320" {
		t.Errorf("quality = %q", cfg.Deezer.Quality)
	}
}

func TestPersistenceDefaultsAfterCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Write corrupt JSON.
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

func TestValidateBadURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Soulseek.SlskdURL = "not-a-url!!!"
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "slskd_url") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected slskd_url error, got: %v", errs)
	}
}

func TestValidateBadDeezerQuality(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Deezer.ARL = "token123"
	cfg.Deezer.Quality = "wav"
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "deezer.quality") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected deezer.quality error, got: %v", errs)
	}
}

func TestValidateSearchTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Soulseek.SearchTimeout = 0
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "search_timeout") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected search_timeout error, got: %v", errs)
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

func TestValidateEmptyARLNoQualityCheck(t *testing.T) {
	// When ARL is empty, bad quality should not be flagged since Deezer isn't configured.
	cfg := DefaultConfig()
	cfg.Deezer.ARL = ""
	cfg.Deezer.Quality = "wav"
	errs := cfg.Validate()
	for _, e := range errs {
		if strings.Contains(e, "deezer.quality") {
			t.Errorf("quality check should be skipped when ARL is empty, got: %v", errs)
		}
	}
}
