package tidal

import (
	"encoding/json"
	"testing"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// ─── Name / DisplayName / Capabilities ──────────────────────────────────

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "tidal" {
		t.Errorf("Name() = %q, want %q", Factory.Name(), "tidal")
	}
}

func TestFactoryDisplayName(t *testing.T) {
	if Factory.DisplayName() != "Tidal" {
		t.Errorf("DisplayName() = %q, want %q", Factory.DisplayName(), "Tidal")
	}
}

func TestFactoryCapabilities(t *testing.T) {
	caps := Factory.Capabilities()
	want := map[string]bool{
		"download":  true,
		"playlist":  true,
		"discovery": true,
		"metadata":  true,
	}
	for _, c := range caps {
		if !want[c] {
			t.Errorf("unexpected capability %q", c)
		}
		delete(want, c)
	}
	if len(want) > 0 {
		for missing := range want {
			t.Errorf("missing capability %q", missing)
		}
	}
}

// ─── ConfigSchema ───────────────────────────────────────────────────────

func TestFactoryConfigSchema(t *testing.T) {
	sp, ok := Factory.(plugin.ConfigSchemaProvider)
	if !ok {
		t.Fatal("Factory does not implement ConfigSchemaProvider")
	}
	schema := sp.ConfigSchema()
	if len(schema) < 5 {
		t.Fatalf("expected at least 5 fields, got %d", len(schema))
	}
	fields := map[string]plugin.ConfigField{}
	for _, f := range schema {
		fields[f.Name] = f
	}

	// Verify key fields exist with expected types.
	tests := []struct {
		name      string
		wantType  string
		wantLabel string
		secret    bool
	}{
		{"enabled", "toggle", "Enabled", false},
		{"client_id", "password", "Client ID", true},
		{"client_secret", "password", "Client Secret", true},
		{"country_code", "text", "Country Code", false},
		{"quality", "select", "Quality", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := fields[tt.name]
			if !ok {
				t.Fatalf("field %q not found in schema", tt.name)
			}
			if f.Type != tt.wantType {
				t.Errorf("type = %q, want %q", f.Type, tt.wantType)
			}
			if f.Label != tt.wantLabel {
				t.Errorf("label = %q, want %q", f.Label, tt.wantLabel)
			}
			if f.Secret != tt.secret {
				t.Errorf("secret = %v, want %v", f.Secret, tt.secret)
			}
			if tt.name == "enabled" && f.Default != "true" {
				t.Errorf("default = %q, want true", f.Default)
			}
		})
	}
}

// ─── ValidateConfig ─────────────────────────────────────────────────────

func TestFactoryValidateConfig(t *testing.T) {
	t.Run("empty config is valid", func(t *testing.T) {
		if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
			t.Errorf("empty config should be valid: %v", err)
		}
	})

	t.Run("valid config with quality LOSSLESS", func(t *testing.T) {
		valid := json.RawMessage(`{"access_token":"tok","quality":"LOSSLESS"}`)
		if err := Factory.ValidateConfig(valid); err != nil {
			t.Errorf("valid LOSSLESS config rejected: %v", err)
		}
	})

	t.Run("valid config with quality HIGH", func(t *testing.T) {
		valid := json.RawMessage(`{"access_token":"tok","quality":"HIGH"}`)
		if err := Factory.ValidateConfig(valid); err != nil {
			t.Errorf("valid HIGH config rejected: %v", err)
		}
	})

	t.Run("valid config with quality LOW", func(t *testing.T) {
		valid := json.RawMessage(`{"access_token":"tok","quality":"LOW"}`)
		if err := Factory.ValidateConfig(valid); err != nil {
			t.Errorf("valid LOW config rejected: %v", err)
		}
	})

	t.Run("invalid quality rejected", func(t *testing.T) {
		tests := []string{"flac", "mp3", "ultra", "hires_lossless"}
		for _, q := range tests {
			invalid := json.RawMessage(`{"access_token":"tok","quality":"` + q + `"}`)
			if err := Factory.ValidateConfig(invalid); err == nil {
				t.Errorf("quality %q should return error", q)
			}
		}
	})

	t.Run("invalid quality empty string ok when no token", func(t *testing.T) {
		// Quality check only applies when quality is non-empty.
		if err := Factory.ValidateConfig(json.RawMessage(`{"quality":""}`)); err != nil {
			t.Errorf("empty quality should be valid: %v", err)
		}
	})

	t.Run("invalid JSON rejected", func(t *testing.T) {
		if err := Factory.ValidateConfig(json.RawMessage(`{bad`)); err == nil {
			t.Error("invalid JSON should return error")
		}
	})
}

// ─── DefaultConfig ──────────────────────────────────────────────────────

