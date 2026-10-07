package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/user"
	usersqlite "github.com/ramonskie/groovearr/internal/user/sqlite"
)

// TestAuthEndToEndGate is the R3-M2 integration guard: it wires the real user
// SQLite store, bootstrap seeding, login, admin user creation, and the real
// withAuth -> route table chain, then asserts the authorization boundary holds
// end to end.
//
// Flow:
//
//	EnsureBootstrapAdmin (seeds the admin in a real store)
//	  -> login as the seeded admin
//	  -> POST /api/users (create a regular user through adminOnly)
//	  -> login as that user
//	  -> the user reaches a shared route (never 403) but is 403 on an admin route.
//
// This is intentionally not a unit test of any one handler: a regression that
// drops the adminOnly wrapper, breaks role resolution from the session, or
// stops issuing role-bearing sessions fails here.
func TestAuthEndToEndGate(t *testing.T) {
	ctx := context.Background()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := usersqlite.New(db)
	if err != nil {
		t.Fatalf("new user store: %v", err)
	}

	adminHash, err := config.HashPassword("admin-pass")
	if err != nil {
		t.Fatalf("hash admin password: %v", err)
	}

	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.Auth.Method = "forms"
		c.Auth.Username = "admin"
		// EnsureBootstrapAdmin stores this verbatim, so it must be a bcrypt hash.
		c.Auth.Password = adminHash
		return nil
	}); err != nil {
		t.Fatalf("config update: %v", err)
	}

	// Real bootstrap against the real (empty) store.
	if err := user.EnsureBootstrapAdmin(ctx, store, cfg.Get(), testAPILogger()); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	if seeded, err := store.GetUserByUsername(ctx, "admin"); err != nil || seeded == nil || seeded.Role != user.RoleAdmin {
		t.Fatalf("bootstrap did not seed an admin: (%+v, %v)", seeded, err)
	}

	sessions := newSessionStore()
	t.Cleanup(sessions.Shutdown)
	limiter := newIPRateLimiter(roleTestBuckets(), testAPILogger())
	t.Cleanup(limiter.Shutdown)

	srv := &Server{
		cfg:         cfg,
		log:         testAPILogger(),
		sessions:    sessions,
		userStore:   store,
		rateLimiter: limiter,
	}
	mux := http.NewServeMux()
	srv.registerAPIRoutes(mux)
	// mustNotPanic keeps the shared-route probe observable: GET /api/downloads
	// reaches a handler whose download service is nil here, which would panic;
	// it must surface as "not 403", not crash the test.
	handler := mustNotPanic(srv.withAuth(mux))

	// 1. Log in as the seeded admin, through the real chain.
	adminToken := loginCookie(t, handler, "admin", "admin-pass")

	// 2. Create a regular user through the admin-only route.
	rec := dispatchCookie(handler, http.MethodPost, "/api/users", adminToken,
		`{"username":"regular","password":"regular-pass","role":"user"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin POST /api/users = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}

	// 3. Log in as the created user.
	userToken := loginCookie(t, handler, "regular", "regular-pass")

	// 4a. The user authenticates and reaches the shared surface.
	if rec := dispatchCookie(handler, http.MethodGet, "/api/me", userToken, ""); rec.Code != http.StatusOK {
		t.Fatalf("user GET /api/me = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec := dispatchCookie(handler, http.MethodGet, "/api/downloads", userToken, ""); rec.Code == http.StatusForbidden {
		t.Fatalf("user GET /api/downloads = 403, want the shared route reachable (%s)", rec.Body.String())
	}

	// 4b. The user is blocked on the admin surface.
	if rec := dispatchCookie(handler, http.MethodGet, "/api/config", userToken, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("user GET /api/config = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// 4c. The admin still reaches the admin surface (proves the 403 above is a
	// role gate, not a broken route).
	if rec := dispatchCookie(handler, http.MethodGet, "/api/config", adminToken, ""); rec.Code == http.StatusForbidden {
		t.Fatalf("admin GET /api/config = 403, want the admin route reachable (%s)", rec.Body.String())
	}
}

// loginCookie posts credentials through h and returns the groovearr_sid value.
func loginCookie(t *testing.T, h http.Handler, username, password string) string {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + password + `"}`
	rec := dispatchCookie(h, http.MethodPost, "/api/login", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %q = %d, want 200 (%s)", username, rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "groovearr_sid" && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("login %q did not set a groovearr_sid cookie", username)
	return ""
}

// dispatchCookie runs one request through h carrying token as the session cookie.
func dispatchCookie(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, http.NoBody)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: token})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}
