// Package config provides application configuration loading and validation.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Config holds all application settings.
type Config struct {
	Sources       map[string]json.RawMessage `json:"sources"`
	Library       LibraryConfig              `json:"library"`
	Auth          AuthConfig                 `json:"auth"`
	MetadataOrder  []string                   `json:"metadata_order"`  // provider priority (e.g. ["deezer", "musicbrainz"])
	DownloadOrder  []string                   `json:"download_order"`  // download source priority (e.g. ["soulseek", "deezer"])
}

// LibraryConfig holds music library paths.
type LibraryConfig struct {
	DownloadPath      string `json:"download_path"`       // download staging directory
	LibraryPath       string `json:"library_path"`        // where organized downloads end up
	FolderTemplate    string `json:"folder_template"`     // e.g. "{artist}/{album} ({year})/{track:02d} - {title}"
	PlaylistPath      string `json:"playlist_path"`       // separate folder for playlist downloads
	PlaylistTemplate  string `json:"playlist_template"`   // e.g. "{position:02d} {artist} - {title}"
	MaxDownloadWorkers int  `json:"max_download_workers"` // concurrent download workers (default 3)
}

// AuthConfig holds authentication settings.
//
// Method = "none" (default): no authentication required.
// Method = "forms": cookie-based login page with username + password.
// Method = "basic": HTTP Basic Auth (browser popup).
//
// APIKey is always accepted regardless of method (for API/programmatic access).
// LocalBypassSubnets lists CIDR ranges that skip authentication entirely.
type AuthConfig struct {
	Method             string   `json:"method"`                // none, forms, basic
	Username           string   `json:"username"`              // for forms/basic auth
	Password           string   `json:"password"`              // bcrypt hash, masked in API responses
	APIKey             string   `json:"api_key"`               // accepted via X-Api-Key header or ?apikey query
	LocalBypassSubnets []string `json:"local_bypass_subnets"`  // CIDR ranges that skip auth (e.g. 192.168.1.0/24)
}

var folderTokenRE = regexp.MustCompile(`\{[a-z_][a-z0-9_:]*\}`)

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Sources:       make(map[string]json.RawMessage),
		MetadataOrder:  []string{"deezer", "musicbrainz", "discogs"},
		DownloadOrder:  []string{"soulseek", "deezer"},
		Library: LibraryConfig{
			DownloadPath:      "./downloads",
			LibraryPath:       "./music",
			FolderTemplate:    "{artist}/{album} ({year})/{track:02d} - {title}",
			PlaylistPath:      "./playlists",
			MaxDownloadWorkers: 3,
			PlaylistTemplate: "{position:02d} {artist} - {title}",
		},
	}
}

// Validate checks the config for errors and returns human-readable messages.
// Empty list means valid.
func (c Config) Validate() []string {
	var errs []string

	// Sources: validate each entry is structurally valid JSON.
	for name, raw := range c.Sources {
		if !json.Valid(raw) {
			errs = append(errs, fmt.Sprintf("sources.%s: invalid JSON", name))
		}
	}

	// Library.
	if c.Library.FolderTemplate != "" {
		// Warn if template has no known tokens.
		if !folderTokenRE.MatchString(c.Library.FolderTemplate) {
			errs = append(errs, "library.folder_template: contains no recognized tokens (e.g. {artist}, {album})")
		}
	}
	if c.Library.LibraryPath != "" && strings.Contains(c.Library.LibraryPath, "\x00") {
		errs = append(errs, "library.library_path: contains null bytes")
	}

	// Auth.
	validMethods := map[string]bool{"none": true, "forms": true, "basic": true, "": true}
	if !validMethods[c.Auth.Method] {
		errs = append(errs, fmt.Sprintf("auth.method: must be none, forms, or basic (got %q)", c.Auth.Method))
	}
	if c.Auth.Method != "" && c.Auth.Method != "none" {
		if c.Auth.Username == "" {
			errs = append(errs, "auth.username: required when auth.method is "+c.Auth.Method)
		}
		if c.Auth.Password == "" {
			errs = append(errs, "auth.password: required when auth.method is "+c.Auth.Method)
		}
	}
	if c.Auth.APIKey != "" && len(c.Auth.APIKey) < 8 {
		errs = append(errs, "auth.api_key: should be at least 8 characters")
	}

	return errs
}

