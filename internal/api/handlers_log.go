package api

import (
	"net/http"

	"github.com/ramonskie/groovearr/internal/logger"
)

// handleGetLogs returns the tail of the on-disk log file (the same lines
// docker logs surfaces) plus the file path and live log level. Live log lines
// are streamed separately over GET /api/events as "log_line" events by a file
// tailer in the app.
func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	level := ""
	if s.logRotator != nil {
		level = s.logRotator.Level()
	}

	limit := 500
	if s.cfg != nil && s.cfg.Get().Logging != nil && s.cfg.Get().Logging.CapturedMax > 0 {
		limit = s.cfg.Get().Logging.CapturedMax
	}

	entries := []logger.Entry{}
	if s.logPath != "" {
		if e, err := logger.ReadTail(s.logPath, limit); err == nil {
			entries = e
		} else {
			s.log.Warn("read log tail failed", "path", s.logPath, "error", err, "component", "api")
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"path":    s.logPath,
		"level":   level,
	})
}
