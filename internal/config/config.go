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

	"github.com/ramonskie/groovearr/internal/logger"
)

// Config holds all application settings.
type Config struct {
	Sources        map[string]json.RawMessage `json:"sources"`
	Library        LibraryConfig              `json:"library"`
	Auth           AuthConfig                 `json:"auth"`
	Logging        *LoggingConfig             `json:"logging"`         // log level, format, rotation policy
	MetadataOrder  []string                   `json:"metadata_order"`  // provider priority (e.g. ["deezer", "musicbrainz"])
	DownloadOrder  []string                   `json:"download_order"`  // download source priority (e.g. ["soulseek", "deezer"])
	AlbumSources   []string                   `json:"album_sources"`   // album-capable source order (e.g. ["prowlarr"])
	DownloadClient string                     `json:"download_client"` // default download client (e.g. "qbittorrent")
	SetupCompleted bool                       `json:"setup_completed"` // first-run wizard dismissed
}

// LibraryConfig holds music library paths.
type LibraryConfig struct {
	DownloadPath         string `json:"download_path"`           // download staging directory
	LibraryPath          string `json:"library_path"`            // where organized downloads end up
	FolderTemplate       string `json:"folder_template"`         // e.g. "{artist}/{album} ({year})/{track:02d} - {title}"
	CompilationTemplate  string `json:"compilation_template"`    // template for VA compilations (defaults to "Various Artists/...")
	PlaylistPath         string `json:"playlist_path"`           // separate folder for playlist downloads
	PlaylistTemplate     string `json:"playlist_template"`       // e.g. "{position:02d} {artist} - {title}"
	MaxDownloadWorkers   int    `json:"max_download_workers"`    // concurrent download workers (default 3)
	PlaylistAutoSyncMins *int   `json:"playlist_auto_sync_mins"` // interval for auto-sync (nil/0 = disabled, default 30)
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
	Method             string   `json:"method"`               // none, forms, basic
	Username           string   `json:"username"`             // for forms/basic auth
	Password           string   `json:"password"`             // bcrypt hash, masked in API responses
	APIKey             string   `json:"api_key"`              // accepted via X-Api-Key header or ?apikey query
	LocalBypassSubnets []string `json:"local_bypass_subnets"` // CIDR ranges that skip auth (e.g. 192.168.1.0/24)
}

// LoggingConfig holds log level, format and file rotation policy. The level
// and rotation settings apply immediately when updated; the format only takes
// effect on restart.
type LoggingConfig struct {
	Level       string `json:"level"`        // debug|info|warn|error (default "info")
	Format      string `json:"format"`       // json|text (default "json")
	MaxSizeMB   int    `json:"max_size_mb"`  // rotate log file after this many MB (default 10, <=0 = default)
	MaxBackups  int    `json:"max_backups"`  // rotated files to keep (<=0 = default 3)
	MaxAgeDays  int    `json:"max_age_days"` // retention in days for rotated files (<=0 = default 7)
	Compress    *bool  `json:"compress"`     // gzip rotated log files (default true)
	CapturedMax int    `json:"captured_max"` // log lines returned by the UI viewer (default 2000)
	// AccessLog writes one structured line per request to logs/access.log,
	// separate from the app event log (nginx/Gitea style). Polling endpoints
	// are always excluded. Default false; takes effect on restart. Pointer so
	// partial updates can distinguish "not sent" from "false".
	AccessLog *bool `json:"access_log"`
}

// LoggerConfig translates the persisted logging config into the logger
// package's runtime config. A nil receiver yields defaults. A nil Compress
// means the value was never configured (legacy config) and is treated as
// false, matching the previous zero-value behavior.
func (c *LoggingConfig) LoggerConfig() logger.Config {
	if c == nil {
		return logger.DefaultConfig()
	}
	return logger.Config{
		Level:      c.Level,
		Format:     c.Format,
		MaxSizeMB:  c.MaxSizeMB,
		MaxBackups: c.MaxBackups,
		MaxAgeDays: c.MaxAgeDays,
		Compress:   c.Compress != nil && *c.Compress,
	}
}

var folderTokenRE = regexp.MustCompile(`\{[a-z_][a-z0-9_:]*\}`)

func intPtr(v int) *int { return &v }

