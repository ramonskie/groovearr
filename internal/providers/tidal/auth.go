// auth.go — Tidal OAuth 2.0 token lifecycle (native, no external library).
//
// Adapted from the binozo Tidal client library v0.1.0 (Apache-2.0), auth.go:
// the refresh-token exchange is ported to a native implementation that
// avoids golang.org/x/oauth2.Token and the upstream library's types so the
// plugin owns its token lifecycle (refresh + expiry) end-to-end. The
// HTTP/JSON request shape mirrors internal/providers/spotify/oauth.go.
package tidal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Tidal OAuth 2.0 token endpoint.
const tokenEndpoint = "https://auth.tidal.com/v1/oauth2/token"

// testHTTPClient allows tests in the same package to inject an HTTP client
// that intercepts token requests. Nil means use http.DefaultClient.
// Mirrors internal/providers/spotify/oauth.go.
var testHTTPClient *http.Client

// tidalToken is the plugin's native OAuth 2.0 token representation.
// It deliberately avoids golang.org/x/oauth2.Token and the upstream library's
// types so the plugin controls token lifecycle (refresh + expiry) end-to-end.
type tidalToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// tokenResponse mirrors the Tidal /v1/oauth2/token JSON response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	UserID       int    `json:"user_id"`
}

// tokenErrorResponse mirrors the Tidal /v1/oauth2/token JSON error body.
type tokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// AuthRequest represents a pending device authorization request returned by
// the Tidal /v1/oauth2/device_authorization endpoint, mirroring its JSON.
// The user must visit VerificationURIComplete to approve the request before
// Expires. Expires is derived from ExpiresIn at decode time.
type AuthRequest struct {
	DeviceCode              string `json:"deviceCode"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	// Expires is derived from ExpiresIn at decode time (not part of the wire JSON).
	Expires time.Time `json:"-"`
}

// AuthResult represents the result of a successful device authorization,
// containing the OAuth2 token fields and user information, mirroring the
// /v1/oauth2/token JSON response.
type AuthResult struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	UserID       int    `json:"user_id"`
	ClientName   string `json:"clientName"`
}

// tokenExpiryBuffer is subtracted from expires_at to ensure tokens are
// refreshed before they actually expire.
const tokenExpiryBuffer = 60 * time.Second

// defaultTokenLifetime is the fallback access-token lifetime (seconds) used
// when a token response omits expires_in. Without it a zero expires_in would
// make the token look permanently expired and trigger a refresh on every call.
const defaultTokenLifetime = 3600

// ─── Token Refresh ────────────────────────────────────────────────────

// refreshAccessToken obtains a new access token using a refresh token.
// Returns the new access token, any rotated refresh token (empty when the
// server does not rotate), the lifetime in seconds, and any error. On
// invalid_grant, the caller should discard the refresh token and
// re-authorize.
func refreshAccessToken(ctx context.Context, refreshToken, clientID, clientSecret string, log *slog.Logger) (newAccessToken, newRefreshToken string, expiresIn int, err error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientID)

	accessToken, newRefresh, expiresIn, err := postToken(ctx, tokenClient(), data, clientID, clientSecret, log)
	if err != nil {
		return "", "", 0, err
	}
	if expiresIn <= 0 {
		expiresIn = defaultTokenLifetime
	}
	return accessToken, newRefresh, expiresIn, nil
}

// tokenClient returns the HTTP client for token requests.
// Tests set testHTTPClient to intercept requests; production uses DefaultClient.
func tokenClient() *http.Client {
	if testHTTPClient != nil {
		return testHTTPClient
	}
	return http.DefaultClient
}

// postToken sends a form-encoded POST to the Tidal token endpoint using
// HTTP Basic auth and parses the JSON response.
func postToken(ctx context.Context, client *http.Client, data url.Values, clientID, clientSecret string, log *slog.Logger) (accessToken, refreshToken string, expiresIn int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		if log != nil {
			log.Error("tidal create token request failed", "error", err, "component", "tidal_oauth")
		}
		return "", "", 0, fmt.Errorf("tidal: create token request: %w", err)
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		if log != nil {
			log.Error("tidal token request failed", "error", err, "component", "tidal_oauth")
		}
		return "", "", 0, fmt.Errorf("tidal: token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if log != nil {
			log.Error("tidal read token response failed", "error", err, "component", "tidal_oauth")
		}
		return "", "", 0, fmt.Errorf("tidal: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var terr tokenErrorResponse
		if json.Unmarshal(body, &terr) == nil && terr.Error != "" {
			return "", "", 0, fmt.Errorf("tidal: token endpoint error (%s): %s", terr.Error, terr.ErrorDescription)
		}
		return "", "", 0, fmt.Errorf("tidal: token endpoint returned HTTP %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", "", 0, fmt.Errorf("tidal: parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", "", 0, fmt.Errorf("tidal: token response missing access_token")
	}

	return tr.AccessToken, tr.RefreshToken, tr.ExpiresIn, nil
}

// ─── Token Validation ─────────────────────────────────────────────────

// IsTokenExpired reports whether the access token is expired or about to
// expire. Uses a 60-second buffer to ensure the token is refreshed before
// it actually expires. A zero-value expiresAt is always considered expired.
func IsTokenExpired(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return true
	}
	return time.Now().After(expiresAt.Add(-tokenExpiryBuffer))
}

// expiryFromConfig converts a persisted config expires_at value (unix
// seconds, 0 = unknown) into a token expiry. Zero or negative values map to
// the zero time, which IsTokenExpired treats as expired so the first request
// refreshes via the stored refresh token.
func expiryFromConfig(unixSeconds int64) time.Time {
	if unixSeconds <= 0 {
		return time.Time{}
	}
	return time.Unix(unixSeconds, 0)
}
