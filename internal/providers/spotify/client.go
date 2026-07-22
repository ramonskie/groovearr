package spotify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Base URL constants for the Spotify Web API and Accounts service.
const (
	SpotifyWebAPI   = "https://api.spotify.com/v1"
	SpotifyAccounts = "https://accounts.spotify.com"
)

const (
	max429Retries  = 3
	userAgent      = "groovearr/1.0"
	defaultTimeout = 30 * time.Second
)

// SpotifyClient wraps an http.Client with Spotify authentication and rate-limit handling.
type SpotifyClient struct {
	http *http.Client
	cfg  *SpotifyConfig
	log  *slog.Logger
}

// NewClient creates a SpotifyClient that automatically injects auth headers
// and handles token refresh and rate limiting via a custom RoundTripper.
func NewClient(cfg *SpotifyConfig, log *slog.Logger) *SpotifyClient {
	return &SpotifyClient{
		cfg: cfg,
		log: log,
		http: &http.Client{
			Transport: &authTransport{
				cfg:         cfg,
				transport:   http.DefaultTransport,
				refreshFunc: RefreshAccessToken,
				log:         log,
			},
			Timeout: defaultTimeout,
		},
	}
}

// Do sends an HTTP request through the auth transport and returns the response.
func (c *SpotifyClient) Do(req *http.Request) (*http.Response, error) {
	return c.http.Do(req)
}

// ─── authTransport ────────────────────────────────────────────────────

// authTransport is an http.RoundTripper that injects Spotify authentication
// headers, refreshes tokens on 401, and retries on 429 rate limits.
type authTransport struct {
	cfg         *SpotifyConfig
	transport   http.RoundTripper
	log         *slog.Logger
	mu          sync.Mutex
	refreshFunc func(ctx context.Context, refreshToken, clientID string, log *slog.Logger) (newAccessToken string, expiresIn int, err error)
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = cloneRequest(req)
	t.setHeaders(req)

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		if t.log != nil {
			t.log.Error("spotify transport roundtrip failed", "error", err, "component", "spotify_client")
		}
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return t.handleUnauthorized(resp, req)
	case http.StatusTooManyRequests:
		return t.handleRateLimit(resp, req)
	}

	return resp, nil
}

// setHeaders injects the Authorization and User-Agent headers.
func (t *authTransport) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)

	token := t.getAccessToken()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// getAccessToken returns the current access token, or "" in free mode.
func (t *authTransport) getAccessToken() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cfg.Mode == "dev" && t.cfg.Tokens.AccessToken != "" {
		return t.cfg.Tokens.AccessToken
	}
	return ""
}

// setAccessToken updates the config with a new access token.
func (t *authTransport) setAccessToken(token string, expiresIn int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cfg.Tokens.AccessToken = token
	t.cfg.Tokens.ExpiresAt = time.Now().Unix() + int64(expiresIn)
}

// handleUnauthorized attempts to refresh the access token and retries the
// request once with the new token. Returns an error if refresh fails.
func (t *authTransport) handleUnauthorized(resp *http.Response, originalReq *http.Request) (*http.Response, error) {
	resp.Body.Close()

	t.mu.Lock()
	refreshToken := t.cfg.Tokens.RefreshToken
	clientID := t.cfg.ClientID
	t.mu.Unlock()

	if refreshToken == "" || clientID == "" {
		return nil, fmt.Errorf("spotify: unauthorized and no refresh token available")
	}

	newToken, expiresIn, err := t.refreshFunc(originalReq.Context(), refreshToken, clientID, t.log)
	if err != nil {
		if t.log != nil {
			t.log.Error("spotify token refresh failed", "error", err, "component", "spotify_client")
		}
		return nil, fmt.Errorf("spotify: token refresh failed: %w", err)
	}

	t.setAccessToken(newToken, expiresIn)

	retryReq := cloneRequest(originalReq)
	t.setHeaders(retryReq)
	return t.transport.RoundTrip(retryReq)
}

// handleRateLimit retries the request up to max429Retries times, sleeping
// according to the Retry-After header before each retry.
func (t *authTransport) handleRateLimit(resp *http.Response, originalReq *http.Request) (*http.Response, error) {
	for retries := 0; retries < max429Retries; retries++ {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		resp.Body.Close()

		time.Sleep(retryAfter)

		retryReq := cloneRequest(originalReq)
		t.setHeaders(retryReq)

		var err error
		resp, err = t.transport.RoundTrip(retryReq)
		if err != nil {
			if t.log != nil {
				t.log.Error("spotify rate limit retry failed", "error", err, "component", "spotify_client")
			}
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
	}

	resp.Body.Close()
	return nil, fmt.Errorf("spotify: rate limit exceeded after %d retries", max429Retries)
}

// ─── Helpers ───────────────────────────────────────────────────────────

// cloneRequest creates a shallow copy of an HTTP request, preserving headers
// and context. The body is not cloned — callers must ensure GetBody is set
// for requests that carry a body and require retries.
func cloneRequest(req *http.Request) *http.Request {
	return req.Clone(req.Context())
}

// parseRetryAfter parses the Retry-After header value. Accepts either
// an integer representing seconds or an HTTP-date. Defaults to 1 second.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return time.Second
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return time.Second
}
