package tidal

import (
	"encoding/json"
	"fmt"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// TidalConfig holds Tidal-specific plugin configuration.
// Fields matching known sensitive keys (access_token, refresh_token, client_secret)
// are auto-masked in logs and config dumps.
type TidalConfig struct {
	AccessToken  string `json:"access_token"`  // auto-masked (contains "token")
	RefreshToken string `json:"refresh_token"` // auto-masked (contains "token")
	ClientID     string `json:"client_id"`     // auto-masked (contains "id" suffix)
	ClientSecret string `json:"client_secret"` // auto-masked (contains "secret")
	CountryCode  string `json:"country_code"`  // ISO 3166-1 alpha-2
	Quality      string `json:"quality"`       // LOSSLESS, HIGH, LOW
	UserID       int    `json:"user_id"`       // Tidal user ID (internal)
}

const pluginName = "tidal"
const displayName = "Tidal"

// Factory is the plugin factory for Tidal.
var Factory plugin.PluginFactory = &factory{}

type factory struct{}

func (f *factory) Name() string        { return pluginName }
func (f *factory) DisplayName() string { return displayName }

// Capabilities returns the capability domains this plugin provides.
func (f *factory) Capabilities() []string {
	return []string{"download", "playlist", "discovery", "metadata"}
}

// Create builds a Tidal client from raw JSON config and runtime resources.
func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	var cfg TidalConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, err
	}
	client, err := NewClient(cfg, resources.DownloadPath, resources.Logger)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// ValidateConfig checks the raw config for structural validity.
func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg TidalConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return err
	}
	validQualities := map[string]bool{"LOSSLESS": true, "HIGH": true, "LOW": true}
	if cfg.Quality != "" && !validQualities[cfg.Quality] {
		return fmt.Errorf("tidal.quality: must be LOSSLESS, HIGH, or LOW")
	}
	return nil
}

// DefaultConfig returns the default Tidal configuration as JSON.
func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"access_token":"","refresh_token":"","client_id":"","client_secret":"","country_code":"US","quality":"LOSSLESS","user_id":0}`)
}

// ConfigSchema returns the UI form fields for Tidal settings.
func (f *factory) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Name:        "client_id",
			Type:        "password",
			Label:       "Client ID",
			Hint:        "Your Tidal API client ID.",
			Secret:      true,
			Placeholder: "Enter client ID",
		},
		{
			Name:        "client_secret",
			Type:        "password",
			Label:       "Client Secret",
			Hint:        "Your Tidal API client secret.",
			Secret:      true,
			Placeholder: "Enter client secret",
		},
		{
			Name:    "country_code",
			Type:    "text",
			Label:   "Country Code",
			Hint:    "ISO 3166-1 alpha-2 country code (e.g. US, GB, DE).",
			Default: "US",
		},
		{
			Name:    "quality",
			Type:    "select",
			Label:   "Quality",
			Hint:    "Preferred audio quality for Tidal tracks.",
			Default: "LOSSLESS",
			Options: []plugin.FieldOption{
				{Value: "LOSSLESS", Label: "FLAC Lossless"},
				{Value: "HIGH", Label: "AAC 320kbps"},
				{Value: "LOW", Label: "AAC 96kbps"},
			},
		},
	}
}

// Icon returns the frontend icon identifier for this provider.
func (f *factory) Icon() string { return "music2" }

// OAuthConfig returns the Tidal OAuth device code flow configuration.
func (f *factory) OAuthConfig() *plugin.OAuthInfo {
	return &plugin.OAuthInfo{
		Enabled:      true,
		ConnectLabel: "Connect Tidal Account",
		ConnectURL:   "/api/tidal/login",

		DeviceCode:    true,
		AuthorizeURL:  "https://login.tidal.com/authorize",
		TokenURL:      "https://auth.tidal.com/v1/oauth2/token",
		DeviceCodeURL: "https://auth.tidal.com/v1/oauth2/device_authorization",
		Scopes:        []string{"r_usr", "w_usr", "w_sub"},
	}
}

// UISlots declares optional frontend features for the Tidal plugin.
func (f *factory) UISlots() *plugin.UISlots {
	return &plugin.UISlots{
		PlaylistBrowser: true,
		ImportURLPatterns: []plugin.ImportPattern{
			{Pattern: `/playlist/([0-9a-fA-F-]+)(?:[/?#]|$)`, Label: "Tidal playlist URL"},
			{Pattern: `^([0-9a-fA-F-]+)$`, Label: "Tidal playlist UUID", IsFallback: true},
		},
	}
}
