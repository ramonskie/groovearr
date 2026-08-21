package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
