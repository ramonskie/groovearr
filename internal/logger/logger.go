// Package logger provides a thin wrapper around log/slog with convenient
// constructors for production (JSON) and development (text) output.
//
// Logs are written to both stderr (for docker logs) and a rotating file
// (rotation/cleanup/compression handled by lumberjack). The on-disk file is
// the source of truth for the settings UI log viewer: the API reads its tail
// for history and a Tailer follows it for live updates.
//
// Usage:
//
//	log, rot, closeFn := logger.New(logger.DefaultConfig(), "/config/logs/groovearr.log")
//	defer closeFn()
//	log.Info("server started", "port", 8080)
//	rot.SetLevel("debug") // change level at runtime
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger is a type alias for slog.Logger so callers don't need to import slog.
type Logger = slog.Logger

// Config controls the logger's level, output format and file rotation policy.
type Config struct {
	// Level filters emitted records (debug|info|warn|error). Default "info".
	Level string
	// Format is the file/stderr serialization (json|text). Default "json".
	// Changing it only takes effect when the logger is constructed.
	Format string
	// MaxSizeMB is the size a log file may reach before a rotation occurs.
	// Default 10. Values <= 0 are treated as the default.
	MaxSizeMB int
	// MaxBackups is the max number of rotated log files retained.
	// Values <= 0 are treated as the default (3); the underlying lumberjack
	// semantics (0 = keep all) are not currently exposed.
	MaxBackups int
	// MaxAgeDays is the max number of days a rotated file is retained.
	// Values <= 0 are treated as the default (7); the underlying lumberjack
	// semantics (0 = keep forever) are not currently exposed.
	MaxAgeDays int
	// Compress gzips rotated log files. Default true.
	Compress bool
}

// DefaultConfig returns sensible production logging defaults.
func DefaultConfig() Config {
	return Config{
		Level:      "info",
		Format:     "json",
		MaxSizeMB:  10,
		MaxBackups: 3,
		MaxAgeDays: 7,
		Compress:   true,
	}
}

// Rotator owns the rotating log file and the runtime-adjustable log level.
// Level changes apply immediately; rotation options (size/backups/age/compress)
// are re-read by lumberjack on every write, so they also apply immediately.
type Rotator struct {
	mu  sync.Mutex
	lj  *lumberjack.Logger
	lvl *slog.LevelVar
}

// Write routes serialized records to the rotating file. Implemented so
// Rotator can be the io.Writer target of New() while config changes
// (SetConfig/Close) stay serialized against in-flight writes.
//
// Errors from the log file are swallowed deliberately: inside an
// io.MultiWriter a failing writer aborts the whole chain, which would take
// stderr (docker logs) down with a broken/unwritable file. slog ignores
// handler write errors anyway, so swallowing only affects file-side logging.
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lj == nil {
		return len(p), nil
	}
	_, _ = r.lj.Write(p)
	return len(p), nil
}

// Path returns the rotating log file path.
func (r *Rotator) Path() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lj == nil {
		return ""
	}
	return r.lj.Filename
}

// SetLevel updates the runtime log level.
func (r *Rotator) SetLevel(s string) {
	if r == nil || r.lvl == nil {
		return
	}
	setLevel(r.lvl, s)
}

// Level returns the current log level as a string.
func (r *Rotator) Level() string {
	if r == nil || r.lvl == nil {
		return "info"
	}
	switch r.lvl.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

// SetConfig applies rotation retention options live. Format is intentionally
// not applied here — it only takes effect at startup.
func (r *Rotator) SetConfig(c Config) {
	if r == nil {
		return
	}
	if c.MaxSizeMB <= 0 {
		c.MaxSizeMB = 10
	}
	if c.MaxBackups <= 0 {
		c.MaxBackups = 3
	}
	if c.MaxAgeDays <= 0 {
		c.MaxAgeDays = 7
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lj == nil {
		return
	}
	r.lj.MaxSize = c.MaxSizeMB
	r.lj.MaxBackups = c.MaxBackups
	r.lj.MaxAge = c.MaxAgeDays
	r.lj.Compress = c.Compress
}

// Close flushes and closes the rotating log file.
func (r *Rotator) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lj == nil {
		return nil
	}
	if err := r.lj.Close(); err != nil {
		return err
	}
	r.lj = nil
	return nil
}

// New constructs the production logger: records are serialized (JSON by
// default) to stderr AND the rotating file at filePath. The on-disk file is
// the source of truth for the settings UI log viewer.
//
// LOG_LEVEL and LOG_FORMAT environment variables override the provided config
// (legacy behavior). The returned closeFn flushes and closes the log file.
func New(cfg Config, filePath string) (l *Logger, rot *Rotator, closeFn func()) {
	cfg = applyDefaults(cfg)

	// Legacy env overrides.
	if v := strings.ToLower(os.Getenv("LOG_LEVEL")); v != "" {
		cfg.Level = v
	}
	if v := strings.ToLower(os.Getenv("LOG_FORMAT")); v != "" {
		cfg.Format = v
	}

	rot = newRotator(cfg, filePath)
	rot.SetLevel(cfg.Level)

	// Write each serialized record to both docker stdout and the rotating file.
	out := io.MultiWriter(os.Stderr, rot)

	opts := &slog.HandlerOptions{Level: rot.lvl}
	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(out, opts)
	} else {
		handler = slog.NewJSONHandler(out, opts)
	}

	return slog.New(handler), rot, func() {
		_ = rot.Close()
	}
}

// NewAccessWriter returns a Rotator writing only to a rotating file, with no
// stderr output. It backs the dedicated access log: per-request lines live in
// their own file (nginx/Gitea style) so the app event log and docker logs stay
// free of request noise. Rotation/retention follow the provided Config. The
// returned closeFn flushes and closes the file.
func NewAccessWriter(cfg Config, filePath string) (*Rotator, func()) {
	cfg = applyDefaults(cfg)
	rot := newRotator(cfg, filePath)
	return rot, func() {
		_ = rot.Close()
	}
}

func newRotator(cfg Config, filePath string) *Rotator {
	return &Rotator{
		lj: &lumberjack.Logger{
			Filename:   filePath,
			MaxSize:    cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAgeDays,
			Compress:   cfg.Compress,
			LocalTime:  true,
		},
		lvl: new(slog.LevelVar),
	}
}

// NewDev creates a development Logger: text output to stderr, DEBUG level,
// no file output. Ignores LOG_FORMAT/LOG_LEVEL env vars.
func NewDev() *Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

func applyDefaults(cfg Config) Config {
	d := DefaultConfig()
	if cfg.Level == "" {
		cfg.Level = d.Level
	}
	if cfg.Format == "" {
		cfg.Format = d.Format
	}
	if cfg.MaxSizeMB <= 0 {
		cfg.MaxSizeMB = d.MaxSizeMB
	}
	if cfg.MaxBackups <= 0 {
		cfg.MaxBackups = d.MaxBackups
	}
	if cfg.MaxAgeDays <= 0 {
		cfg.MaxAgeDays = d.MaxAgeDays
	}
	return cfg
}

func setLevel(lvl *slog.LevelVar, s string) {
	switch strings.ToLower(s) {
	case "debug":
		lvl.Set(slog.LevelDebug)
	case "warn", "warning":
		lvl.Set(slog.LevelWarn)
	case "error":
		lvl.Set(slog.LevelError)
	default:
		lvl.Set(slog.LevelInfo)
	}
}
