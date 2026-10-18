package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/session"
	"github.com/ramonskie/groovearr/internal/user"
)

// failingSessionStore makes every lookup fail, standing in for a database
// outage. withAuth must fail closed: no identity, 401, never a bypass.
type failingSessionStore struct{}

func (failingSessionStore) Create(context.Context, session.Session) error {
	return errors.New("db down")
}
func (failingSessionStore) Get(context.Context, string) (session.Session, bool, error) {
	return session.Session{}, false, errors.New("db down")
}
func (failingSessionStore) Touch(context.Context, string, time.Time) (bool, error) {
	return false, errors.New("db down")
}
func (failingSessionStore) Delete(context.Context, string) error        { return errors.New("db down") }
func (failingSessionStore) DeleteByUserID(context.Context, int64) error { return errors.New("db down") }
func (failingSessionStore) DeleteExpired(context.Context, time.Time) (int64, error) {
	return 0, errors.New("db down")
}

// sessionsServer builds a forms-auth Server over the given session service plus
// the in-memory store behind it, so a test can age a row.
func sessionsServer(t *testing.T, svc *session.Service) *Server {
	t.Helper()
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.Auth.Method = "forms"
		return nil
	}); err != nil {
		t.Fatalf("config update: %v", err)
	}
	return &Server{cfg: cfg, log: testAPILogger(), sessions: svc}
}

// ageSession rewrites the stored expiry so the session is past the renewal
// threshold, simulating a returning visitor near the end of the idle window.
func ageSession(t *testing.T, store *memSessionStore, token string, expiresAt time.Time) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	hash := session.HashToken(token)
	row, ok := store.rows[hash]
	if !ok {
		t.Fatalf("session row %q not found", hash)
	}
	row.ExpiresAt = expiresAt
	store.rows[hash] = row
}

// storedSession returns the persisted row for a token.
func storedSession(t *testing.T, store *memSessionStore, token string) session.Session {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	row, ok := store.rows[session.HashToken(token)]
	if !ok {
		t.Fatal("session row missing")
	}
	return row
}

