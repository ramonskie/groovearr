package spotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/metadata"
)

// testClientWithRefresh builds a SpotifyClient whose authTransport uses the
// given refreshFunc instead of the real RefreshAccessToken.
func testClientWithRefresh(cfg *SpotifyConfig, refreshFunc func(ctx context.Context, refreshToken, clientID string, log *slog.Logger) (string, int, error)) *SpotifyClient {
	log := slog.New(slog.DiscardHandler)
	return &SpotifyClient{
		cfg: cfg,
		log: log,
		http: &http.Client{
			Transport: &authTransport{
				cfg:         cfg,
				transport:   http.DefaultTransport,
				refreshFunc: refreshFunc,
				log:         log,
			},
			Timeout: defaultTimeout,
		},
	}
}

// ─── Bearer token injection ──────────────────────────────────────────

func TestClientBearerTokenInjected(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{
		Mode: "dev",
		Tokens: SpotifyTokens{
			AccessToken: "tokendev123",
		},
	}
	client := NewClient(cfg, slog.New(slog.DiscardHandler))

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if capturedAuth != "Bearer tokendev123" {
		t.Errorf("Authorization = %q, want %q", capturedAuth, "Bearer tokendev123")
	}
}

// ─── Free mode — no auth header ──────────────────────────────────────

func TestClientNoAuthHeaderFreeMode(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{
		Mode: "free",
	}
	client := NewClient(cfg, slog.New(slog.DiscardHandler))

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if capturedAuth != "" {
		t.Errorf("Authorization = %q, want empty (free mode)", capturedAuth)
	}
}

// ─── 401 → refresh → retry success ──────────────────────────────────

func TestClient401RefreshRetrySuccess(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			if r.Header.Get("Authorization") != "Bearer expired-token" {
				t.Errorf("first request auth = %q, want Bearer expired-token", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fresh-token" {
			t.Errorf("retry request auth = %q, want Bearer fresh-token", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{
		Mode:     "dev",
		ClientID: "client-id-1",
		Tokens: SpotifyTokens{
			AccessToken:  "expired-token",
			RefreshToken: "refresh-token-1",
		},
	}

	client := testClientWithRefresh(cfg, func(ctx context.Context, refreshToken, clientID string, _ *slog.Logger) (string, int, error) {
		if refreshToken != "refresh-token-1" {
			t.Errorf("refresh called with token %q, want refresh-token-1", refreshToken)
		}
		if clientID != "client-id-1" {
			t.Errorf("refresh called with clientID %q, want client-id-1", clientID)
		}
		return "fresh-token", 3600, nil
	})

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if callCount != 2 {
		t.Errorf("call count = %d, want 2", callCount)
	}
	if cfg.Tokens.AccessToken != "fresh-token" {
		t.Errorf("config token not updated: got %q, want fresh-token", cfg.Tokens.AccessToken)
	}
	if cfg.Tokens.ExpiresAt == 0 {
		t.Error("config expires_at was not updated")
	}
}

// ─── 401 → refresh fails → error ────────────────────────────────────

func TestClient401RefreshFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{
		Mode:     "dev",
		ClientID: "client-id-1",
		Tokens: SpotifyTokens{
			AccessToken:  "expired-token",
			RefreshToken: "refresh-token-1",
		},
	}

	refreshErr := errors.New("spotify: invalid_grant")
	client := testClientWithRefresh(cfg, func(ctx context.Context, refreshToken, clientID string, _ *slog.Logger) (string, int, error) {
		return "", 0, refreshErr
	})

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ─── 429 → Retry-After → retry success ───────────────────────────────

func TestClient429RetryAfterSuccess(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{Mode: "free"}
	client := NewClient(cfg, slog.New(slog.DiscardHandler))

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if callCount != 2 {
		t.Errorf("call count = %d, want 2", callCount)
	}
}

// ─── 429 → exceeds max retries → error ──────────────────────────────

func TestClient429ExceedsMaxRetries(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{Mode: "free"}
	client := NewClient(cfg, slog.New(slog.DiscardHandler))

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error after max retries, got nil")
	}

	// max429Retries = 3 → 1 initial + 3 retries = 4 calls total
	if callCount != 4 {
		t.Errorf("call count = %d, want 4 (1 initial + 3 retries)", callCount)
	}
}

// ─── 429 retry aborts when the request context is cancelled ──────────

// TestClient429RetryRespectsContext is the regression test for the enrichment
// job convoy stall: handleRateLimit used a bare time.Sleep(retryAfter), which
// never observes the request context, so a 429 with a long Retry-After pinned
// a job worker past its per-track deadline with no way to abort. The wait must
// be context-aware: cancelling the context mid-sleep returns context.Canceled
// promptly instead of sleeping to completion.
func TestClient429RetryRespectsContext(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Retry-After within the transient window (≤ maxTransientRetryAfter) so
		// the request actually enters the retry sleep; cancelling must abort it.
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{Mode: "free"}
	client := NewClient(cfg, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)

	done := make(chan error, 1)
	go func() {
		_, err := client.Do(req)
		done <- err
	}()

	// Wait until the request reaches the server (the 429 arrives and the retry
	// sleep starts), then cancel. Polling instead of a fixed sleep avoids a
	// scheduling-delay flake where cancel fires before the goroutine issues the
	// request — the test would then assert calls==0, not the real behavior.
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("request never reached server")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give the handler a moment to finish recording the call before cancelling.
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client.Do did not return after context cancellation (retry sleep ignored ctx)")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancellation took %v, want prompt return", elapsed)
	}
	// Only the initial 429 request should have been made — no retry after cancel.
	if got := calls.Load(); got != 1 {
		t.Errorf("server calls = %d, want 1 (no retry after cancel)", got)
	}
}