// Merge copies non-zero fields from partial into c, preserving original
// values for sensitive fields (arl, token, secret, etc.) when partial
// contains masked strings (i.e., came from Config.Mask()).
func (c *Config) Merge(partial *Config) {
	c.mergeFields(partial)

	if c.Sources == nil {
		c.Sources = make(map[string]json.RawMessage)
	}
	mergeSourcesPreservingSecrets(c.Sources, partial.Sources)
}

// mergeFields copies non-zero library fields from partial into c.
func (c *Config) mergeFields(partial *Config) {
	if partial.Library.DownloadPath != "" {
		c.Library.DownloadPath = partial.Library.DownloadPath
	}
	if partial.Library.FolderTemplate != "" {
		c.Library.FolderTemplate = partial.Library.FolderTemplate
	}
	if partial.Library.LibraryPath != "" {
		c.Library.LibraryPath = partial.Library.LibraryPath
	}
	if partial.Library.PlaylistPath != "" {
		c.Library.PlaylistPath = partial.Library.PlaylistPath
	}
	if partial.Library.PlaylistTemplate != "" {
		c.Library.PlaylistTemplate = partial.Library.PlaylistTemplate
	}
	if partial.Library.MaxDownloadWorkers > 0 {
		c.Library.MaxDownloadWorkers = partial.Library.MaxDownloadWorkers
	}

	// Auth — preserve password if partial has a masked (asterisk) value.
	if partial.Auth.Method != "" {
		c.Auth.Method = partial.Auth.Method
	}
	if partial.Auth.Username != "" {
		c.Auth.Username = partial.Auth.Username
	}
	if partial.Auth.Password != "" && !isMaskedString(partial.Auth.Password) {
		hashed, err := HashPassword(partial.Auth.Password)
		if err == nil {
			c.Auth.Password = hashed
		}
	}
	if partial.Auth.APIKey != "" {
		c.Auth.APIKey = partial.Auth.APIKey
	}
	if partial.Auth.LocalBypassSubnets != nil {
		c.Auth.LocalBypassSubnets = partial.Auth.LocalBypassSubnets
	}

	if len(partial.MetadataOrder) > 0 {
		c.MetadataOrder = partial.MetadataOrder
	}
	if len(partial.DownloadOrder) > 0 {
		c.DownloadOrder = partial.DownloadOrder
	}
}

// Load reads config from a JSON file, falling back to defaults for missing fields.
// Validation warnings are logged via the provided logger.
func Load(path string, logger *slog.Logger) (Config, error) {
	cfg, err := readConfigFile(path)
	if err != nil {
		logger.Error("read config failed", "path", path, "error", err, "component", "config")
		return cfg, err
	}

	// Hash plaintext password on first load (bcrypt hashes start with "$2a$").
	if cfg.Auth.Password != "" && !strings.HasPrefix(cfg.Auth.Password, "$2") {
		hashed, hashErr := HashPassword(cfg.Auth.Password)
		if hashErr == nil {
			cfg.Auth.Password = hashed
			// Persist the hashed password back to file.
			if saveErr := saveConfigFile(path, cfg); saveErr != nil {
				logger.Warn("failed to persist hashed password", "error", saveErr, "component", "config")
			}
		} else {
			logger.Error("failed to hash password", "error", hashErr, "component", "config")
		}
	}

	// Expand relative paths.
	expandPaths(&cfg)

	// Log validation warnings at startup.
	if errs := cfg.Validate(); len(errs) > 0 {
		logger.Warn("validation warnings",
			"path", path,
			"component", "config",
			slog.Any("warnings", errs),
		)
	}

	return cfg, nil
}