func TestFactoryDefaultConfig(t *testing.T) {
	def := Factory.DefaultConfig()
	if !json.Valid(def) {
		t.Error("default config is not valid JSON")
	}
	var cfg TidalConfig
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("default config unmarshal: %v", err)
	}
	// Defaults from factory.go.
	if cfg.CountryCode != "US" {
		t.Errorf("CountryCode = %q, want US", cfg.CountryCode)
	}
	if cfg.Quality != "LOSSLESS" {
		t.Errorf("Quality = %q, want LOSSLESS", cfg.Quality)
	}
	if cfg.AccessToken != "" {
		t.Errorf("AccessToken = %q, want empty", cfg.AccessToken)
	}
	if cfg.UserID != 0 {
		t.Errorf("UserID = %d, want 0", cfg.UserID)
	}
	if !cfg.Enabled {
		t.Error("Enabled = false, want true (toggle defaults ON)")
	}
}

// ─── Enabler ────────────────────────────────────────────────────────────

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
		{"legacy config without enabled", json.RawMessage(`{"access_token":"tok"}`), true},
		{"explicitly disabled", json.RawMessage(`{"enabled":false,"access_token":"tok"}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := plugin.NewRegistry()
			if err := reg.RegisterFactory(Factory); err != nil {
				t.Fatalf("RegisterFactory: %v", err)
			}
			sources := map[string]json.RawMessage{"tidal": tt.raw}
			if err := reg.InitAll(sources, plugin.PluginResources{DownloadPath: t.TempDir()}); err != nil {
				t.Fatalf("InitAll: %v", err)
			}
			p := reg.Get("tidal")
			if p == nil {
				t.Fatal("tidal plugin not registered")
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

// ─── Create ─────────────────────────────────────────────────────────────

func TestFactoryCreateWithValidConfig(t *testing.T) {
	resources := plugin.PluginResources{
		DownloadPath: t.TempDir(),
		Logger:       nil, // nil logger should not panic (discard handler used)
	}
	raw := json.RawMessage(`{"access_token":"test","country_code":"US","quality":"LOSSLESS"}`)
	client, err := Factory.Create(raw, resources)
	if err != nil {
		t.Fatalf("Create with valid config failed: %v", err)
	}
	if client == nil {
		t.Fatal("Create returned nil client")
	}
	if client.Name() != "tidal" {
		t.Errorf("Name() = %q, want tidal", client.Name())
	}
}

func TestFactoryCreateWithInvalidJSON(t *testing.T) {
	resources := plugin.PluginResources{
		DownloadPath: t.TempDir(),
	}
	_, err := Factory.Create(json.RawMessage(`{bad`), resources)
	if err == nil {
		t.Error("Create with invalid JSON should return error")
	}
}

// ─── OAuthConfig ────────────────────────────────────────────────────────

func TestFactoryOAuthConfig(t *testing.T) {
	sp, ok := Factory.(plugin.ConfigSchemaProvider)
	if !ok {
		t.Fatal("Factory does not implement ConfigSchemaProvider")
	}
	oauth := sp.OAuthConfig()
	if oauth == nil {
		t.Fatal("OAuthConfig returned nil")
	}
	if !oauth.Enabled {
		t.Error("OAuth should be enabled")
	}
	if !oauth.DeviceCode {
		t.Error("DeviceCode should be true")
	}
	if oauth.ConnectLabel == "" {
		t.Error("ConnectLabel should not be empty")
	}
	if oauth.ConnectURL == "" {
		t.Error("ConnectURL should not be empty")
	}
	if oauth.AuthorizeURL == "" {
		t.Error("AuthorizeURL should not be empty")
	}
	if oauth.TokenURL == "" {
		t.Error("TokenURL should not be empty")
	}
	if oauth.DeviceCodeURL == "" {
		t.Error("DeviceCodeURL should not be empty")
	}
	if len(oauth.Scopes) < 2 {
		t.Errorf("expected at least 2 scopes, got %d", len(oauth.Scopes))
	}
}

// ─── UISlots ────────────────────────────────────────────────────────────

func TestFactoryUISlots(t *testing.T) {
	sp, ok := Factory.(plugin.ConfigSchemaProvider)
	if !ok {
		t.Fatal("Factory does not implement ConfigSchemaProvider")
	}
	slots := sp.UISlots()
	if slots == nil {
		t.Fatal("UISlots returned nil")
	}
	if !slots.PlaylistBrowser {
		t.Error("PlaylistBrowser should be true")
	}
	if len(slots.ImportURLPatterns) < 2 {
		t.Errorf("expected at least 2 import URL patterns, got %d", len(slots.ImportURLPatterns))
	}
	hasPlaylistURL := false
	hasUUID := false
	for _, p := range slots.ImportURLPatterns {
		if p.Label == "Tidal playlist URL" {
			hasPlaylistURL = true
		}
		if p.Label == "Tidal playlist UUID" {
			hasUUID = true
		}
	}
	if !hasPlaylistURL {
		t.Error("missing 'Tidal playlist URL' import pattern")
	}
	if !hasUUID {
		t.Error("missing 'Tidal playlist UUID' import pattern")
	}
}

// ─── Icon ───────────────────────────────────────────────────────────────

func TestFactoryIcon(t *testing.T) {
	sp, ok := Factory.(plugin.ConfigSchemaProvider)
	if !ok {
		t.Fatal("Factory does not implement ConfigSchemaProvider")
	}
	if sp.Icon() != "music2" {
		t.Errorf("Icon() = %q, want music2", sp.Icon())
	}
}
