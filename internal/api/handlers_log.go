package api

import (
	"net/http"

	"github.com/ramonskie/groovearr/internal/logger"
)

// handleGetLogs returns the current in-memory log buffer snapshot along with
// the on-disk log file path and the live log level. Live log lines are
// streamed separately over GET /api/events as "log_line" events.
func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	level := ""
	if s.logRotator != nil {
		level = s.logRotator.Level()
	}

	entries := []logger.Entry{}
	if s.logBuffer != nil {
		entries = s.logBuffer.Snapshot()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"path":    s.logPath,
		"level":   level,
	})
}

// handleClearLogs empties the in-memory log buffer. On-disk log files (and
// their rotation history) are left untouched.
func (s *Server) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	if s.logBuffer != nil {
		s.logBuffer.Clear()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}
