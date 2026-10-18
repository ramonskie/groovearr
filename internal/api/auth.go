package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/session"
	"github.com/ramonskie/groovearr/internal/user"
)

// sessionCookieName is the cookie carrying the opaque session token.
const sessionCookieName = "groovearr_sid"

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
// Sessions are persisted (internal/session) and survive a process restart. They
// use a 30-day idle window and slide forward on activity: once fewer than 15
// days remain, the same request extends the expiry to a fresh 30 days and
// re-sends the cookie (same token, no rotation).
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
			id := Identity{Role: user.RoleAdmin}
			accessIdentityFrom(r.Context()).record(id)
			next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
			return
		}

		// Always allow health check, login, and non-API paths (static files, SPA).
		if r.URL.Path == "/api/health" || r.URL.Path == "/api/login" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// Session cookie (forms/basic methods) — credentials take precedence
		// over the local bypass so a session's real role governs.
		if id, ok := s.sessionIdentity(w, r); ok {
			accessIdentityFrom(r.Context()).record(id)
			next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
			return
		}

		// API key (any supported transport) — an explicit admin credential.
		if s.hasValidAPIKey(r) {
			id := Identity{Role: user.RoleAdmin, ViaAPIKey: true}
			accessIdentityFrom(r.Context()).record(id)
			next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
			return
		}

		// Local bypass — no credential was presented, but the request comes
		// from a trusted subnet. Grant the least-privileged role (C4): never
		// admin, so the settings surface stays locked.
		if len(cfg.Auth.LocalBypassSubnets) > 0 && isInSubnet(r.RemoteAddr, cfg.Auth.LocalBypassSubnets) {
			id := Identity{Role: user.RoleUser}
			accessIdentityFrom(r.Context()).record(id)
			next.ServeHTTP(w, r.WithContext(contextWithIdentity(r.Context(), id)))
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

// sessionIdentity resolves the session cookie to an Identity.
//
// It renews the session in passing when the sliding-expiry threshold is
// reached: the same token is re-sent with a fresh expiry, so parallel tabs and
// in-flight requests can never be handed a stale cookie.
//
// Every ambiguous outcome is unauthenticated, never an error to the client: an
// absent, unknown, or expired cookie, a nil session service (test wiring), and
// a store failure all return false. A database outage fails closed; the caller
// then falls through to the API key and local bypass checks, or 401s.
func (s *Server) sessionIdentity(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" || s.sessions == nil {
		return Identity{}, false
	}

	ses, ok, err := s.sessions.Validate(r.Context(), cookie.Value)
	if err != nil {
		s.log.Error("auth: session lookup failed",
			"remote_addr", r.RemoteAddr,
			"error", err,
			"component", "api",
		)
		return Identity{}, false
	}
	if !ok {
		return Identity{}, false
	}

	if s.sessions.NeedsRenewal(ses) {
		expires, renewed, err := s.sessions.Renew(r.Context(), cookie.Value)
		if err != nil {
			// The session is still valid; a failed renewal only means the
			// expiry did not move. Keep the caller authenticated.
			s.log.Warn("auth: session renewal failed",
				"remote_addr", r.RemoteAddr,
				"error", err,
				"component", "api",
			)
		} else if renewed {
			writeSessionCookie(w, r, cookie.Value, expires)
		}
	}

	return Identity{UserID: ses.UserID, Username: ses.Username, Role: ses.Role}, true
}

// sessionPurgeContext detaches a security-relevant session mutation from the
// request lifetime. A client that disconnects mid-request must not cancel the
// delete and leave a credential live; the operation stays bounded.
func sessionPurgeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), sessionPurgeTimeout)
}

// discardSession removes a session that must not be released to the client
// (e.g. the account was disabled between the credential check and the insert).
// It uses a detached, bounded context and only logs a failure — the caller is
// already returning an error response.
func (s *Server) discardSession(ctx context.Context, token string) {
	ctx, cancel := sessionPurgeContext(ctx)
	defer cancel()
	if err := s.sessions.Delete(ctx, token); err != nil {
		s.log.Error("auth: discard session failed", "error", err, "component", "api")
	}
}

