// Package spotify implements a Spotify metadata and playlist plugin.
package spotify

import (
	"encoding/json"
	"fmt"

	"github.com/ramonskie/groovearr/internal/plugin"
)

const pluginName = "spotify"
const displayName = "Spotify"

// SpotifyTokens holds OAuth 2.0 token state for Spotify API access.
type SpotifyTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix timestamp
}

// SpotifyConfig holds Spotify-specific plugin configuration.
type SpotifyConfig struct {
	Mode         string        `json:"mode"`          // "free" or "dev"
	ClientID     string        `json:"client_id"`     // Spotify app client ID
	ClientSecret string        `json:"client_secret"` // Spotify app client secret
	RedirectURI  string        `json:"redirect_uri"`  // OAuth redirect URI
	Tokens       SpotifyTokens `json:"tokens"`        // OAuth token state
}

// Factory is the plugin factory for Spotify.
// Defaults to free mode (metadata only). Capabilities expand to include
// "playlist" when config mode is "dev" — detected at Create time.
var Factory plugin.PluginFactory = NewFactory("free")

type factory struct {
	mode string
}

// NewFactory creates a Spotify plugin factory for the given mode.
// Valid modes: "free" (metadata only), "dev" (metadata + playlist support).
func NewFactory(mode string) plugin.PluginFactory {
	return &factory{mode: mode}
}

func (f *factory) Name() string        { return pluginName }
func (f *factory) DisplayName() string { return displayName }

// Capabilities returns the capability domains this plugin provides.
// Free mode: no capabilities (URL-based metadata only, no API access).
// Dev mode: ["metadata", "discovery", "playlist"] — requires valid OAuth token.
func (f *factory) Capabilities() []string {
	if f.mode == "dev" {
		return []string{"metadata", "discovery", "playlist"}
	}
	return nil
}

// Create builds a Spotify plugin from raw JSON config and runtime resources.
// Also syncs the factory's mode with the config so Capabilities() reflects
// the actual runtime mode (metadata-only for free, metadata+playlist for dev).
func (f *factory) Create(rawCfg json.RawMessage, resources plugin.PluginResources) (plugin.BasePlugin, error) {
	var cfg SpotifyConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return nil, fmt.Errorf("spotify: invalid config: %w", err)
	}
	// Sync factory mode to config mode so Capabilities() is accurate.
	// safe: plugin.Registry serializes Create/InitAll/Rebuild calls via its write lock.
	if cfg.Mode == "dev" || cfg.Mode == "free" {
		f.mode = cfg.Mode
	}
	return NewPlugin(&cfg, resources.DownloadPath, resources.Logger), nil
}

// ValidateConfig checks whether the raw config is structurally valid.
// Rejects invalid modes and missing required fields in dev mode.
func (f *factory) ValidateConfig(rawCfg json.RawMessage) error {
	var cfg SpotifyConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return err
	}

	validModes := map[string]bool{"free": true, "dev": true}
	if cfg.Mode != "" && !validModes[cfg.Mode] {
		return fmt.Errorf("spotify.mode: must be 'free' or 'dev'")
	}

	if cfg.Mode == "dev" {
		if cfg.ClientID == "" {
			return fmt.Errorf("spotify.client_id: required in dev mode")
		}
		if cfg.ClientSecret == "" {
			return fmt.Errorf("spotify.client_secret: required in dev mode")
		}
	}

	return nil
}

// DefaultConfig returns the default Spotify configuration as raw JSON.
func (f *factory) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"mode":"free","client_id":"","client_secret":"","redirect_uri":"http://localhost:8008/api/spotify/callback","tokens":{"access_token":"","refresh_token":"","expires_at":0}}`)
}

func (f *factory) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Name:    "mode",
			Type:    "select",
			Label:   "Mode",
			Hint:    "Free: no credentials needed, parses Spotify URLs for metadata via public endpoints. Dev: full API access including search, playlists, and discovery.",
			Default: "free",
			Options: []plugin.FieldOption{
				{Value: "free", Label: "Free (no credentials)"},
				{Value: "dev", Label: "Developer App"},
			},
		},
		{
			Name:        "client_id",
			Type:        "text",
			Label:       "Client ID",
			Hint:        "Your Spotify Developer App client ID.",
			Placeholder: "Enter client ID",
			DependsOn:   &plugin.FieldDependsOn{Field: "mode", Value: "dev"},
		},
		{
			Name:        "client_secret",
			Type:        "password",
			Label:       "Client Secret",
			Hint:        "Your Spotify Developer App client secret.",
			Secret:      true,
			Placeholder: "Enter client secret",
			DependsOn:   &plugin.FieldDependsOn{Field: "mode", Value: "dev"},
		},
		{
			Name:        "redirect_uri",
			Type:        "text",
			Label:       "Redirect URI",
			Hint:        "Must match the redirect URI configured in your Spotify Developer App.",
			Placeholder: "http://localhost:8008/api/spotify/callback",
			Default:     "http://localhost:8008/api/spotify/callback",
			DependsOn:   &plugin.FieldDependsOn{Field: "mode", Value: "dev"},
		},
	}
}

func (f *factory) Icon() string { return "disc3" }
func (f *factory) OAuthConfig() *plugin.OAuthInfo {
	return &plugin.OAuthInfo{
		Enabled:      true,
		ConnectLabel: "Connect Spotify Account",
		ConnectURL:   "/api/spotify/login",
		DependsOn:    &plugin.FieldDependsOn{Field: "mode", Value: "dev"},
	}
}
func (f *factory) UISlots() *plugin.UISlots {
	return &plugin.UISlots{
		PlaylistBrowser: true,
		ImportURLPatterns: []plugin.ImportPattern{
			{Pattern: `open\.spotify\.com/playlist/([a-zA-Z0-9]+)`, Label: "Spotify playlist URL"},
		},
	}
}
