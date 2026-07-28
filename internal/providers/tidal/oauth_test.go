package tidal

import (
	"encoding/json"
	"testing"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// ─── OAuthConfig Structure ──────────────────────────────────────────────

func TestOAuthConfigStructure(t *testing.T) {
	sp, ok := Factory.(plugin.ConfigSchemaProvider)
	if !ok {
		t.Fatal("Factory does not implement ConfigSchemaProvider")
	}
	oauth := sp.OAuthConfig()
	if oauth == nil {
		t.Fatal("OAuthConfig returned nil")
	}

	t.Run("enabled and device code", func(t *testing.T) {
		if !oauth.Enabled {
			t.Error("OAuth should be enabled")
		}
		if !oauth.DeviceCode {
			t.Error("DeviceCode should be true")
		}
	})

	t.Run("labels and URLs", func(t *testing.T) {
		if oauth.ConnectLabel != "Connect Tidal Account" {
			t.Errorf("ConnectLabel = %q, want Connect Tidal Account", oauth.ConnectLabel)
		}
		if oauth.ConnectURL != "/api/tidal/login" {
			t.Errorf("ConnectURL = %q, want /api/tidal/login", oauth.ConnectURL)
		}
	})

	t.Run("OAuth endpoints", func(t *testing.T) {
		if oauth.AuthorizeURL != "https://login.tidal.com/authorize" {
			t.Errorf("AuthorizeURL = %q", oauth.AuthorizeURL)
		}
		if oauth.TokenURL != "https://auth.tidal.com/v1/oauth2/token" {
			t.Errorf("TokenURL = %q", oauth.TokenURL)
		}
		if oauth.DeviceCodeURL != "https://auth.tidal.com/v1/oauth2/device_authorization" {
			t.Errorf("DeviceCodeURL = %q", oauth.DeviceCodeURL)
		}
	})

	t.Run("scopes", func(t *testing.T) {
		wantScopes := map[string]bool{
			"r_usr": true,
			"w_usr": true,
			"w_sub": true,
		}
		if len(oauth.Scopes) != len(wantScopes) {
			t.Errorf("Scopes count = %d, want %d", len(oauth.Scopes), len(wantScopes))
		}
		for _, s := range oauth.Scopes {
			if !wantScopes[s] {
				t.Errorf("unexpected scope %q", s)
			}
			delete(wantScopes, s)
		}
		for missing := range wantScopes {
			t.Errorf("missing scope %q", missing)
		}
	})
}

// ─── UISlots Structure ──────────────────────────────────────────────────

func TestUISlotsStructure(t *testing.T) {
	sp, ok := Factory.(plugin.ConfigSchemaProvider)
	if !ok {
		t.Fatal("Factory does not implement ConfigSchemaProvider")
	}
	slots := sp.UISlots()
	if slots == nil {
		t.Fatal("UISlots returned nil")
	}

	t.Run("playlist browser", func(t *testing.T) {
		if !slots.PlaylistBrowser {
			t.Error("PlaylistBrowser should be true")
		}
	})

	t.Run("import URL patterns", func(t *testing.T) {
		if len(slots.ImportURLPatterns) < 2 {
			t.Fatalf("expected at least 2 import patterns, got %d", len(slots.ImportURLPatterns))
		}

		foundPlaylistURL := false
		foundUUID := false
		for _, p := range slots.ImportURLPatterns {
			if p.Pattern == "" {
				t.Errorf("pattern %q has empty regex", p.Label)
			}
			if p.Label == "Tidal playlist URL" {
				foundPlaylistURL = true
				if p.IsFallback {
					t.Error("playlist URL pattern should not be fallback")
				}
			}
			if p.Label == "Tidal playlist UUID" {
				foundUUID = true
				if !p.IsFallback {
					t.Error("UUID pattern should be fallback")
				}
			}
		}
		if !foundPlaylistURL {
			t.Error("missing 'Tidal playlist URL' pattern")
		}
		if !foundUUID {
			t.Error("missing 'Tidal playlist UUID' pattern")
		}
	})
}

// ─── DefaultConfig Values ───────────────────────────────────────────────

func TestDefaultConfigStructure(t *testing.T) {
	def := Factory.DefaultConfig()
	if !json.Valid(def) {
		t.Fatal("default config is not valid JSON")
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("unmarshal defaults: %v", err)
	}

	t.Run("has all expected keys", func(t *testing.T) {
		expectedKeys := []string{
			"access_token", "refresh_token", "client_id", "client_secret",
			"country_code", "quality", "user_id",
		}
		for _, k := range expectedKeys {
			if _, ok := cfg[k]; !ok {
				t.Errorf("missing key %q in default config", k)
			}
		}
	})

	t.Run("default values", func(t *testing.T) {
		if cfg["country_code"] != "US" {
			t.Errorf("country_code = %v, want US", cfg["country_code"])
		}
		if cfg["quality"] != "LOSSLESS" {
			t.Errorf("quality = %v, want LOSSLESS", cfg["quality"])
		}
		if cfg["user_id"] != float64(0) {
			t.Errorf("user_id = %v, want 0", cfg["user_id"])
		}
	})
}

// ─── ValidateConfig Edge Cases ──────────────────────────────────────────

func TestValidateConfigEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{
			name:    "empty config",
			config:  `{}`,
			wantErr: false,
		},
		{
			name:    "quality LOSSLESS",
			config:  `{"quality":"LOSSLESS","access_token":"x"}`,
			wantErr: false,
		},
		{
			name:    "quality HIGH",
			config:  `{"quality":"HIGH","access_token":"x"}`,
			wantErr: false,
		},
		{
			name:    "quality LOW",
			config:  `{"quality":"LOW","access_token":"x"}`,
			wantErr: false,
		},
		{
			name:    "empty quality is ok (defaults applied later)",
			config:  `{"quality":""}`,
			wantErr: false,
		},
		{
			name:    "invalid quality flac",
			config:  `{"quality":"flac","access_token":"x"}`,
			wantErr: true,
		},
		{
			name:    "invalid quality mp3_320",
			config:  `{"quality":"mp3_320","access_token":"x"}`,
			wantErr: true,
		},
		{
			name:    "invalid quality hires",
			config:  `{"quality":"hires","access_token":"x"}`,
			wantErr: true,
		},
		{
			name:    "malformed JSON",
			config:  `{"quality":"LOW"`,
			wantErr: true,
		},
		{
			name:    "all fields populated",
			config:  `{"access_token":"at","refresh_token":"rt","client_id":"cid","client_secret":"cs","country_code":"GB","quality":"HIGH","user_id":42}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Factory.ValidateConfig(json.RawMessage(tt.config))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig(%q) error = %v, wantErr = %v", tt.config, err, tt.wantErr)
			}
		})
	}
}

// ─── Config Unmarshal ───────────────────────────────────────────────────

func TestTidalConfigUnmarshal(t *testing.T) {
	t.Run("full config", func(t *testing.T) {
		raw := `{
			"access_token": "at123",
			"refresh_token": "rt456",
			"client_id": "client-abc",
			"client_secret": "secret-xyz",
			"country_code": "DE",
			"quality": "HIGH",
			"user_id": 99
		}`
		var cfg TidalConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if cfg.AccessToken != "at123" {
			t.Errorf("AccessToken = %q", cfg.AccessToken)
		}
		if cfg.RefreshToken != "rt456" {
			t.Errorf("RefreshToken = %q", cfg.RefreshToken)
		}
		if cfg.ClientID != "client-abc" {
			t.Errorf("ClientID = %q", cfg.ClientID)
		}
		if cfg.ClientSecret != "secret-xyz" {
			t.Errorf("ClientSecret = %q", cfg.ClientSecret)
		}
		if cfg.CountryCode != "DE" {
			t.Errorf("CountryCode = %q", cfg.CountryCode)
		}
		if cfg.Quality != "HIGH" {
			t.Errorf("Quality = %q", cfg.Quality)
		}
		if cfg.UserID != 99 {
			t.Errorf("UserID = %d", cfg.UserID)
		}
	})

	t.Run("partial config with defaults", func(t *testing.T) {
		raw := `{"quality":"LOW"}`
		var cfg TidalConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if cfg.AccessToken != "" {
			t.Errorf("AccessToken should be empty, got %q", cfg.AccessToken)
		}
		if cfg.CountryCode != "" {
			t.Errorf("CountryCode should be empty, got %q", cfg.CountryCode)
		}
		if cfg.Quality != "LOW" {
			t.Errorf("Quality = %q, want LOW", cfg.Quality)
		}
	})
}

// ─── API Error Type ─────────────────────────────────────────────────────

func TestTidalError(t *testing.T) {
	t.Run("error with details", func(t *testing.T) {
		raw := `{"errors":[{"detail":"invalid token"}],"userMessage":"Please login again"}`
		var apiErr tidalError
		if err := json.Unmarshal([]byte(raw), &apiErr); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if apiErr.Error() != "invalid token" {
			t.Errorf("Error() = %q, want invalid token", apiErr.Error())
		}
	})

	t.Run("error with user message only", func(t *testing.T) {
		raw := `{"errors":[],"userMessage":"Rate limited"}`
		var apiErr tidalError
		if err := json.Unmarshal([]byte(raw), &apiErr); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if apiErr.Error() != "Rate limited" {
			t.Errorf("Error() = %q, want Rate limited", apiErr.Error())
		}
	})

	t.Run("error with no details", func(t *testing.T) {
		raw := `{"errors":[],"userMessage":""}`
		var apiErr tidalError
		if err := json.Unmarshal([]byte(raw), &apiErr); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if apiErr.Error() != "unknown tidal API error" {
			t.Errorf("Error() = %q, want unknown tidal API error", apiErr.Error())
		}
	})
}

// ─── parseRetryAfter ────────────────────────────────────────────────────

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		wantMinSec int // minimum seconds expected
		wantMaxSec int // maximum seconds expected (for http-date format)
	}{
		{name: "numeric seconds", header: "30", wantMinSec: 29, wantMaxSec: 31},
		{name: "zero seconds", header: "0", wantMinSec: 0, wantMaxSec: 1},
		{name: "empty header defaults to 5s", header: "", wantMinSec: 5, wantMaxSec: 5},
		{name: "invalid string defaults to 5s", header: "not-a-number", wantMinSec: 5, wantMaxSec: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := parseRetryAfter(tt.header)
			sec := int(d.Seconds())
			if sec < tt.wantMinSec || sec > tt.wantMaxSec {
				t.Errorf("parseRetryAfter(%q) = %v, want between %d-%d seconds",
					tt.header, d, tt.wantMinSec, tt.wantMaxSec)
			}
		})
	}
}

// ─── API Constants ──────────────────────────────────────────────────────

func TestAPIConstants(t *testing.T) {
	if v1BaseURL != "https://api.tidal.com/v1" {
		t.Errorf("v1BaseURL = %q", v1BaseURL)
	}
	if v2BaseURL != "https://api.tidal.com/v2" {
		t.Errorf("v2BaseURL = %q", v2BaseURL)
	}
}
