package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// withAuth returns middleware that enforces API key authentication.
//
// When no API key is configured, all requests pass through — this is the
// default, backwards-compatible behavior.
//
// Health check and non-API routes (static files, SPA) are always allowed.
//
// Supported auth methods:
//   - X-Api-Key header (primary method for fetch/XHR requests)
//   - ?apikey query parameter (for SSE EventSource which cannot set headers)
//
// Reverse proxy authentication (e.g. nginx auth_request) should be handled
// at the proxy level, not by trusting arbitrary forwarded headers.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.cfg.Get()

		// No auth configured — allow all (backwards compatible default).
		if cfg.Auth.APIKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Always allow health check and non-API paths (static files, SPA).
		if r.URL.Path == "/api/health" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// API key via header (primary method for fetch/XHR requests).
		if subtle.ConstantTimeCompare(
			[]byte(r.Header.Get("X-Api-Key")),
			[]byte(cfg.Auth.APIKey),
		) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		// API key via query parameter (for SSE EventSource which cannot set headers).
		if subtle.ConstantTimeCompare(
			[]byte(r.URL.Query().Get("apikey")),
			[]byte(cfg.Auth.APIKey),
		) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		s.log.Warn("auth: unauthorized request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
			"component", "api",
		)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
	})
}