// TestWithAuthRenewsSessionOnActivity is the sliding-expiry guard: a returning
// visitor past the halfway point keeps the same token but gets a fresh 30-day
// expiry, both in the store and in the re-sent cookie.
func TestWithAuthRenewsSessionOnActivity(t *testing.T) {
	store := &memSessionStore{}
	svc := session.NewService(store, testAPILogger())
	t.Cleanup(svc.Shutdown)
	srv := sessionsServer(t, svc)

	token, _, err := svc.Create(context.Background(), user.User{ID: 3, Username: "alice", Role: user.RoleAdmin})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Less than RenewAfter left: the next request must extend the session.
	ageSession(t, store, token, time.Now().Add(session.RenewAfter-time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.withAuth(&captureIdentity{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	cookie := findSessionCookie(rec.Result().Cookies())
	if cookie == nil {
		t.Fatal("renewal did not re-send the session cookie")
	}
	if cookie.Value != token {
		t.Fatalf("cookie token changed on renewal (%q -> %q); the token must not rotate", token, cookie.Value)
	}
	if cookie.MaxAge != int(session.TTL/time.Second) {
		t.Fatalf("cookie MaxAge = %d, want %d", cookie.MaxAge, int(session.TTL/time.Second))
	}
	stored := storedSession(t, store, token)
	if !cookie.Expires.Equal(stored.ExpiresAt) {
		t.Fatalf("cookie expiry = %v, stored expiry = %v; they must agree", cookie.Expires, stored.ExpiresAt)
	}
	if remaining := time.Until(stored.ExpiresAt); remaining < session.TTL-time.Minute {
		t.Fatalf("stored expiry = %v from now, want a full window", remaining)
	}
}

// TestWithAuthDoesNotRenewFreshSession proves the write is rare: a session with
// more than half its window left is used as-is and no cookie is re-sent.
func TestWithAuthDoesNotRenewFreshSession(t *testing.T) {
	store := &memSessionStore{}
	svc := session.NewService(store, testAPILogger())
	t.Cleanup(svc.Shutdown)
	srv := sessionsServer(t, svc)

	token, _, err := svc.Create(context.Background(), user.User{ID: 3, Username: "alice", Role: user.RoleAdmin})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	before := storedSession(t, store, token).ExpiresAt

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.withAuth(&captureIdentity{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cookie := findSessionCookie(rec.Result().Cookies()); cookie != nil {
		t.Fatalf("fresh session was renewed (cookie %q)", cookie.Value)
	}
	if after := storedSession(t, store, token).ExpiresAt; !after.Equal(before) {
		t.Fatalf("expiry changed on a fresh session: %v -> %v", before, after)
	}
}

// TestWithAuthSessionStoreFailureFailsClosed is the security guard for the new
// database dependency: a lookup error must resolve no identity and 401. The
// caller still falls through to the other transports (API key, local bypass),
// but a database outage can never by itself authenticate anyone.
func TestWithAuthSessionStoreFailureFailsClosed(t *testing.T) {
	svc := session.NewService(failingSessionStore{}, testAPILogger())
	t.Cleanup(svc.Shutdown)
	srv := sessionsServer(t, svc)
	var logs bytes.Buffer
	srv.log = slog.New(slog.NewTextHandler(&logs, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "token"})
	capture := &captureIdentity{}
	rec := httptest.NewRecorder()
	srv.withAuth(capture).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if capture.seen {
		t.Fatal("a store failure injected an identity")
	}
	if !strings.Contains(logs.String(), "session lookup failed") {
		t.Fatalf("lookup failure was not logged; logs=%s", logs.String())
	}
}

// touchFailStore lets lookups succeed but fails the renewal write, simulating a
// transient write failure on a valid session.
type touchFailStore struct{ *memSessionStore }

func (touchFailStore) Touch(context.Context, string, time.Time) (bool, error) {
	return false, errors.New("db down")
}

// TestWithAuthRenewalFailureKeepsCallerAuthenticated: a failed renewal must not
// log the caller out — the session is still valid, only its expiry did not move.
func TestWithAuthRenewalFailureKeepsCallerAuthenticated(t *testing.T) {
	base := &memSessionStore{}
	svc := session.NewService(touchFailStore{base}, testAPILogger())
	t.Cleanup(svc.Shutdown)
	srv := sessionsServer(t, svc)

	token, _, err := svc.Create(context.Background(), user.User{ID: 8, Username: "dave", Role: user.RoleUser})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ageSession(t, base, token, time.Now().Add(session.RenewAfter-time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	capture := &captureIdentity{}
	rec := httptest.NewRecorder()
	srv.withAuth(capture).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite the failed renewal (body %s)", rec.Code, rec.Body.String())
	}
	if !capture.seen || capture.id.Username != "dave" {
		t.Fatalf("identity = %+v seen=%v, want dave", capture.id, capture.seen)
	}
	if cookie := findSessionCookie(rec.Result().Cookies()); cookie != nil {
		t.Fatal("a failed renewal still re-sent the cookie")
	}
}

// TestWithAuthExpiredSessionIs401 covers the middleware path for an expired
// cookie: no identity is injected and the request is rejected.
func TestWithAuthExpiredSessionIs401(t *testing.T) {
	store := &memSessionStore{}
	svc := session.NewService(store, testAPILogger())
	t.Cleanup(svc.Shutdown)
	srv := sessionsServer(t, svc)

	token, _, err := svc.Create(context.Background(), user.User{ID: 5, Username: "carol", Role: user.RoleAdmin})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ageSession(t, store, token, time.Now().Add(-time.Minute))

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	capture := &captureIdentity{}
	rec := httptest.NewRecorder()
	srv.withAuth(capture).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if capture.seen {
		t.Fatal("an expired session injected an identity")
	}
}
