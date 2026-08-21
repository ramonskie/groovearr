package tidal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── Token endpoint test client ───────────────────────────────────────

// tokenRoundTripper runs an httptest handler in-process for any request,
// preserving the original request (method, path, headers, body) without
// performing a real network dial. Used because the Tidal tokenEndpoint is a
// constant, so the request URL cannot be pointed at a local server.
type tokenRoundTripper struct {
	handler http.HandlerFunc
}

func (rt tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.handler(rec, req)
	return rec.Result(), nil
}

// setupTokenClient injects a test HTTP client that intercepts token requests
// through the package-level testHTTPClient injection point (mirrors
// internal/providers/spotify/oauth_test.go). The handler sees the real
// request shape: POST /v1/oauth2/token with form body, Basic auth, and
// application/x-www-form-urlencoded content type.
func setupTokenClient(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	oldClient := testHTTPClient
	t.Cleanup(func() {
		testHTTPClient = oldClient
	})
	testHTTPClient = &http.Client{Transport: tokenRoundTripper{handler: handler}}
}

// ─── refreshAccessToken: success ───────────────────────────────────────

func TestRefreshAccessToken_Success(t *testing.T) {
	const (
		clientID     = "client-abc"
		clientSecret = "secret-xyz"
		refreshToken = "existing_refresh_token"
	)
	setupTokenClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify request shape: method, endpoint, content-type, Basic auth, form.
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/oauth2/token" {
			t.Errorf("path = %q, want /v1/oauth2/token", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", got)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != clientID || pass != clientSecret {
			t.Errorf("Basic auth = (%q, %q, %v), want (%q, %q, true)", user, pass, ok, clientID, clientSecret)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.PostForm.Get("refresh_token"); got != refreshToken {
			t.Errorf("refresh_token = %q, want %q", got, refreshToken)
		}
		if got := r.PostForm.Get("client_id"); got != clientID {
			t.Errorf("client_id = %q, want %q", got, clientID)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "new_access_token_abc",
			TokenType:    "Bearer",
			Scope:        "r_usr w_usr w_sub",
			ExpiresIn:    3600,
			RefreshToken: "rotated_refresh_token_xyz",
		})
	})

	ctx := context.Background()
	accessToken, newRefreshToken, expiresIn, err := refreshAccessToken(ctx, refreshToken, clientID, clientSecret, nil)
	if err != nil {
		t.Fatalf("refreshAccessToken: %v", err)
	}
	if accessToken != "new_access_token_abc" {
		t.Errorf("accessToken = %q, want new_access_token_abc", accessToken)
	}
	if newRefreshToken != "rotated_refresh_token_xyz" {
		t.Errorf("newRefreshToken = %q, want rotated_refresh_token_xyz", newRefreshToken)
	}
	if expiresIn != 3600 {
		t.Errorf("expiresIn = %d, want 3600", expiresIn)
	}
}

// ─── refreshAccessToken: errors ────────────────────────────────────────

func TestRefreshAccessToken_InvalidGrant(t *testing.T) {
	setupTokenClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Request body/headers must still be sent correctly even when the
		// grant fails — the caller needs a descriptive error to re-authorize.
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.PostForm.Get("refresh_token"); got != "revoked_token" {
			t.Errorf("refresh_token = %q, want revoked_token", got)
		}
		if _, _, ok := r.BasicAuth(); !ok {
			t.Error("missing Basic auth header")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(tokenErrorResponse{
			Error:            "invalid_grant",
			ErrorDescription: "Refresh token revoked",
		})
	})

	ctx := context.Background()
	_, _, _, err := refreshAccessToken(ctx, "revoked_token", "client-abc", "secret-xyz", nil)
	if err == nil {
		t.Fatal("refreshAccessToken should return error for invalid_grant")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error should mention invalid_grant, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Refresh token revoked") {
		t.Errorf("error should include error_description, got: %v", err)
	}
}

func TestRefreshAccessToken_NonJSONError(t *testing.T) {
	setupTokenClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})

	ctx := context.Background()
	_, _, _, err := refreshAccessToken(ctx, "rt", "client", "secret", nil)
	if err == nil {
		t.Fatal("refreshAccessToken should return error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error should mention HTTP 500, got: %v", err)
	}
}

func TestRefreshAccessToken_MissingAccessToken(t *testing.T) {
	setupTokenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "rt",
		})
	})

	ctx := context.Background()
	_, _, _, err := refreshAccessToken(ctx, "rt", "client", "secret", nil)
	if err == nil {
		t.Fatal("refreshAccessToken should return error when access_token is missing")
	}
	if !strings.Contains(err.Error(), "access_token") {
		t.Errorf("error should mention access_token, got: %v", err)
	}
}

