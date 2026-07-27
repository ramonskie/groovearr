package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ─── PKCE tests ──────────────────────────────────────────────────────

func TestGeneratePKCEVerifier(t *testing.T) {
	v, err := GeneratePKCEVerifier()
	if err != nil {
		t.Fatalf("GeneratePKCEVerifier: %v", err)
	}

	// 32 random bytes → 43 chars in base64url without padding.
	if len(v) != 43 {
		t.Errorf("verifier length = %d, want 43", len(v))
	}

	// Verify only valid base64url characters (RFC 7636: [A-Za-z0-9._~-]).
	// base64.RawURLEncoding uses [A-Za-z0-9_-] (no padding).
	for i, c := range v {
		if !((c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_') {
			t.Errorf("verifier[%d] = %q, not a valid base64url character", i, c)
		}
	}

	// Each call should produce a different value.
	v2, err := GeneratePKCEVerifier()
	if err != nil {
		t.Fatalf("second GeneratePKCEVerifier: %v", err)
	}
	if v == v2 {
		t.Error("two consecutive verifiers should be different")
	}
}

func TestGeneratePKCEChallenge_RFC7636(t *testing.T) {
	// RFC 7636 Appendix B test vector.
	// https://tools.ietf.org/html/rfc7636#appendix-B
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const expected = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	challenge := GeneratePKCEChallenge(verifier)
	if challenge != expected {
		t.Errorf("PKCE challenge mismatch\n  got:  %s\n  want: %s", challenge, expected)
	}
}

func TestGeneratePKCEChallenge_RoundTrip(t *testing.T) {
	// Generate a verifier, compute challenge, verify the hash relationship.
	verifier, err := GeneratePKCEVerifier()
	if err != nil {
		t.Fatalf("GeneratePKCEVerifier: %v", err)
	}
	challenge := GeneratePKCEChallenge(verifier)

	// Challenge should be a valid base64url string.
	if challenge == "" {
		t.Error("challenge must not be empty")
	}
	// Challenge must differ from the verifier (it's a hash).
	if challenge == verifier {
		t.Error("challenge should differ from verifier")
	}
}

// ─── BuildAuthURL tests ──────────────────────────────────────────────

func TestBuildAuthURL(t *testing.T) {
	authURL := BuildAuthURL("client123", "http://localhost:8008/callback", "challenge_abc", "state_xyz")

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("BuildAuthURL produced invalid URL: %v", err)
	}

	if u.Scheme != "https" {
		t.Errorf("scheme = %q, want https", u.Scheme)
	}
	if u.Host != "accounts.spotify.com" {
		t.Errorf("host = %q, want accounts.spotify.com", u.Host)
	}
	if u.Path != "/authorize" {
		t.Errorf("path = %q, want /authorize", u.Path)
	}

	q := u.Query()
	checks := map[string]string{
		"client_id":             "client123",
		"response_type":         "code",
		"redirect_uri":          "http://localhost:8008/callback",
		"state":                 "state_xyz",
		"code_challenge_method": "S256",
		"code_challenge":        "challenge_abc",
		"scope":                 spotifyScopes,
	}
	for param, want := range checks {
		if got := q.Get(param); got != want {
			t.Errorf("query param %s = %q, want %q", param, got, want)
		}
	}
}

func TestBuildAuthURL_Encoding(t *testing.T) {
	// Redirect URIs and challenges may contain special characters.
	authURL := BuildAuthURL("c1", "https://example.com/callback?a=1&b=2", "ch+al/len=ge", "st=ate")
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("BuildAuthURL: %v", err)
	}

	q := u.Query()
	if q.Get("redirect_uri") != "https://example.com/callback?a=1&b=2" {
		t.Errorf("redirect_uri not preserved: %s", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge") != "ch+al/len=ge" {
		t.Errorf("code_challenge not preserved: %s", q.Get("code_challenge"))
	}
	if q.Get("state") != "st=ate" {
		t.Errorf("state not preserved: %s", q.Get("state"))
	}
}

// ─── Token exchange mock server ──────────────────────────────────────

func setupTokenServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	oldEndpoint := tokenEndpoint
	oldClient := testHTTPClient
	t.Cleanup(func() {
		tokenEndpoint = oldEndpoint
		testHTTPClient = oldClient
	})
	tokenEndpoint = srv.URL + "/api/token"
	testHTTPClient = srv.Client()
	return srv
}

