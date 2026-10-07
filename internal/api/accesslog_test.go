package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/logger"
	"github.com/ramonskie/groovearr/internal/user"
)

func TestWithAccessLog(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	})

	t.Run("disabled is a no-op", func(t *testing.T) {
		h := withAccessLog(nil)(ok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if rec.Code != http.StatusCreated {
			t.Fatalf("disabled middleware must still serve the handler, got %d", rec.Code)
		}
	})

	// Regression: a disabled access log is stored as a typed-nil
	// *logger.Rotator. Converting it to io.Writer yields a non-nil interface
	// wrapping a nil pointer — the middleware's `accessLog == nil` guard must
	// not let that reach Write, which would panic on the nil receiver.
	t.Run("typed-nil rotator is a no-op", func(t *testing.T) {
		var rot *logger.Rotator
		h := withAccessLog(rot)(ok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if rec.Code != http.StatusCreated {
			t.Fatalf("typed-nil middleware must still serve the handler, got %d", rec.Code)
		}
	})

	t.Run("skips poll paths entirely", func(t *testing.T) {
		var buf bytes.Buffer
		h := withAccessLog(&buf)(ok)
		for _, path := range []string{"/api/jobs", "/api/downloads", "/api/playlists/42", "/api/events", "/api/health"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusCreated {
				t.Fatalf("%s: unexpected status %d", path, rec.Code)
			}
		}
		if buf.Len() != 0 {
			t.Fatalf("poll paths were logged: %q", buf.Bytes())
		}
	})

	t.Run("logs actions on the same route shape as a poll", func(t *testing.T) {
		var buf bytes.Buffer
		h := withAccessLog(&buf)(ok)
		for _, m := range []string{http.MethodPatch, http.MethodDelete} {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(m, "/api/playlists/42", nil))
		}
		// GET /api/playlists (the list endpoint, not polled) must also be logged.
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/playlists", nil))
		// Playlist source browse actions share the /api/playlists/ prefix but
		// are user-triggered, not polls — must stay logged.
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/playlists/sources", nil))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/playlists/sources/spotify", nil))
		lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
		if len(lines) != 5 {
			t.Fatalf("expected 5 access lines (PATCH, DELETE, list GET, sources GET, browse GET), got %d: %q", len(lines), buf.Bytes())
		}
	})

	t.Run("logs real requests with fields", func(t *testing.T) {
		var buf bytes.Buffer
		h := withAccessLog(&buf)(ok)
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.RemoteAddr = "10.0.0.1:4242"
		req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
		req.Header.Set("User-Agent", "test-agent")
		req.Header.Set("Referer", "http://example/")
		h.ServeHTTP(httptest.NewRecorder(), req)

		lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
		if len(lines) != 1 {
			t.Fatalf("expected 1 access line, got %d: %q", len(lines), buf.Bytes())
		}
		var rec map[string]any
		if err := json.Unmarshal(lines[0], &rec); err != nil {
			t.Fatalf("access line not valid JSON: %v", err)
		}
		for field, want := range map[string]any{
			"method":      "GET",
			"path":        "/api/config",
			"proto":       "HTTP/1.1",
			"status":      float64(http.StatusCreated),
			"bytes":       float64(len("hello")),
			"remote_addr": "203.0.113.9", // X-Forwarded-For first hop, not the proxy peer
			"user_agent":  "test-agent",
			"referer":     "http://example/",
		} {
			if got := rec[field]; got != want {
				t.Errorf("%s: got %v, want %v", field, got, want)
			}
		}
		if _, ok := rec["duration_ms"]; !ok {
			t.Error("duration_ms missing")
		}
		if _, ok := rec["time"]; !ok {
			t.Error("time missing")
		}
	})

	t.Run("does not skip job actions with different paths", func(t *testing.T) {
		var buf bytes.Buffer
		h := withAccessLog(&buf)(ok)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/jobs/scan", nil))
		if buf.Len() == 0 {
			t.Fatal("POST /api/jobs/scan should be logged (only the poll GET /api/jobs is skipped)")
		}
	})
}

// TestWithAccessLogUserAttribution drives requests through the real middleware
// chain (withAccessLog -> withAuth) and asserts the access-log line carries the
// caller resolved by withAuth. This is the M8 regression: withAuth injects its
// Identity into a *new* request context, so the outer log middleware cannot
// read it from the request it holds — only from the shared accessIdentity
// holder installed before the chain ran.
func TestWithAccessLogUserAttribution(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// A shared, logged (never-skipped) library route: the download surface M8
	// exists to attribute.
	const sharedPath = "/api/library/tracks/1/download"

	// decodeLine parses the single access-log line written to buf.
	decodeLine := func(t *testing.T, buf *bytes.Buffer) map[string]any {
		t.Helper()
		lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
		if len(lines) != 1 {
			t.Fatalf("expected 1 access line, got %d: %q", len(lines), buf.Bytes())
		}
		var rec map[string]any
		if err := json.Unmarshal(lines[0], &rec); err != nil {
			t.Fatalf("access line not valid JSON: %v", err)
		}
		return rec
	}

	t.Run("session records the username", func(t *testing.T) {
		srv, sessions := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			return nil
		})
		token, _, _ := sessions.Create(user.User{ID: 7, Username: "alice", Role: user.RoleAdmin})

		req := httptest.NewRequest(http.MethodGet, sharedPath, nil)
		req.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: token})
		var buf bytes.Buffer
		rec := httptest.NewRecorder()
		withAccessLog(&buf)(srv.withAuth(ok)).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		got := decodeLine(t, &buf)
		if got["user"] != "alice" {
			t.Errorf("user = %v, want alice", got["user"])
		}
		if got["via_api_key"] != false {
			t.Errorf("via_api_key = %v, want false", got["via_api_key"])
		}
	})

	t.Run("api key records via_api_key and never the key", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			c.Auth.APIKey = testMeAPIKey
			return nil
		})

		req := httptest.NewRequest(http.MethodGet, sharedPath, nil)
		req.Header.Set("X-Api-Key", testMeAPIKey)
		var buf bytes.Buffer
		rec := httptest.NewRecorder()
		withAccessLog(&buf)(srv.withAuth(ok)).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		got := decodeLine(t, &buf)
		if got["user"] != "" {
			t.Errorf("user = %v, want empty for api key", got["user"])
		}
		if got["via_api_key"] != true {
			t.Errorf("via_api_key = %v, want true", got["via_api_key"])
		}
		if strings.Contains(buf.String(), testMeAPIKey) {
			t.Fatalf("raw API key leaked into access log: %q", buf.Bytes())
		}
	})

	t.Run("unauthenticated is still logged with a blank user", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			c.Auth.APIKey = testMeAPIKey
			return nil
		})

		req := httptest.NewRequest(http.MethodGet, sharedPath, nil)
		var buf bytes.Buffer
		rec := httptest.NewRecorder()
		withAccessLog(&buf)(srv.withAuth(ok)).ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		got := decodeLine(t, &buf)
		if got["user"] != "" {
			t.Errorf("user = %v, want empty", got["user"])
		}
		if got["via_api_key"] != false {
			t.Errorf("via_api_key = %v, want false", got["via_api_key"])
		}
	})
}