// ─── 429 with long Retry-After fails fast as ErrRateLimited ─────────

// TestClient429LongRetryAfterFailsFast covers the fail-fast design: a 429 with
// a Retry-After beyond the transient window must return metadata.ErrRateLimited
// immediately instead of sleeping for minutes, so the enrichment job can mark
// the provider cooling down and move to the next one.
func TestClient429LongRetryAfterFailsFast(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{Mode: "free"}
	client := NewClient(cfg, slog.New(slog.DiscardHandler))

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	start := time.Now()
	_, err := client.Do(req)
	elapsed := time.Since(start)

	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v, want metadata.ErrRateLimited", err)
	}
	var rl *metadata.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter < time.Hour {
		t.Errorf("error should carry RetryAfter ≈ 1h via RateLimitError, got %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("fail-fast took %v, want immediate", elapsed)
	}
	// No retries for a long backoff.
	if got := calls.Load(); got != 1 {
		t.Errorf("server calls = %d, want 1 (no retry for long Retry-After)", got)
	}
}

// ─── Standard 200 pass-through ───────────────────────────────────────

func TestClient200PassThrough(t *testing.T) {
	var capturedUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.Header().Set("X-Custom", "yes")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	}))
	defer server.Close()

	cfg := &SpotifyConfig{Mode: "free"}
	client := NewClient(cfg, slog.New(slog.DiscardHandler))

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if capturedUA != userAgent {
		t.Errorf("User-Agent = %q, want %q", capturedUA, userAgent)
	}
	if resp.Header.Get("X-Custom") != "yes" {
		t.Error("custom header not preserved")
	}
}

// ─── User-Agent is set on all requests ──────────────────────────────

func TestClientUserAgentSet(t *testing.T) {
	// Verify dev mode also sets User-Agent alongside auth header.
	var capturedUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &SpotifyConfig{
		Mode: "dev",
		Tokens: SpotifyTokens{
			AccessToken: "tok",
		},
	}
	client := NewClient(cfg, slog.New(slog.DiscardHandler))

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if capturedUA != userAgent {
		t.Errorf("User-Agent = %q, want %q", capturedUA, userAgent)
	}
}

// ─── Parse Retry-After ──────────────────────────────────────────────

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // use string comparison to avoid floating issues
	}{
		{"empty", "", "1s"},
		{"seconds integer", "5", "5s"},
		{"seconds zero", "0", "0s"},
		{"bad value falls back", "xyz", "1s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.input)
			if got.String() != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %s", tt.input, got, tt.want)
			}
		})
	}
}
