package config

import (
	"os"
	"path/filepath"
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
	err = p.Update(func(cfg *Config) {
		cfg.Soulseek.SlskdURL = "http://slskd:5030"
		cfg.Soulseek.APIKey = "secret123"
		cfg.Deezer.ARL = "arl_token"
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
	p.Update(func(cfg *Config) { cfg.Soulseek.SlskdURL = "url1" })
	p.Update(func(cfg *Config) { cfg.Soulseek.APIKey = "key1" })
	p.Update(func(cfg *Config) { cfg.Deezer.Quality = "mp3_320" })

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
