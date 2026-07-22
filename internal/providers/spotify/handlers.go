package spotify

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
)

// oauthStates stores in-progress OAuth state parameters.
// Avoids cookie SameSite/redirect issues across browsers.
var (
	oauthStates   = make(map[string]string) // state → verifier
	oauthStatesMu sync.Mutex
)

func init() {
	// Clean expired states periodically.
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			oauthStatesMu.Lock()
			for k := range oauthStates {
				delete(oauthStates, k)
			}
			oauthStatesMu.Unlock()
		}
	}()
}

// RegisterOAuthRoutes adds Spotify OAuth login and callback endpoints to the given mux.
// The rebuild callback is invoked after tokens are stored to recreate the spotify plugin
// so it picks up the new access token. The verify callback is called after rebuild to
// run CheckConnection so the UI reflects the new state.
func RegisterOAuthRoutes(mux *http.ServeMux, cfg *config.Persistence, logger *slog.Logger, rebuild func(name string, rawCfg json.RawMessage) error, verify func(name string)) {
	mux.HandleFunc("GET /api/spotify/login", handleSpotifyLogin(cfg))
	mux.HandleFunc("GET /api/spotify/callback", handleSpotifyCallback(cfg, logger, rebuild, verify))
}

// handleSpotifyLogin initiates the OAuth PKCE flow by redirecting the user
// to the Spotify authorization page.
func handleSpotifyLogin(cfg *config.Persistence) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current := cfg.Get()
		raw, ok := current.Sources["spotify"]
		if !ok {
			http.Error(w, "spotify not configured", http.StatusBadRequest)
			return
		}

		var spCfg struct {
			ClientID    string `json:"client_id"`
			RedirectURI string `json:"redirect_uri"`
		}
		if err := json.Unmarshal(raw, &spCfg); err != nil {
			http.Error(w, "invalid spotify config", http.StatusInternalServerError)
			return
		}
		if spCfg.ClientID == "" || spCfg.RedirectURI == "" {
			http.Error(w, "spotify: client_id and redirect_uri required in dev mode", http.StatusBadRequest)
			return
		}

		verifier, err := GeneratePKCEVerifier()
		if err != nil {
			http.Error(w, "failed to generate verifier", http.StatusInternalServerError)
			return
		}
		challenge := GeneratePKCEChallenge(verifier)

		// The verifier value is also used as the OAuth state parameter for CSRF protection.
		state := verifier

		// Store state in-memory (avoids SameSite cookie issues on redirect).
		oauthStatesMu.Lock()
		oauthStates[state] = verifier
		oauthStatesMu.Unlock()

		authURL := BuildAuthURL(spCfg.ClientID, spCfg.RedirectURI, challenge, state)
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// handleSpotifyCallback handles the OAuth callback from Spotify. It validates
// the state parameter, exchanges the authorization code for tokens, and stores
// them in the config.
func handleSpotifyCallback(cfg *config.Persistence, logger *slog.Logger, rebuild func(name string, rawCfg json.RawMessage) error, verify func(name string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errorParam := r.URL.Query().Get("error")

		if errorParam != "" {
			http.Error(w, "spotify auth error: "+errorParam, http.StatusBadRequest)
			return
		}
		if code == "" || state == "" {
			http.Error(w, "missing code or state parameter", http.StatusBadRequest)
			return
		}

		// Validate state against the in-memory store.
		oauthStatesMu.Lock()
		verifier, ok := oauthStates[state]
		if ok {
			delete(oauthStates, state)
		}
		oauthStatesMu.Unlock()
		if !ok {
			logger.Warn("CSRF check: unknown state", "state", state, "component", "spotify")
			http.Error(w, "invalid state — CSRF check failed", http.StatusForbidden)
			return
		}

		current := cfg.Get()
		raw, ok := current.Sources["spotify"]
		if !ok {
			http.Error(w, "spotify not configured", http.StatusBadRequest)
			return
		}

		var spCfg SpotifyConfig
		if err := json.Unmarshal(raw, &spCfg); err != nil {
			http.Error(w, "invalid spotify config", http.StatusInternalServerError)
			return
		}

		accessToken, refreshToken, expiresIn, err := ExchangeCode(
			r.Context(), code, verifier, spCfg.ClientID, spCfg.RedirectURI, logger,
		)
		if err != nil {
			logger.Error("token exchange failed", "error", err, "component", "spotify")
			http.Error(w, "token exchange failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Store tokens in config.
		spCfg.Tokens = SpotifyTokens{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			ExpiresAt:    time.Now().Unix() + int64(expiresIn),
		}

		if err := cfg.Update(func(c *config.Config) error {
			b, err := json.Marshal(spCfg)
			if err != nil {
				logger.Error("spotify config marshal failed", "error", err, "component", "spotify_handler")
				return err
			}
			if c.Sources == nil {
				c.Sources = make(map[string]json.RawMessage)
			}
			c.Sources["spotify"] = b
			return nil
		}); err != nil {
			logger.Error("failed to save tokens", "error", err, "component", "spotify")
			http.Error(w, "failed to save tokens", http.StatusInternalServerError)
			return
		}

		// Rebuild the plugin so it picks up the new tokens.
		updated := cfg.Get()
		if raw, ok := updated.Sources["spotify"]; ok {
			if err := rebuild("spotify", raw); err != nil {
				logger.Error("plugin rebuild after OAuth failed", "error", err, "component", "spotify")
			} else if verify != nil {
				verify("spotify")
			}
		}

		// Redirect back to settings.
		http.Redirect(w, r, "/settings?spotify=connected", http.StatusFound)
	}
}
