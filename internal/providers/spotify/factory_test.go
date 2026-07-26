package spotify

import (
	"encoding/json"
	"testing"

	"github.com/ramonskie/groovearr/internal/plugin"
)

func TestFactoryValidateConfig(t *testing.T) {
	// Empty config should be valid (plugin starts unconfigured).
	if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config should be valid: %v", err)
	}

	// Valid free mode.
	free := json.RawMessage(`{"mode":"free"}`)
	if err := Factory.ValidateConfig(free); err != nil {
		t.Errorf("valid free mode config rejected: %v", err)
	}

	// Valid dev mode with required fields.
	dev := json.RawMessage(`{"mode":"dev","client_id":"abc123","client_secret":"secret456"}`)
	if err := Factory.ValidateConfig(dev); err != nil {
		t.Errorf("valid dev mode config rejected: %v", err)
	}

	// Invalid mode.
	badMode := json.RawMessage(`{"mode":"premium"}`)
	if err := Factory.ValidateConfig(badMode); err == nil {
		t.Error("invalid mode should return error")
	}

	// Dev mode missing client_id.
	noClientID := json.RawMessage(`{"mode":"dev","client_secret":"secret456"}`)
	if err := Factory.ValidateConfig(noClientID); err == nil {
		t.Error("dev mode without client_id should return error")
	}

	// Dev mode missing client_secret.
	noClientSecret := json.RawMessage(`{"mode":"dev","client_id":"abc123"}`)
	if err := Factory.ValidateConfig(noClientSecret); err == nil {
		t.Error("dev mode without client_secret should return error")
	}

	// Invalid JSON.
	if err := Factory.ValidateConfig(json.RawMessage(`{bad`)); err == nil {
		t.Error("invalid JSON should return error")
	}
}

func TestFactoryDefaultConfig(t *testing.T) {
	def := Factory.DefaultConfig()
	if !json.Valid(def) {
		t.Error("default config is not valid JSON")
	}

	var cfg SpotifyConfig
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("default config unmarshal: %v", err)
	}

	if cfg.Mode != "free" {
		t.Errorf("default mode = %q, want 'free'", cfg.Mode)
	}
	if cfg.RedirectURI == "" {
		t.Error("default redirect_uri should not be empty")
	}
}

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "spotify" {
		t.Errorf("name = %q, want spotify", Factory.Name())
	}
	if Factory.DisplayName() != "Spotify" {
		t.Errorf("display = %q, want Spotify", Factory.DisplayName())
	}
}

func TestFactoryCapabilities(t *testing.T) {
	// Free mode — no capabilities (requires dev mode with valid token).
	freeFactory := NewFactory("free")
	caps := freeFactory.Capabilities()
	if len(caps) != 0 {
		t.Errorf("free mode capabilities = %v, want []", caps)
	}

	// Dev mode factory — metadata + playlist.
	devFactory := NewFactory("dev")
	caps = devFactory.Capabilities()
	hasMetadata := false
	hasPlaylist := false
	for _, c := range caps {
		if c == "metadata" {
			hasMetadata = true
		}
		if c == "playlist" {
			hasPlaylist = true
		}
	}
	if !hasMetadata {
		t.Error("dev mode capabilities missing 'metadata'")
	}
	if !hasPlaylist {
		t.Error("dev mode capabilities missing 'playlist'")
	}
}

func TestFactoryCreate(t *testing.T) {
	// Create should return a valid plugin (Plugin is fully implemented).
	p, err := Factory.Create(json.RawMessage(`{}`), plugin.PluginResources{DownloadPath: "/tmp/test"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if p == nil {
		t.Error("Create returned nil plugin")
	}
}

func TestFactoryCreateSyncsModeToConfig(t *testing.T) {
	// Free mode factory starts with no capabilities.
	f := NewFactory("free")
	if caps := f.Capabilities(); len(caps) != 0 {
		t.Fatalf("free mode capabilities = %v, want []", caps)
	}

	// Creating a plugin with dev config should sync the factory mode.
	_, err := f.Create(json.RawMessage(`{"mode":"dev","client_id":"abc","client_secret":"xyz"}`), plugin.PluginResources{DownloadPath: "/tmp/test"})
	if err != nil {
		t.Fatalf("Create with dev config: %v", err)
	}

	caps := f.Capabilities()
	hasPlaylist := false
	for _, c := range caps {
		if c == "playlist" {
			hasPlaylist = true
		}
	}
	if !hasPlaylist {
		t.Errorf("capabilities after dev Create = %v, want includes 'playlist'", caps)
	}
}
