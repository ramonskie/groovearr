package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ramonskie/groovearr/internal/config"
)

// withAuth returns middleware that enforces authentication based on the
// configured auth method.
//
// Method = "none" (or empty): pass-through, backwards compatible.
// Method = "forms" or "basic": session cookie OR API key accepted.
//
// Always allows: /api/health, /api/login, non-API paths (static files, SPA).
//
// Supported API key transports: X-Api-Key header, ?apikey query, Authorization: Bearer header.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.cfg.Get()

		// No auth configured — allow all.
		if cfg.Auth.Method == "" || cfg.Auth.Method == "none" {
			next.ServeHTTP(w, r)
			return
		}

		// Always allow health check, login, and non-API paths.
		if r.URL.Path == "/api/health" || r.URL.Path == "/api/login" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// Session cookie (forms/basic methods).
		if cookie, err := r.Cookie("groovearr_sid"); err == nil && cookie.Value != "" {
			if username, ok := s.sessions.Validate(cookie.Value); ok {
				_ = username // available for future auditing/logging
				next.ServeHTTP(w, r)
				return
			}
		}

		// API key via X-Api-Key header.
		if cfg.Auth.APIKey != "" && subtle.ConstantTimeCompare(
			[]byte(r.Header.Get("X-Api-Key")),
			[]byte(cfg.Auth.APIKey),
		) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		// API key via ?apikey query parameter (SSE/EventSource).
		if cfg.Auth.APIKey != "" && subtle.ConstantTimeCompare(
			[]byte(r.URL.Query().Get("apikey")),
			[]byte(cfg.Auth.APIKey),
		) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		// API key via Authorization: Bearer header.
		if cfg.Auth.APIKey != "" {
			authHeader := r.Header.Get("Authorization")
			if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
				if subtle.ConstantTimeCompare(
					[]byte(authHeader[7:]),
					[]byte(cfg.Auth.APIKey),
				) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}
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

// loginRequest is the JSON body for POST /api/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin validates credentials and sets a session cookie.
// POST /api/login
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	cfg := s.cfg.Get()

	// Only forms auth supports login endpoint.
	if cfg.Auth.Method != "forms" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "login not available with current auth method"})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password required"})
		return
	}

	valid := config.CheckPassword(cfg.Auth.Password, req.Password) &&
		subtle.ConstantTimeCompare([]byte(req.Username), []byte(cfg.Auth.Username)) == 1

	if !valid {
		s.log.Warn("auth: login failed",
			"username", req.Username,
			"remote_addr", r.RemoteAddr,
			"component", "api",
		)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	token, expires := s.sessions.Create(req.Username)
	http.SetCookie(w, &http.Cookie{
		Name:     "groovearr_sid",
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	s.log.Info("auth: login succeeded",
		"username", req.Username,
		"remote_addr", r.RemoteAddr,
		"component", "api",
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleLogout clears the session cookie.
// POST /api/logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("groovearr_sid"); err == nil && cookie.Value != "" {
		s.sessions.Delete(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "groovearr_sid",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	s.log.Info("auth: logout",
		"remote_addr", r.RemoteAddr,
		"component", "api",
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