func boolPtr(v bool) *bool { return &v }

// DefaultLogging returns a LoggingConfig populated with sensible defaults.
func DefaultLogging() *LoggingConfig {
	return &LoggingConfig{
		Level:       "info",
		Format:      "json",
		MaxSizeMB:   10,
		MaxBackups:  3,
		MaxAgeDays:  7,
		Compress:    boolPtr(true),
		CapturedMax: 2000,
		AccessLog:   boolPtr(false),
	}
}

// Default library paths target the Docker image's mount points. Every
// installation runs in Docker; local/command-line runs override these with
// GROOVEARR_* env vars before the config file is first created.
const (
	DefaultLibraryPath  = "/music"
	DefaultDownloadPath = "/downloads"
	DefaultPlaylistPath = "/playlists"
)

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Sources:        make(map[string]json.RawMessage),
		Logging:        DefaultLogging(),
		MetadataOrder:  []string{"deezer", "musicbrainz", "discogs"},
		DownloadOrder:  []string{"soulseek", "deezer"},
		AlbumSources:   []string{},
		DownloadClient: "",
		Library: LibraryConfig{
			DownloadPath:         DefaultDownloadPath,
			LibraryPath:          DefaultLibraryPath,
			FolderTemplate:       "{artist}/{album} ({year})/{track:02d} - {title}",
			CompilationTemplate:  "Various Artists/{album} ({year})/{track:02d}. {artist} - {title}",
			PlaylistPath:         DefaultPlaylistPath,
			MaxDownloadWorkers:   3,
			PlaylistTemplate:     "{position:02d} {artist} - {title}",
			PlaylistAutoSyncMins: intPtr(30),
		},
	}
}