func TestRefreshAccessToken_NetworkError(t *testing.T) {
	// Transport-level failure (connection refused, timeout) must surface as a
	// descriptive error rather than being silently swallowed.
	oldClient := testHTTPClient
	t.Cleanup(func() {
		testHTTPClient = oldClient
	})
	testHTTPClient = &http.Client{Transport: errorRoundTripper{err: errors.New("connection refused")}}

	ctx := context.Background()
	_, _, _, err := refreshAccessToken(ctx, "rt", "client", "secret", nil)
	if err == nil {
		t.Fatal("refreshAccessToken should return error when the token request fails")
	}
	if !strings.Contains(err.Error(), "token request failed") {
		t.Errorf("error should mention token request failure, got: %v", err)
	}
}

// errorRoundTripper always fails with the configured error.
type errorRoundTripper struct {
	err error
}

func (rt errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, rt.err
}

// ─── IsTokenExpired ────────────────────────────────────────────────────

func TestIsTokenExpired(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "zero value is expired",
			expiresAt: time.Time{},
			want:      true,
		},
		{
			name:      "just expired",
			expiresAt: now.Add(-time.Second),
			want:      true,
		},
		{
			name:      "inside buffer window (30s from now)",
			expiresAt: now.Add(30 * time.Second),
			want:      true,
		},
		{
			name:      "exactly on buffer boundary (60s from now)",
			expiresAt: now.Add(60 * time.Second),
			want:      true,
		},
		{
			name:      "just past buffer (61s from now)",
			expiresAt: now.Add(61 * time.Second),
			want:      false,
		},
		{
			name:      "far future",
			expiresAt: now.Add(24 * time.Hour),
			want:      false,
		},
		{
			name:      "distant past",
			expiresAt: now.Add(-24 * time.Hour),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTokenExpired(tt.expiresAt); got != tt.want {
				t.Errorf("IsTokenExpired(%v) = %v, want %v", tt.expiresAt, got, tt.want)
			}
		})
	}
}

// ─── tidalToken JSON persistence ───────────────────────────────────────

func TestTidalTokenJSONRoundTrip(t *testing.T) {
	expiresAt := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	orig := tidalToken{
		AccessToken:  "access_token_abc",
		RefreshToken: "refresh_token_xyz",
		ExpiresAt:    expiresAt,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal tidalToken: %v", err)
	}

	// JSON keys match the config persistence contract.
	raw := string(data)
	for _, key := range []string{"access_token", "refresh_token", "expires_at"} {
		if !strings.Contains(raw, `"`+key+`"`) {
			t.Errorf("marshaled JSON missing key %q: %s", key, raw)
		}
	}

	var got tidalToken
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal tidalToken: %v", err)
	}
	if got.AccessToken != orig.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, orig.AccessToken)
	}
	if got.RefreshToken != orig.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, orig.RefreshToken)
	}
	if !got.ExpiresAt.Equal(orig.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, orig.ExpiresAt)
	}
}

// ─── Expiry derived from expires_in ────────────────────────────────────

func TestTokenExpiryFromExpiresIn(t *testing.T) {
	// Mirrors how the plugin persists a refresh result: expiresAt is derived
	// from expires_in seconds, then IsTokenExpired drives refresh decisions.
	setupTokenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "new_access_token_abc",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "rotated_refresh_token_xyz",
		})
	})

	ctx := context.Background()
	_, _, expiresIn, err := refreshAccessToken(ctx, "existing_refresh_token", "client-abc", "secret-xyz", nil)
	if err != nil {
		t.Fatalf("refreshAccessToken: %v", err)
	}

	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if IsTokenExpired(expiresAt) {
		t.Errorf("token with expiresIn=%d should not be considered expired", expiresIn)
	}
}

// ─── expires_in fallback ────────────────────────────────────────────────

func TestRefreshAccessToken_ZeroExpiresInFallsBackToDefault(t *testing.T) {
	// A refresh response that omits expires_in must not make the token look
	// permanently expired (which would refresh on every operation).
	setupTokenClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "new_access_token_abc",
			TokenType:   "Bearer",
			ExpiresIn:   0,
		})
	})

	ctx := context.Background()
	_, _, expiresIn, err := refreshAccessToken(ctx, "existing_refresh_token", "client-abc", "secret-xyz", nil)
	if err != nil {
		t.Fatalf("refreshAccessToken: %v", err)
	}
	if expiresIn != defaultTokenLifetime {
		t.Errorf("expiresIn = %d, want default %d", expiresIn, defaultTokenLifetime)
	}
}
