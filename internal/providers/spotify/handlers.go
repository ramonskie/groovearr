package spotify

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
)

// RegisterOAuthRoutes adds Spotify OAuth login and callback endpoints to the given mux.
// The rebuild callback is invoked after tokens are stored to recreate the spotify plugin
// so it picks up the new access token and becomes "Connected".
func RegisterOAuthRoutes(mux *http.ServeMux, cfg *config.Persistence, rebuild func(name string, rawCfg json.RawMessage) error) {
	mux.HandleFunc("GET /api/spotify/login", handleSpotifyLogin(cfg))
	mux.HandleFunc("GET /api/spotify/callback", handleSpotifyCallback(cfg, rebuild))
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

		// Store the verifier in a cookie for callback validation.
		http.SetCookie(w, &http.Cookie{
			Name:     "spotify_oauth_state",
			Value:    verifier,
			Path:     "/api/spotify",
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		authURL := BuildAuthURL(spCfg.ClientID, spCfg.RedirectURI, challenge, state)
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// handleSpotifyCallback handles the OAuth callback from Spotify. It validates
// the state parameter, exchanges the authorization code for tokens, and stores
// them in the config.
func handleSpotifyCallback(cfg *config.Persistence, rebuild func(name string, rawCfg json.RawMessage) error) http.HandlerFunc {
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

		// Validate state against the cookie stored during login.
		cookie, err := r.Cookie("spotify_oauth_state")
		if err != nil || cookie.Value != state {
			http.Error(w, "invalid state — CSRF check failed", http.StatusForbidden)
			return
		}
		verifier := cookie.Value

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
			r.Context(), code, verifier, spCfg.ClientID, spCfg.RedirectURI,
		)
		if err != nil {
			log.Printf("spotify: token exchange failed: %v", err)
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
				return err
			}
			if c.Sources == nil {
				c.Sources = make(map[string]json.RawMessage)
			}
			c.Sources["spotify"] = b
			return nil
		}); err != nil {
			log.Printf("spotify: failed to save tokens: %v", err)
			http.Error(w, "failed to save tokens", http.StatusInternalServerError)
			return
		}

		// Rebuild the spotify plugin so it picks up the new tokens.
		updated := cfg.Get()
		if raw, ok := updated.Sources["spotify"]; ok {
			if err := rebuild("spotify", raw); err != nil {
				log.Printf("spotify: plugin rebuild after OAuth: %v", err)
			}
		}

		// Clear the state cookie and redirect to the frontend.
		http.SetCookie(w, &http.Cookie{
			Name:   "spotify_oauth_state",
			Value:  "",
			Path:   "/api/spotify",
			MaxAge: -1,
		})
		http.Redirect(w, r, "/?spotify=connected", http.StatusFound)
	}
}
