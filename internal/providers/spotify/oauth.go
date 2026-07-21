package spotify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Spotify OAuth 2.0 PKCE endpoints.
var (
	authEndpoint  = "https://accounts.spotify.com/authorize"
	tokenEndpoint = "https://accounts.spotify.com/api/token"

	// testHTTPClient allows tests in the same package to inject an HTTP client
	// that intercepts token requests. Nil means use http.DefaultClient.
	testHTTPClient *http.Client
)

// OAuth scopes required by the Spotify plugin.
const spotifyScopes = "user-read-private user-read-email playlist-read-private playlist-read-collaborative"

// tokenResponse mirrors the Spotify /api/token JSON response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// tokenErrorResponse mirrors the Spotify /api/token JSON error body.
type tokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// tokenExpiryBuffer is subtracted from expires_at to ensure tokens are
// refreshed before they actually expire.
const tokenExpiryBuffer = 60 * time.Second

// ─── PKCE ────────────────────────────────────────────────────────────

// GeneratePKCEVerifier creates a cryptographically random code verifier.
// Returns a 43-character URL-safe base64 string (without padding) per RFC 7636.
func GeneratePKCEVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("spotify: generate pkce verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GeneratePKCEChallenge computes the S256 code challenge from a verifier.
// Returns the SHA-256 hash of verifier, base64url-encoded without padding.
func GeneratePKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ─── Authorization URL ───────────────────────────────────────────────

// BuildAuthURL constructs the Spotify authorization URL for the PKCE flow.
// The caller is responsible for generating and storing state for CSRF protection.
func BuildAuthURL(clientID, redirectURI, challenge, state string) string {
	u, err := url.Parse(authEndpoint)
	if err != nil {
		// authEndpoint is a hard-coded constant — should never fail.
		panic("spotify: invalid authEndpoint constant: " + err.Error())
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("scope", spotifyScopes)
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", challenge)
	u.RawQuery = q.Encode()
	return u.String()
}

// ─── Token Exchange ──────────────────────────────────────────────────

// ExchangeCode exchanges an OAuth authorization code for access and refresh tokens.
// Returns accessToken, refreshToken, expiresIn (seconds), and any error.
func ExchangeCode(ctx context.Context, code, verifier, clientID, redirectURI string) (accessToken, refreshToken string, expiresIn int, err error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", clientID)
	data.Set("code_verifier", verifier)

	return postToken(ctx, tokenClient(), data)
}

// RefreshAccessToken obtains a new access token using a refresh token.
// Returns newAccessToken, expiresIn (seconds), and any error.
// On invalid_grant, the caller should discard the refresh token and re-authorize.
func RefreshAccessToken(ctx context.Context, refreshToken, clientID string) (newAccessToken string, expiresIn int, err error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientID)

	accessToken, _, expiresIn, err := postToken(ctx, tokenClient(), data)
	return accessToken, expiresIn, err
}

// tokenClient returns the HTTP client for token requests.
// Tests set testHTTPClient to intercept requests; production uses DefaultClient.
func tokenClient() *http.Client {
	if testHTTPClient != nil {
		return testHTTPClient
	}
	return http.DefaultClient
}

// postToken sends a form-encoded POST to the Spotify token endpoint and
// parses the JSON response.
func postToken(ctx context.Context, client *http.Client, data url.Values) (accessToken, refreshToken string, expiresIn int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", "", 0, fmt.Errorf("spotify: create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("spotify: token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", 0, fmt.Errorf("spotify: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var terr tokenErrorResponse
		if json.Unmarshal(body, &terr) == nil && terr.Error != "" {
			return "", "", 0, fmt.Errorf("spotify: token endpoint error (%s): %s", terr.Error, terr.ErrorDescription)
		}
		return "", "", 0, fmt.Errorf("spotify: token endpoint returned HTTP %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", "", 0, fmt.Errorf("spotify: parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", "", 0, fmt.Errorf("spotify: token response missing access_token")
	}

	return tr.AccessToken, tr.RefreshToken, tr.ExpiresIn, nil
}

// ─── Token Validation ────────────────────────────────────────────────

// IsTokenExpired reports whether the access token is expired or about to expire.
// Uses a 60-second buffer to ensure the token is refreshed before it actually expires.
// A zero-value expiresAt is always considered expired.
func IsTokenExpired(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return true
	}
	return time.Now().After(expiresAt.Add(-tokenExpiryBuffer))
}