// writeSessionCookie sets the session cookie for token with the given expiry.
// MaxAge mirrors the sliding TTL so a browser discards the cookie at the same
// time the server-side row would expire; HttpOnly keeps it away from scripts and
// Secure is only ever added (see secureRequest).
func writeSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(session.TTL / time.Second),
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
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

	// Without a session service no cookie can be issued, so a valid credential
	// would be discarded. Treat it as the same server misconfiguration.
	if s.sessions == nil {
		s.log.Error("auth: login attempted without a session service", "component", "api")
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
	// rejection. A bcrypt comparison runs in every case — against a dummy hash
	// when there is no usable stored one — so response time does not reveal
	// whether a username exists or is disabled (username enumeration). Inputs
	// longer than 72 bytes take bcrypt's fast internal reject path, but that is
	// symmetric across accounts and reveals nothing.
	hash := dummyLoginHash()
	if u != nil && u.PasswordHash != "" {
		hash = u.PasswordHash
	}
	passwordOK := config.CheckPassword(hash, req.Password)
	if u == nil || u.Disabled || !passwordOK {
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
	token, expires, err := s.sessions.Create(r.Context(), *u)
	if err != nil {
		// Entropy failure: surface a 500 and issue no cookie. The credential
		// was valid but no session can be created safely.
		s.log.Error("auth: session create failed", "username", u.Username, "error", err, "component", "api")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// A security-relevant account change racing the bcrypt comparison can land
	// between the lookup above and this insert: a disable, delete, demotion, or
	// password reset whose purge completed before Create wrote the row. Compare
	// every field the purge protects — not just disabled — so a stale role or a
	// credential that was just revoked cannot be handed a session (with a
	// 30-day idle window the race would otherwise persist far longer than it
	// used to).
	refresh, err := s.userStore.GetUser(r.Context(), u.ID)
	if err != nil {
		s.log.Error("auth: post-login user re-check failed",
			"username", u.Username,
			"error", err,
			"component", "api",
		)
		s.discardSession(r.Context(), token)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if refresh == nil || refresh.Disabled || refresh.Role != u.Role || refresh.PasswordHash != u.PasswordHash {
		s.discardSession(r.Context(), token)
		s.log.Warn("auth: login raced an account change",
			"username", u.Username,
			"remote_addr", r.RemoteAddr,
			"component", "api",
		)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	writeSessionCookie(w, r, token, expires)

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
//
// The server-side row is deleted first, so the cookie is dead even if the
// browser ignores the clearing Set-Cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" && s.sessions != nil {
		ctx, cancel := sessionPurgeContext(r.Context())
		defer cancel()
		if err := s.sessions.Delete(ctx, cookie.Value); err != nil {
			s.log.Warn("auth: session delete failed",
				"remote_addr", r.RemoteAddr,
				"error", err,
				"component", "api",
			)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})

	s.log.Info("auth: logout",
		"remote_addr", r.RemoteAddr,
		"component", "api",
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// dummyLoginHash returns a bcrypt hash of a fixed throwaway string, computed
// once. handleLogin compares against it when there is no usable stored hash, so
// an unknown or disabled account costs the same bcrypt time as a wrong
// password and cannot be told apart by response time.
//
// The error path returns "" (CheckPassword then short-circuits); it is
// unreachable for the fixed 26-byte literal, and TestDummyLoginHashIsValid
// fails loudly if that ever changes.
var dummyLoginHash = sync.OnceValue(func() string {
	h, err := config.HashPassword("groovearr-timing-equalizer")
	if err != nil {
		return ""
	}
	return h
})

// secureRequest reports whether the request reached the app over TLS, directly
// or through a TLS-terminating reverse proxy (X-Forwarded-Proto: https).
//
// It is used only to add the Secure attribute to the session cookie. A direct
// r.TLS is checked first and OR-ed in, so Secure can never be removed from a
// direct-TLS request. Behind a proxy r.TLS is nil and X-Forwarded-Proto is the
// only signal, so the proxy MUST overwrite it (nginx's
// `proxy_set_header X-Forwarded-Proto $scheme;` does); a client-supplied header
// can then only force Secure on for its own response, never off. The first
// token of a comma-separated value is used, matching proxy conventions.
func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// ensureFormsTransition runs just before a config save that turns forms
// authentication on (from none/empty). It delegates the domain rule to
// user.EnsureFormsReady, which performs user-store I/O, so it MUST be called
// outside the config write lock (see handleUpdateConfig) — the config lock is
// held across every persistence mutation and blocking config readers on a DB
// round trip would stall the whole API.
//
// It is gated on the transition so an ordinary settings save made while forms
// is already active neither re-reads the store nor can be refused. The
// effective method mirrors config.Merge: a non-empty partial method wins.
//
// The Username/Password in the auth config are consumed exactly once, by
// EnsureBootstrapAdmin at startup, and only while the user store is empty. A
// runtime switch to "forms" therefore used to land with no account to log in
// with — and because PUT /api/config is adminOnly, the locked-out operator had
// no route back in short of the API key or editing the config file.
func (s *Server) ensureFormsTransition(ctx context.Context, base config.Config, partial config.Config) error {
	effective := base.Auth.Method
	if partial.Auth.Method != "" {
		effective = partial.Auth.Method
	}
	if base.Auth.Method == "forms" || effective != "forms" {
		return nil
	}

	username := base.Auth.Username
	if partial.Auth.Username != "" {
		username = partial.Auth.Username
	}

	// Only a password supplied in this request may seed the first admin. The
	// UI sends "" for "unchanged"; an API client echoing GET /api/config sends
	// the masked sentinel — both mean the stored hash, which is never re-seeded
	// (it may be a credential the operator no longer knows).
	var newPassword string
	if partial.Auth.Password != "" && !config.IsMaskedString(partial.Auth.Password) {
		newPassword = partial.Auth.Password
	}

	return user.EnsureFormsReady(ctx, s.userStore, user.FormsRequest{
		Username:    username,
		NewPassword: newPassword,
	}, s.log)
}