// readConfigFile reads a JSON config file, merging onto DefaultConfig.
// Returns DefaultConfig if the file does not exist.
func readConfigFile(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// saveConfigFile writes cfg to path as indented JSON.
func saveConfigFile(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// Mask returns a copy of Config with sensitive fields masked.
// Recognized sensitive keys: api_key, token, secret, arl, password, key, api_secret.
func (c Config) Mask() Config {
	masked := c
	// Mask password (bcrypt hash) — never expose it.
	if masked.Auth.Password != "" {
		masked.Auth.Password = "********"
	}
	masked.Sources = make(map[string]json.RawMessage, len(c.Sources))
	for name, raw := range c.Sources {
		masked.Sources[name] = maskSensitiveJSON(raw)
	}
	return masked
}

// maskSensitiveJSON recursively masks values for known sensitive keys.
func maskSensitiveJSON(raw json.RawMessage) json.RawMessage {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return raw
	}
	maskMap(data)
	result, _ := json.Marshal(data)
	return result
}

func maskMap(m map[string]any) {
	for k, v := range m {
		lower := strings.ToLower(k)
		if isSensitiveKey(lower) {
			if s, ok := v.(string); ok && len(s) > 4 {
				m[k] = s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
			}
		}
		if nested, ok := v.(map[string]any); ok {
			maskMap(nested)
		}
	}
}

// isMaskedString detects a string that has been through maskSensitiveJSON:
// first 2 chars visible + repeat("*", len-4) + last 2 chars visible.
func isMaskedString(s string) bool {
	if len(s) < 5 {
		return false
	}
	for i := 2; i < len(s)-2; i++ {
		if s[i] != '*' {
			return false
		}
	}
	return true
}

// mergeSourcesPreservingSecrets merges partial source configs into original,
// skipping any sensitive key whose value in partial is a masked string.
// This prevents the frontend from overwriting secrets with asterisks.
func mergeSourcesPreservingSecrets(original, partial map[string]json.RawMessage) {
	for key, partialRaw := range partial {
		if len(partialRaw) == 0 || string(partialRaw) == "null" || string(partialRaw) == "{}" {
			continue
		}
		origRaw, hasOrig := original[key]
		if !hasOrig {
			original[key] = partialRaw
			continue
		}
		original[key] = mergeJSONPreservingSecrets(origRaw, partialRaw)
	}
}

// mergeJSONPreservingSecrets deep-merges partial into original, preserving
// original values for any key whose partial value is a masked string.
func mergeJSONPreservingSecrets(orig, partial json.RawMessage) json.RawMessage {
	var origMap, partialMap map[string]any
	if json.Unmarshal(partial, &partialMap) != nil {
		return partial
	}
	if json.Unmarshal(orig, &origMap) != nil {
		return partial
	}

	for k, v := range partialMap {
		lower := strings.ToLower(k)
		if isSensitiveKey(lower) {
			if s, ok := v.(string); ok && isMaskedString(s) {
				// Keep original secret — don't overwrite with asterisks.
				if origVal, exists := origMap[k]; exists {
					partialMap[k] = origVal
				}
				continue
			}
		}
		// Recurse into nested objects.
		if nestedPartial, ok := v.(map[string]any); ok {
			if nestedOrig, ok := origMap[k].(map[string]any); ok {
				mergeMapPreservingSecrets(nestedOrig, nestedPartial)
				partialMap[k] = nestedPartial
			}
		}
	}

	result, _ := json.Marshal(partialMap)
	return result
}

// mergeMapPreservingSecrets merges partial into orig in-place, skipping masked secrets.
func mergeMapPreservingSecrets(orig, partial map[string]any) {
	for k, v := range partial {
		lower := strings.ToLower(k)
		if isSensitiveKey(lower) {
			if s, ok := v.(string); ok && isMaskedString(s) {
				if origVal, exists := orig[k]; exists {
					partial[k] = origVal
				}
				continue
			}
		}
		if nestedPartial, ok := v.(map[string]any); ok {
			if nestedOrig, ok := orig[k].(map[string]any); ok {
				mergeMapPreservingSecrets(nestedOrig, nestedPartial)
			}
		}
	}
}

var sensitiveKeys = map[string]bool{
	"api_key": true, "token": true, "secret": true, "arl": true,
	"password": true, "key": true, "api_secret": true,
	"access_token": true, "license_token": true,
}

func isSensitiveKey(k string) bool {
	for sk := range sensitiveKeys {
		if k == sk || strings.Contains(k, "_"+sk) || strings.Contains(k, sk+"_") {
			return true
		}
	}
	return false
}

// expandPaths converts relative library paths to absolute.
func expandPaths(cfg *Config) {
	if cfg.Library.DownloadPath != "" && !filepath.IsAbs(cfg.Library.DownloadPath) {
		if abs, err := filepath.Abs(cfg.Library.DownloadPath); err == nil {
			cfg.Library.DownloadPath = abs
		}
	}
	if cfg.Library.LibraryPath != "" && !filepath.IsAbs(cfg.Library.LibraryPath) {
		if abs, err := filepath.Abs(cfg.Library.LibraryPath); err == nil {
			cfg.Library.LibraryPath = abs
		}
	}
	if cfg.Library.PlaylistPath != "" && !filepath.IsAbs(cfg.Library.PlaylistPath) {
		if abs, err := filepath.Abs(cfg.Library.PlaylistPath); err == nil {
			cfg.Library.PlaylistPath = abs
		}
	}
}
