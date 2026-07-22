// Package logger provides a thin wrapper around log/slog with convenient
// constructors for production (JSON) and development (text) output.
//
// Usage:
//
//	log := logger.New(logger.NewDefault())
//	log.Info("server started", "port", 8080)
//	log.Error("failed to connect", "error", err)
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Logger is a type alias for slog.Logger so callers don't need to import slog.
type Logger = slog.Logger

// New wraps an slog.Handler into a *Logger.
func New(handler slog.Handler) *Logger {
	return slog.New(handler)
}

// NewDefault creates a production Logger: JSON output to stderr, INFO level by
// default. Override via LOG_FORMAT (json|text) and LOG_LEVEL (debug|info|warn|error).
func NewDefault() *Logger {
	format := strings.ToLower(os.Getenv("LOG_FORMAT"))
	level := parseLevel(os.Getenv("LOG_LEVEL"))

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level}

	switch format {
	case "text":
		handler = slog.NewTextHandler(os.Stderr, opts)
	default:
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}

// NewDev creates a development Logger: text output to stderr, DEBUG level.
// Ignores LOG_FORMAT/LOG_LEVEL env vars.
func NewDev() *Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
