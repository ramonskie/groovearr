package api

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/user"
)

// withAuth returns middleware that enforces authentication based on the
// configured auth method and injects the resolved Identity into the request
// context (see identityFrom) for the rest of the handler chain.
//
// Method = "none" (or empty): pass-through as admin, backwards compatible.
// Method = "forms" or "basic": session cookie OR API key accepted.
//
// Always allows: /api/health, /api/login, non-API paths (static files, SPA).
//
// Identity resolution, in order (explicit credentials win over the local
// bypass so a configured admin is never downgraded by their source subnet):
//   - no auth configured -> {Role: admin} (NOT access control);
//   - valid session      -> identity from the session (UserID, Username, Role);
//   - valid API key      -> {Role: admin, ViaAPIKey: true} (any transport);
//   - local bypass       -> {Role: user} (see security note below);
//   - otherwise          -> 401, no identity.
//
// Security note (finding C4): local_bypass_subnets used to grant the same
// full access as a valid credential. Now a LAN-bypassed host that presents no
// credential is authenticated only as a regular user (user.RoleUser). Being on
// a trusted subnet must not silently confer admin — the settings surface
// stays admin-only even from a bypassed host.
//
// Supported API key transports: X-Api-Key header, ?apikey query, Authorization: Bearer header.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.cfg.Get()

		// No auth configured — allow all as admin (Sonarr "None" behavior).
		// The caller is trusted by configuration, not by a credential; this is
		// backwards compatible and explicitly NOT access control.
		if cfg.Auth.Method == "" || cfg.Auth.Method == "none" {
			next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), Identity{Role: user.RoleAdmin})))
			return
		}

		// Always allow health check, login, and non-API paths (static files, SPA).
		if r.URL.Path == "/api/health" || r.URL.Path == "/api/login" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// Session cookie (forms/basic methods) — credentials take precedence
		// over the local bypass so a session's real role governs.
		if cookie, err := r.Cookie("groovearr_sid"); err == nil && cookie.Value != "" {
			if ses, ok := s.sessions.Validate(cookie.Value); ok {
				id := Identity{UserID: ses.UserID, Username: ses.Username, Role: ses.Role}
				next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
				return
			}
		}

		// API key (any supported transport) — an explicit admin credential.
		if s.hasValidAPIKey(r) {
			id := Identity{Role: user.RoleAdmin, ViaAPIKey: true}
			next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
			return
		}

		// Local bypass — no credential was presented, but the request comes
		// from a trusted subnet. Grant the least-privileged role (C4): never
		// admin, so the settings surface stays locked.
		if len(cfg.Auth.LocalBypassSubnets) > 0 && isInSubnet(r.RemoteAddr, cfg.Auth.LocalBypassSubnets) {
			next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), Identity{Role: user.RoleUser})))
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

// hasValidAPIKey reports whether the request carries the configured API key in
// any supported transport: X-Api-Key header, ?apikey query (SSE/EventSource),
// or Authorization: Bearer header. Comparisons are constant-time. The key is
// never returned or logged — callers only learn whether it matched.
func (s *Server) hasValidAPIKey(r *http.Request) bool {
	key := s.cfg.Get().Auth.APIKey
	if key == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Api-Key")), []byte(key)) == 1 {
		return true
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("apikey")), []byte(key)) == 1 {
		return true
	}
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
		if subtle.ConstantTimeCompare([]byte(authHeader[7:]), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

// meResponse is the JSON payload for GET /api/me.
//
// It reports the caller's coarse identity only: username, role, and whether the
// request authenticated with the API key. The key itself is never included.
type meResponse struct {
	Username  string `json:"username"`
	Role      string `json:"role"`
	ViaAPIKey bool   `json:"via_api_key"`
}

// handleMe returns the caller's identity.
// GET /api/me
//
// The route sits behind withAuth, which resolves the caller and injects the
// Identity into the request context. This handler only reports what withAuth
// established — it performs no authentication of its own:
//   - method "none" (or empty) -> admin (not access control);
//   - a valid session          -> that session's username/role;
//   - the API key              -> admin + via_api_key (any transport); the
//     key itself is never stored or echoed;
//   - a local-bypassed host    -> regular user, with an empty username.
//
// The defensive 401 covers the case where the handler is reached without
// middleware (a misconfiguration), so an unauthenticated caller is never
// misreported as valid.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	writeJSON(w, http.StatusOK, meResponse{
		Username:  id.Username,
		Role:      string(id.Role),
		ViaAPIKey: id.ViaAPIKey,
	})
}

// loginRequest is the JSON body for POST /api/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin authenticates a username/password against the persistent user
// store and, on success, sets the groovearr_sid session cookie.
// POST /api/login
//
// Only the "forms" auth method exposes this endpoint; "none" (and anything
// else) is rejected with the same message as before. The configured
// cfg.Auth.Username/Password are bootstrap-only and never consulted here —
// accounts live in the user store.
//
// Unknown user, disabled account, and wrong password all collapse to one 401
// "invalid credentials" response so the endpoint never reveals whether an
// account exists. Passwords are never logged.
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

	// A nil store means the server was wired without user persistence. No
	// credential can be verified against it, so fail closed rather than
	// dereferencing nil. This is a server misconfiguration, not a rejected
	// login, so it is a 500 — no session is ever issued.
	if s.userStore == nil {
		s.log.Error("auth: login attempted without a user store", "component", "api")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "authentication unavailable"})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Trim the username before lookup: the store matches case-insensitively
	// but a stray leading/trailing space would otherwise miss. The password is
	// compared as-is — whitespace there is significant.
	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password required"})
		return
	}

	u, err := s.userStore.GetUserByUsername(r.Context(), username)
	if err != nil {
		s.log.Error("auth: user lookup failed",
			"username", username,
			"remote_addr", r.RemoteAddr,
			"error", err,
			"component", "api",
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Unknown user, disabled account, or bad password: one indistinguishable
	// rejection. CheckPassword is only reached when the account exists and is
	// enabled, so a disabled user can never authenticate.
	if u == nil || u.Disabled || !config.CheckPassword(u.PasswordHash, req.Password) {
		s.log.Warn("auth: login failed",
			"username", username,
			"remote_addr", r.RemoteAddr,
			"component", "api",
		)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	// Session carries the persisted identity (ID, username, role) so every
	// authenticated request resolves the real role via withAuth.
	token, expires, err := s.sessions.Create(*u)
	if err != nil {
		// Entropy failure: surface a 500 and issue no cookie. The credential
		// was valid but no session can be created safely.
		s.log.Error("auth: session create failed", "username", u.Username, "error", err, "component", "api")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "groovearr_sid",
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})

	s.log.Info("auth: login succeeded",
		"username", u.Username,
		"remote_addr", r.RemoteAddr,
		"component", "api",
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// isInSubnet returns true if addr is within any of the given CIDR ranges.
func isInSubnet(addr string, subnets []string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range subnets {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
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