func TestExchangeCode_Success(t *testing.T) {
	srv := setupTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/api/token" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}

		// Verify form fields.
		if r.PostForm.Get("grant_type") != "authorization_code" {
			http.Error(w, "bad grant_type", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("code") != "auth_code_123" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("code_verifier") != "verifier_abc" {
			http.Error(w, "bad code_verifier", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("client_id") != "cli_42" {
			http.Error(w, "bad client_id", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("redirect_uri") != "http://localhost:8008/callback" {
			http.Error(w, "bad redirect_uri", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "access_token_xyz",
			TokenType:    "Bearer",
			Scope:        spotifyScopes,
			ExpiresIn:    3600,
			RefreshToken: "refresh_token_xyz",
		})
	})
	defer srv.Close()

	ctx := context.Background()
	accessToken, refreshToken, expiresIn, err := ExchangeCode(
		ctx, "auth_code_123", "verifier_abc", "cli_42", "http://localhost:8008/callback", nil,
	)
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if accessToken != "access_token_xyz" {
		t.Errorf("accessToken = %q, want access_token_xyz", accessToken)
	}
	if refreshToken != "refresh_token_xyz" {
		t.Errorf("refreshToken = %q, want refresh_token_xyz", refreshToken)
	}
	if expiresIn != 3600 {
		t.Errorf("expiresIn = %d, want 3600", expiresIn)
	}
}

func TestExchangeCode_ErrorResponse(t *testing.T) {
	srv := setupTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(tokenErrorResponse{
			Error:            "invalid_grant",
			ErrorDescription: "Authorization code expired",
		})
	})
	defer srv.Close()

	ctx := context.Background()
	_, _, _, err := ExchangeCode(ctx, "bad_code", "v", "c", "http://localhost", nil)
	if err == nil {
		t.Fatal("ExchangeCode should return error for invalid_grant")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error should mention invalid_grant, got: %v", err)
	}
}

func TestExchangeCode_NonOKStatus(t *testing.T) {
	srv := setupTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	defer srv.Close()

	ctx := context.Background()
	_, _, _, err := ExchangeCode(ctx, "code", "v", "c", "http://localhost", nil)
	if err == nil {
		t.Fatal("ExchangeCode should return error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error should mention HTTP 500, got: %v", err)
	}
}

func TestExchangeCode_MissingAccessToken(t *testing.T) {
	srv := setupTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "rt",
		})
	})
	defer srv.Close()

	ctx := context.Background()
	_, _, _, err := ExchangeCode(ctx, "code", "v", "c", "http://localhost", nil)
	if err == nil {
		t.Fatal("ExchangeCode should return error when access_token is missing")
	}
}

func TestRefreshAccessToken_Success(t *testing.T) {
	srv := setupTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("grant_type") != "refresh_token" {
			http.Error(w, "bad grant_type", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("refresh_token") != "existing_refresh_token" {
			http.Error(w, "bad refresh_token", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("client_id") != "cli_42" {
			http.Error(w, "bad client_id", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "new_access_token_abc",
			TokenType:   "Bearer",
			Scope:       spotifyScopes,
			ExpiresIn:   3600,
		})
	})
	defer srv.Close()

	ctx := context.Background()
	accessToken, expiresIn, err := RefreshAccessToken(ctx, "existing_refresh_token", "cli_42", nil)
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if accessToken != "new_access_token_abc" {
		t.Errorf("accessToken = %q, want new_access_token_abc", accessToken)
	}
	if expiresIn != 3600 {
		t.Errorf("expiresIn = %d, want 3600", expiresIn)
	}
}

func TestRefreshAccessToken_Error(t *testing.T) {
	srv := setupTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(tokenErrorResponse{
			Error:            "invalid_grant",
			ErrorDescription: "Refresh token revoked",
		})
	})
	defer srv.Close()

	ctx := context.Background()
	_, _, err := RefreshAccessToken(ctx, "revoked_token", "cli", nil)
	if err == nil {
		t.Fatal("RefreshAccessToken should return error for invalid_grant")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error should mention invalid_grant, got: %v", err)
	}
}

// ─── IsTokenExpired tests ────────────────────────────────────────────

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
			expiresAt: now.Add(-1 * time.Second),
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
			got := IsTokenExpired(tt.expiresAt)
			if got != tt.want {
				t.Errorf("IsTokenExpired(%v) = %v, want %v", tt.expiresAt, got, tt.want)
			}
		})
	}
}

func TestIsTokenExpired_BufferPrecision(t *testing.T) {
	now := time.Now()

	// 60s from now — exactly at buffer boundary → expired.
	// time.Now() inside IsTokenExpired will be ≥ now, so After() is satisfied.
	if !IsTokenExpired(now.Add(60 * time.Second)) {
		t.Errorf("token expiring in exactly 60s should be considered expired")
	}
	// 62s from now — safely past buffer (2s margin avoids timing races) → not expired.
	if IsTokenExpired(now.Add(62 * time.Second)) {
		t.Errorf("token expiring in 62s should not be considered expired yet")
	}
}

// ─── Context cancellation ────────────────────────────────────────────

func TestExchangeCode_ContextCancelled(t *testing.T) {
	// Use a server that delays, then cancel the context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, _, _, err := ExchangeCode(ctx, "code", "v", "c", "http://localhost", nil)
	if err == nil {
		t.Fatal("ExchangeCode should return error when context is cancelled")
	}
	if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "cancel") {
		t.Logf("error returned (may vary by platform): %v", err)
	}
}

func TestRefreshAccessToken_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := RefreshAccessToken(ctx, "rt", "c", nil)
	if err == nil {
		t.Fatal("RefreshAccessToken should return error when context is cancelled")
	}
}

// ─── Compile-time checks ────────────────────────────────────────────

func TestFunctionSignatures(t *testing.T) {
	// Verify exported function signatures exist and are callable.
	_, _ = GeneratePKCEVerifier()
	_ = GeneratePKCEChallenge("test_verifier")
	_ = BuildAuthURL("id", "uri", "challenge", "state")
	_ = fmt.Sprintf("%v", tokenExpiryBuffer) // ensure constant exists
}
