package soulseek

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestExtractID covers both daemon response shapes for search creation:
// slskd returns {"id": ...} while slskr returns {"searchId": ...}.
func TestExtractID(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"slskd id", `{"id":"abc","searchText":"x"}`, "abc"},
		{"slskr searchId", `{"searchId":"3","query":"x","results":[]}`, "3"},
		{"id preferred over searchId", `{"searchId":"3","id":"abc"}`, "abc"},
		{"numeric id", `{"id":42}`, "42"},
		{"large numeric id not scientific", `{"id":1000000}`, "1000000"},
		{"13-digit numeric id", `{"id":1234567890123}`, "1234567890123"},
		{"null id falls through to searchId", `{"id":null,"searchId":"3"}`, "3"},
		{"empty id falls through to searchId", `{"id":"","searchId":"3"}`, "3"},
		{"null id only", `{"id":null}`, ""},
		{"list form", `[{"id":"arr-1"}]`, "arr-1"},
		{"list searchId", `[{"searchId":"arr-2"}]`, "arr-2"},
		{"empty object", `{}`, ""},
		{"invalid json", `{bad`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractID(json.RawMessage(tt.raw)); got != tt.want {
				t.Errorf("extractID(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestExtractDownloadID covers both enqueue response shapes:
// slskd returns {"enqueued":[{"id":...}]} while slskr returns
// {"blocked":[],"queued":N,"transfers":[{"id":...}]}.
func TestExtractDownloadID(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"slskd enqueued", `{"enqueued":[{"id":"e1","filename":"a.flac"}],"failed":[]}`, "e1"},
		{"slskr transfers", `{"blocked":[],"queued":1,"transfers":[{"id":"t1","filename":"a.flac"}]}`, "t1"},
		{"null entry id falls back", `{"transfers":[{"id":null}]}`, "fallback"},
		{"empty entry id falls back", `{"transfers":[{"id":""}]}`, "fallback"},
		{"flat id", `{"id":"flat"}`, "flat"},
		{"empty falls back", `{}`, "fallback"},
		{"invalid json falls back", `{bad`, "fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractDownloadID(json.RawMessage(tt.raw), "fallback"); got != tt.want {
				t.Errorf("extractDownloadID(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeDaemon(t *testing.T) {
	tests := map[string]string{
		"":        daemonAuto,
		"auto":    daemonAuto,
		"slskd":   daemonSlskd,
		"slskr":   daemonSlskr,
		"SLSKR":   daemonSlskr,
		" slskd ": daemonSlskd,
		"bogus":   daemonAuto,
	}
	for in, want := range tests {
		if got := normalizeDaemon(in); got != want {
			t.Errorf("normalizeDaemon(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectDaemon(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		appState   string
		want       string
	}{
		// Explicit config wins over the payload.
		{"explicit slskr", daemonSlskr, `{"version":{}}`, daemonSlskr},
		{"explicit slskd", daemonSlskd, `{"product":"slskR"}`, daemonSlskd},
		// Auto: slskr adds a top-level "product" the slskd state DTO lacks.
		{"auto slskr", daemonAuto, `{"product":"slskR","server":{}}`, daemonSlskr},
		{"auto slskd", daemonAuto, `{"version":{"current":"0.23.0"},"server":{}}`, daemonSlskd},
		{"auto empty body", daemonAuto, `{}`, daemonSlskd},
		{"auto invalid body", daemonAuto, `{bad`, daemonSlskd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectDaemon(tt.configured, json.RawMessage(tt.appState)); got != tt.want {
				t.Errorf("detectDaemon(%q, %s) = %q, want %q", tt.configured, tt.appState, got, tt.want)
			}
		})
	}
}

func TestUsableResponse(t *testing.T) {
	tests := []struct {
		name string
		resp map[string]any
		want bool
	}{
		{"peer present", map[string]any{"username": "peer-one"}, true},
		{"empty username", map[string]any{"username": ""}, false},
		{"whitespace username", map[string]any{"username": "  "}, false},
		{"missing username", map[string]any{"files": []any{}}, false},
		{"non-string username", map[string]any{"username": 42}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usableResponse(tt.resp); got != tt.want {
				t.Errorf("usableResponse(%v) = %v, want %v", tt.resp, got, tt.want)
			}
		})
	}
}

// TestProcessResponsesSkipsEmptyUsername verifies that responses without a peer
// identity (slskr local share-index hits) are dropped rather than surfacing
// undownloadable tracks.
func TestProcessResponsesSkipsEmptyUsername(t *testing.T) {
	responses := []map[string]any{
		{
			"username": "",
			"files": []any{
				map[string]any{"filename": "music/Artist/Album/01 - Track.flac", "size": float64(100)},
			},
		},
		{
			"username": "peer-one",
			"files": []any{
				map[string]any{"filename": "Artist/Album/02 - Song.flac", "size": float64(200)},
			},
		},
	}

	tracks, _ := processResponses(responses)
	for _, tr := range tracks {
		if tr.Username == "" {
			t.Errorf("track %q has empty username; username-less responses must be skipped", tr.Filename)
		}
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1 (only the peer response)", len(tracks))
	}
	if tracks[0].Username != "peer-one" {
		t.Errorf("track username = %q, want peer-one", tracks[0].Username)
	}
}

func TestFactoryValidateConfigDaemon(t *testing.T) {
	for _, daemon := range []string{"", daemonAuto, daemonSlskd, daemonSlskr} {
		raw := json.RawMessage(`{"slskd_url":"http://localhost:5030","api_key":"k","daemon":"` + daemon + `"}`)
		if err := Factory.ValidateConfig(raw); err != nil {
			t.Errorf("daemon %q should be valid: %v", daemon, err)
		}
	}
	if err := Factory.ValidateConfig(json.RawMessage(`{"daemon":"bogus"}`)); err == nil {
		t.Error("daemon \"bogus\" should be rejected")
	}
}

// TestSearchSkipsUnusableResponses drives the poll loop end to end: the first
// poll returns only a username-less (local) response, then a peer response
// arrives. The search must NOT treat the unusable-only page as "finished" (it
// must keep polling) and must return the peer's track.
func TestSearchSkipsUnusableResponses(t *testing.T) {
	const unusable = `[{"username":"","files":[{"filename":"music/Local/Album/01 - Local.flac","size":10}]}]`
	const mixed = `[{"username":"","files":[{"filename":"music/Local/Album/01 - Local.flac","size":10}]},` +
		`{"username":"peer-one","files":[{"filename":"Artist/Album/02 - Song.flac","size":20,"bitRate":900,"length":100}]}]`

	var polls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/searches":
			io.WriteString(w, `{"searchId":"s1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v0/searches/s1/responses":
			if atomic.AddInt32(&polls, 1) == 1 {
				io.WriteString(w, unusable)
				return
			}
			io.WriteString(w, mixed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(json.RawMessage(`{"slskd_url":"`+srv.URL+`","api_key":"k"}`), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	tracks, _, err := c.search(ctx, "query", 1, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1 (unusable responses must not end the search)", len(tracks))
	}
	if tracks[0].Username != "peer-one" {
		t.Errorf("track username = %q, want peer-one", tracks[0].Username)
	}
}

// TestClientDaemonNameFromConfig verifies New() normalizes the configured daemon
// and exposes it via DaemonName() before any connection is made.
func TestClientDaemonNameFromConfig(t *testing.T) {
	tests := []struct {
		cfg  string
		want string
	}{
		{`{"daemon":"slskr"}`, daemonSlskr},
		{`{"daemon":"slskd"}`, daemonSlskd},
		{`{"daemon":"auto"}`, daemonAuto},
		{`{}`, daemonAuto},
		{`{"daemon":"bogus"}`, daemonAuto},
	}
	for _, tt := range tests {
		c, err := New(json.RawMessage(tt.cfg), "", nil)
		if err != nil {
			t.Fatalf("New(%s): %v", tt.cfg, err)
		}
		if got := c.DaemonName(); got != tt.want {
			t.Errorf("New(%s) DaemonName() = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}
