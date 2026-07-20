package deezer

import (
	"encoding/json"
	"testing"
)

func TestFactoryValidateConfig(t *testing.T) {
	// Empty config should be valid (plugin starts unconfigured).
	if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config should be valid: %v", err)
	}

	// Valid config with quality.
	valid := json.RawMessage(`{"arl":"token123","quality":"flac","allow_fallback":true}`)
	if err := Factory.ValidateConfig(valid); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	// Invalid quality.
	invalid := json.RawMessage(`{"arl":"token123","quality":"wav"}`)
	if err := Factory.ValidateConfig(invalid); err == nil {
		t.Error("invalid quality should return error")
	}

	// Invalid JSON.
	if err := Factory.ValidateConfig(json.RawMessage(`{bad`)); err == nil {
		t.Error("invalid JSON should return error")
	}

	// ARL empty, bad quality — should skip quality check (not configured).
	noARL := json.RawMessage(`{"arl":"","quality":"wav"}`)
	if err := Factory.ValidateConfig(noARL); err != nil {
		t.Errorf("empty ARL with bad quality should be valid (quality check skipped): %v", err)
	}
}

func TestFactoryDefaultConfig(t *testing.T) {
	def := Factory.DefaultConfig()
	if !json.Valid(def) {
		t.Error("default config is not valid JSON")
	}
	var cfg DeezerConfig
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("default config unmarshal: %v", err)
	}
}

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "deezer" {
		t.Errorf("name = %q, want deezer", Factory.Name())
	}
	if Factory.DisplayName() != "Deezer" {
		t.Errorf("display = %q, want Deezer", Factory.DisplayName())
	}
	caps := Factory.Capabilities()
	hasDownload := false
	hasPlaylist := false
	for _, c := range caps {
		if c == "download" {
			hasDownload = true
		}
		if c == "playlist" {
			hasPlaylist = true
		}
	}
	if !hasDownload {
		t.Error("capabilities missing 'download'")
	}
	if !hasPlaylist {
		t.Error("capabilities missing 'playlist'")
	}
}
