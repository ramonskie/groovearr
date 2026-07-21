// Package config provides application configuration loading and validation.
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Config holds all application settings.
type Config struct {
	Sources map[string]json.RawMessage `json:"sources"`
	Library LibraryConfig              `json:"library"`
	Quality QualityConfig              `json:"quality"`
}

// LibraryConfig holds music library paths.
type LibraryConfig struct {
	DownloadPath     string `json:"download_path"`     // download staging directory
	LibraryPath      string `json:"library_path"`      // where organized downloads end up
	FolderTemplate   string `json:"folder_template"`   // e.g. "{artist}/{album} ({year})/{track:02d} - {title}"
	PlaylistPath     string `json:"playlist_path"`     // separate folder for playlist downloads
	PlaylistTemplate string `json:"playlist_template"` // e.g. "{position:02d} {artist} - {title}"
}

// QualityConfig holds quality preferences for downloads.
type QualityConfig struct {
	PreferredFormat string `json:"preferred_format"` // flac, mp3, any
	MinBitrate      int    `json:"min_bitrate"`      // kbps, 0 = no minimum
}

var validFormats = map[string]bool{"flac": true, "mp3": true, "any": true}
var folderTokenRE = regexp.MustCompile(`\{[a-z_][a-z0-9_:]*\}`)

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Sources: make(map[string]json.RawMessage),
		Library: LibraryConfig{
			DownloadPath:     "./downloads",
			LibraryPath:      "./music",
			FolderTemplate:   "{artist}/{album} ({year})/{track:02d} - {title}",
			PlaylistPath:     "./playlists",
			PlaylistTemplate: "{position:02d} {artist} - {title}",
		},
		Quality: QualityConfig{
			PreferredFormat: "flac",
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

	// Quality.
	if c.Quality.PreferredFormat != "" && !validFormats[c.Quality.PreferredFormat] {
		errs = append(errs, fmt.Sprintf("quality.preferred_format: must be flac, mp3, or any (got %q)", c.Quality.PreferredFormat))
	}
	if c.Quality.MinBitrate < 0 {
		errs = append(errs, "quality.min_bitrate: must be >= 0")
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

// mergeFields copies non-zero library and quality fields from partial into c.
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

	if partial.Quality.PreferredFormat != "" {
		c.Quality.PreferredFormat = partial.Quality.PreferredFormat
	}
	if partial.Quality.MinBitrate > 0 {
		c.Quality.MinBitrate = partial.Quality.MinBitrate
	}
}

// Load reads config from a JSON file, falling back to defaults for missing fields.
func Load(path string) (Config, error) {
	cfg, err := readConfigFile(path)
	if err != nil {
		return cfg, err
	}

	// Expand relative paths.
	expandPaths(&cfg)

	// Log validation warnings at startup.
	if errs := cfg.Validate(); len(errs) > 0 {
		log.Printf("config: validation warnings in %s:", path)
		for _, e := range errs {
			log.Printf("  - %s", e)
		}
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

// Mask returns a copy of Config with sensitive fields masked.
// Recognized sensitive keys: api_key, token, secret, arl, password, key, api_secret.
func (c Config) Mask() Config {
	masked := c
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
