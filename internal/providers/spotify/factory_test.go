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
	if !cfg.Enabled {
		t.Error("Enabled = false, want true (toggle defaults ON)")
	}
}

// ─── Enabler / ConfigSchema ─────────────────────────────────────────────

func TestFactoryConfigSchemaEnabledToggle(t *testing.T) {
	sp, ok := Factory.(plugin.ConfigSchemaProvider)
	if !ok {
		t.Fatal("Factory does not implement ConfigSchemaProvider")
	}
	for _, f := range sp.ConfigSchema() {
		if f.Name != "enabled" {
			continue
		}
		if f.Type != "toggle" {
			t.Errorf("enabled type = %q, want toggle", f.Type)
		}
		if f.Label != "Enabled" {
			t.Errorf("enabled label = %q, want Enabled", f.Label)
		}
		if f.Default != "true" {
			t.Errorf("enabled default = %q, want true", f.Default)
		}
		return
	}
	t.Fatal("enabled field not found in schema")
}

func TestFactoryEnabledToggle(t *testing.T) {
	resources := plugin.PluginResources{DownloadPath: t.TempDir()}
	tests := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{"default config", Factory.DefaultConfig(), true},
		{"explicitly enabled", json.RawMessage(`{"enabled":true}`), true},
		{"explicitly disabled", json.RawMessage(`{"enabled":false}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Factory.Create(tt.raw, resources)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			enabler, ok := p.(plugin.Enabler)
			if !ok {
				t.Fatal("plugin does not implement plugin.Enabler")
			}
			if got := enabler.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRegistryMergeDefaultsEnabled pins the production path: InitAll merges
// factory defaults over the stored source config, so a legacy config with no
// "enabled" key stays enabled and an explicit false is preserved.
func TestRegistryMergeDefaultsEnabled(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{"legacy config without enabled", json.RawMessage(`{"mode":"free"}`), true},
		{"explicitly disabled", json.RawMessage(`{"enabled":false,"mode":"free"}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := plugin.NewRegistry()
			if err := reg.RegisterFactory(Factory); err != nil {
				t.Fatalf("RegisterFactory: %v", err)
			}
			sources := map[string]json.RawMessage{"spotify": tt.raw}
			if err := reg.InitAll(sources, plugin.PluginResources{DownloadPath: t.TempDir()}); err != nil {
				t.Fatalf("InitAll: %v", err)
			}
			p := reg.Get("spotify")
			if p == nil {
				t.Fatal("spotify plugin not registered")
			}
			enabler, ok := p.(plugin.Enabler)
			if !ok {
				t.Fatal("plugin does not implement plugin.Enabler")
			}
			if got := enabler.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled = %v, want %v", got, tt.want)
			}
			configured := reg.Configured()
			if !tt.want && len(configured) != 0 {
				t.Errorf("Configured() = %v, want empty for disabled provider", configured)
			}
		})
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