// applyEnvPathDefaults overrides the default library paths from GROOVEARR_*
// environment variables. Applied only when no config file exists yet — a
// persisted config keeps whatever the user wrote. The Docker defaults are the
// container mount points; a local command-line run sets these env vars instead.
func applyEnvPathDefaults(cfg *Config) {
	if p := os.Getenv("GROOVEARR_LIBRARY_PATH"); p != "" {
		cfg.Library.LibraryPath = p
	}
	if p := os.Getenv("GROOVEARR_DOWNLOAD_PATH"); p != "" {
		cfg.Library.DownloadPath = p
	}
	if p := os.Getenv("GROOVEARR_PLAYLIST_PATH"); p != "" {
		cfg.Library.PlaylistPath = p
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
	if c.Library.PlaylistAutoSyncMins != nil && *c.Library.PlaylistAutoSyncMins > 0 && *c.Library.PlaylistAutoSyncMins < 5 {
		errs = append(errs, "library.playlist_auto_sync_mins: minimum 5 minutes (or 0 to disable)")
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

	// Logging.
	lc := c.Logging
	if lc == nil {
		lc = DefaultLogging()
	}
	if lc.Level != "" && !validLogLevels[strings.ToLower(lc.Level)] {
		errs = append(errs, fmt.Sprintf("logging.level: must be debug, info, warn, or error (got %q)", lc.Level))
	}
	if lc.Format != "" && strings.ToLower(lc.Format) != "json" && strings.ToLower(lc.Format) != "text" {
		errs = append(errs, fmt.Sprintf("logging.format: must be json or text (got %q)", lc.Format))
	}
	if lc.MaxSizeMB < 1 {
		errs = append(errs, "logging.max_size_mb: must be at least 1")
	}
	if lc.MaxBackups < 1 {
		errs = append(errs, "logging.max_backups: must be at least 1")
	}
	if lc.MaxAgeDays < 1 {
		errs = append(errs, "logging.max_age_days: must be at least 1")
	}

	return errs
}

var validLogLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "warning": true, "error": true,
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
	if partial.Library.PlaylistAutoSyncMins != nil {
		v := *partial.Library.PlaylistAutoSyncMins
		if c.Library.PlaylistAutoSyncMins == nil || v != *c.Library.PlaylistAutoSyncMins {
			c.Library.PlaylistAutoSyncMins = &v
		}
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

	// Logging — merge field-wise when present so partial updates never zero out
	// the retained settings. A nil pointer keeps the existing logging config.
	if partial.Logging != nil {
		if c.Logging == nil {
			c.Logging = DefaultLogging()
		}
		if partial.Logging.Level != "" {
			c.Logging.Level = partial.Logging.Level
		}
		if partial.Logging.Format != "" {
			c.Logging.Format = partial.Logging.Format
		}
		if partial.Logging.MaxSizeMB > 0 {
			c.Logging.MaxSizeMB = partial.Logging.MaxSizeMB
		}
		if partial.Logging.MaxBackups > 0 {
			c.Logging.MaxBackups = partial.Logging.MaxBackups
		}
		if partial.Logging.MaxAgeDays > 0 {
			c.Logging.MaxAgeDays = partial.Logging.MaxAgeDays
		}
		if partial.Logging.CapturedMax > 0 {
			c.Logging.CapturedMax = partial.Logging.CapturedMax
		}
		if partial.Logging.Compress != nil {
			c.Logging.Compress = partial.Logging.Compress
		}
		if partial.Logging.AccessLog != nil {
			c.Logging.AccessLog = partial.Logging.AccessLog
		}
	}

	// Order/source arrays merge when present (non-nil), so an explicitly empty
	// array from the UI clears the setting. JSON decodes an absent field to nil
	// and "[]" to an empty non-nil slice.
	if partial.MetadataOrder != nil {
		c.MetadataOrder = partial.MetadataOrder
	}
	if partial.DownloadOrder != nil {
		c.DownloadOrder = partial.DownloadOrder
	}

	// Album/torrent sources.
	if partial.AlbumSources != nil {
		c.AlbumSources = partial.AlbumSources
	}
	if partial.DownloadClient != "" {
		c.DownloadClient = partial.DownloadClient
	}
	if partial.SetupCompleted {
		c.SetupCompleted = true
	}
	if partial.Library.CompilationTemplate != "" {
		c.Library.CompilationTemplate = partial.Library.CompilationTemplate
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
	// A logging block may be absent (keep defaults) or partial (JSON zeroes
	// omitted fields). Normalize every non-specified value to its default so a
	// partial block can never silently drop e.g. compress=true or MaxSizeMB.
	if cfg.Logging == nil {
		cfg.Logging = DefaultLogging()
	} else {
		d := DefaultLogging()
		if cfg.Logging.Level == "" {
			cfg.Logging.Level = d.Level
		}
		if cfg.Logging.Format == "" {
			cfg.Logging.Format = d.Format
		}
		if cfg.Logging.MaxSizeMB <= 0 {
			cfg.Logging.MaxSizeMB = d.MaxSizeMB
		}
		if cfg.Logging.MaxBackups <= 0 {
			cfg.Logging.MaxBackups = d.MaxBackups
		}
		if cfg.Logging.MaxAgeDays <= 0 {
			cfg.Logging.MaxAgeDays = d.MaxAgeDays
		}
		if cfg.Logging.Compress == nil {
			cfg.Logging.Compress = d.Compress
		}
		if cfg.Logging.CapturedMax <= 0 {
			cfg.Logging.CapturedMax = d.CapturedMax
		}
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

// contractPaths restores the previous raw (on-disk) path form when a path was
// set to the normalized expansion of it. Consumers like the settings UI echo
// the absolute value Get() returns, so an unchanged relative path would
// otherwise be rewritten to a machine-specific absolute path on every save.
// Comparison is on the cleaned form so a trailing slash or ".." in the echo
// still contracts. Any genuinely new value (different absolute path, or a new
// relative path) is kept as written.
func contractPaths(cur *LibraryConfig, prev LibraryConfig) {
	if prev.DownloadPath != "" {
		if abs, err := filepath.Abs(prev.DownloadPath); err == nil && filepath.Clean(cur.DownloadPath) == abs {
			cur.DownloadPath = prev.DownloadPath
		}
	}
	if prev.LibraryPath != "" {
		if abs, err := filepath.Abs(prev.LibraryPath); err == nil && filepath.Clean(cur.LibraryPath) == abs {
			cur.LibraryPath = prev.LibraryPath
		}
	}
	if prev.PlaylistPath != "" {
		if abs, err := filepath.Abs(prev.PlaylistPath); err == nil && filepath.Clean(cur.PlaylistPath) == abs {
			cur.PlaylistPath = prev.PlaylistPath
		}
	}
}
