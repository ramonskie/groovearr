package tidal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// pendingAuth holds in-progress device authorization state.
type pendingAuth struct {
	deviceCode string
	userCode   string
	expiresAt  time.Time
}

var (
	pendingAuths   = make(map[string]pendingAuth) // deviceCode → pendingAuth
	pendingAuthsMu sync.Mutex
)

func init() {
	go func() {
		for {
			time.Sleep(1 * time.Minute)
			pendingAuthsMu.Lock()
			now := time.Now()
			for k, v := range pendingAuths {
				if now.After(v.expiresAt) {
					delete(pendingAuths, k)
				}
			}
			pendingAuthsMu.Unlock()
		}
	}()
}

// RegisterOAuthRoutes adds Tidal OAuth device code flow endpoints to the mux.
func RegisterOAuthRoutes(mux *http.ServeMux, cfg *config.Persistence, registry *plugin.Registry, logger *slog.Logger, rebuild func(name string, rawCfg json.RawMessage) error, verify func(name string)) {
	mux.HandleFunc("GET /api/tidal/login", handleTidalLogin(cfg, registry, logger))
	mux.HandleFunc("GET /api/tidal/poll", handleTidalPoll(cfg, registry, logger, rebuild, verify))

	// Wire token persistence on startup so refreshed tokens survive restarts.
	if tp := registry.Get("tidal"); tp != nil {
		if tc, ok := tp.(*Client); ok {
			tc.SetTokenPersistCallback(func(accessToken, refreshToken string) {
				if err := cfg.Update(func(c *config.Config) error {
					raw, ok := c.Sources["tidal"]
					if !ok {
						return nil
					}
					var tcfg TidalConfig
					if err := json.Unmarshal(raw, &tcfg); err != nil {
						return err
					}
					tcfg.AccessToken = accessToken
					tcfg.RefreshToken = refreshToken
					b, err := json.Marshal(tcfg)
					if err != nil {
						return err
					}
					c.Sources["tidal"] = b
					return nil
				}); err != nil {
					logger.Error("tidal token persist failed", "error", err, "component", "tidal_oauth")
				}
			})
		}
	}
}

// handleTidalLogin initiates the Tidal device authorization flow and returns
// a page showing the user code and verification URL with auto-polling.
func handleTidalLogin(cfg *config.Persistence, registry *plugin.Registry, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := registry.Get("tidal")
		if p == nil {
			http.Error(w, "tidal plugin not found", http.StatusNotFound)
			return
		}

		client, ok := p.(*Client)
		if !ok {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		userCode, verifyURL, deviceCode, expiresAt, err := client.StartDeviceAuth(r.Context())
		if err != nil {
			logger.Error("tidal device auth failed", "error", err, "component", "tidal_oauth")
			http.Error(w, "Failed to start device authorization: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Ensure the verification URL has a scheme so the browser doesn't interpret
		// it as relative (Tidal API sometimes returns bare "link.tidal.com/CODE").
		if !strings.HasPrefix(verifyURL, "http://") && !strings.HasPrefix(verifyURL, "https://") {
			verifyURL = "https://" + verifyURL
		}
		// Escape double-quotes in URL to prevent href attribute breakage.
		verifyURL = strings.ReplaceAll(verifyURL, "\"", "&quot;")

		// Store pending auth for polling.
		pendingAuthsMu.Lock()
		pendingAuths[deviceCode] = pendingAuth{
			deviceCode: deviceCode,
			userCode:   userCode,
			expiresAt:  expiresAt,
		}
		pendingAuthsMu.Unlock()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, deviceAuthPage, verifyURL, verifyURL, userCode, deviceCode)
	}
}

// handleTidalPoll checks if the device authorization has been completed.
func handleTidalPoll(cfg *config.Persistence, registry *plugin.Registry, logger *slog.Logger, rebuild func(name string, rawCfg json.RawMessage) error, verify func(name string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceCode := r.URL.Query().Get("device_code")
		if deviceCode == "" {
			writePollJSON(w, "error", "missing device_code parameter")
			return
		}

		// Check if auth is pending.
		pendingAuthsMu.Lock()
		auth, exists := pendingAuths[deviceCode]
		if !exists {
			pendingAuthsMu.Unlock()
			writePollJSON(w, "expired", "Authorization session expired or not found. Please try again.")
			return
		}
		if time.Now().After(auth.expiresAt) {
			delete(pendingAuths, deviceCode)
			pendingAuthsMu.Unlock()
			writePollJSON(w, "expired", "Authorization code expired. Please try again.")
			return
		}
		pendingAuthsMu.Unlock()

		p := registry.Get("tidal")
		if p == nil {
			writePollJSON(w, "error", "tidal plugin not found")
			return
		}

		client, ok := p.(*Client)
		if !ok {
			writePollJSON(w, "error", "internal error")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		if err := client.CompleteDeviceAuth(ctx); err != nil {
			if strings.Contains(err.Error(), "pending") {
				writePollJSON(w, "pending", "Waiting for authorization...")
				return
			}
			logger.Error("tidal device auth completion failed", "error", err, "component", "tidal_oauth")
			// Don't delete pending auth — user may retry.
			writePollJSON(w, "error", err.Error())
			return
		}

		// Clean up pending auth.
		pendingAuthsMu.Lock()
		delete(pendingAuths, deviceCode)
		pendingAuthsMu.Unlock()

		// Extract token and save to config.
		tokJSON, err := client.GetTokenJSON()
		if err != nil {
			logger.Error("tidal get token failed", "error", err, "component", "tidal_oauth")
			writePollJSON(w, "error", "Failed to retrieve token: "+err.Error())
			return
		}

		// Merge token into tidal config.
		current := cfg.Get()
		raw, ok := current.Sources["tidal"]
		if !ok {
			writePollJSON(w, "error", "tidal not configured")
			return
		}

		var tidalCfg TidalConfig
		if err := json.Unmarshal(raw, &tidalCfg); err != nil {
			writePollJSON(w, "error", "invalid tidal config")
			return
		}

		// Extract access and refresh token from the oauth2.Token.
		var tok struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Expiry       time.Time `json:"expiry"`
		}
		if err := json.Unmarshal(tokJSON, &tok); err != nil {
			writePollJSON(w, "error", "invalid token format")
			return
		}

		tidalCfg.AccessToken = tok.AccessToken
		tidalCfg.RefreshToken = tok.RefreshToken

		newRaw, err := json.Marshal(tidalCfg)
		if err != nil {
			writePollJSON(w, "error", "failed to marshal config")
			return
		}

		if err := cfg.Update(func(c *config.Config) error {
			if c.Sources == nil {
				c.Sources = make(map[string]json.RawMessage)
			}
			c.Sources["tidal"] = newRaw
			return nil
		}); err != nil {
			logger.Error("tidal token save failed", "error", err, "component", "tidal_oauth")
			writePollJSON(w, "error", "Failed to save token: "+err.Error())
			return
		}

		// Rebuild the plugin.
		if err := rebuild("tidal", newRaw); err != nil {
			logger.Error("tidal plugin rebuild failed", "error", err, "component", "tidal_oauth")
			writePollJSON(w, "error", "Token saved but plugin rebuild failed: "+err.Error())
			return
		}
		if verify != nil {
			verify("tidal")
		}

		// Wire token persistence on the rebuilt client so future refreshes save to config.
		if tp := registry.Get("tidal"); tp != nil {
			if tc, ok := tp.(*Client); ok {
				tc.SetTokenPersistCallback(func(accessToken, refreshToken string) {
					if err := cfg.Update(func(c *config.Config) error {
						raw, ok := c.Sources["tidal"]
						if !ok {
							return nil
						}
						var tc TidalConfig
						if err := json.Unmarshal(raw, &tc); err != nil {
							return err
						}
						tc.AccessToken = accessToken
						tc.RefreshToken = refreshToken
						b, err := json.Marshal(tc)
						if err != nil {
							return err
						}
						c.Sources["tidal"] = b
						return nil
					}); err != nil {
						logger.Error("tidal token persist failed", "error", err, "component", "tidal_oauth")
					}
				})
			}
		}

		writePollJSON(w, "connected", "Authorization successful! You can close this page.")
	}
}

func writePollJSON(w http.ResponseWriter, status, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": status, "message": message})
}

// deviceAuthPage is the HTML page shown to the user during device authorization.
const deviceAuthPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Tidal — Device Authorization</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, -apple-system, sans-serif; background: #0f172a; color: #e2e8f0; display: flex; justify-content: center; align-items: center; min-height: 100vh; }
  .card { background: #1e293b; border-radius: 12px; padding: 2rem; max-width: 480px; width: 90%%; text-align: center; box-shadow: 0 4px 24px rgba(0,0,0,0.3); }
  h1 { font-size: 1.25rem; margin-bottom: 1rem; color: #f8fafc; }
  .code { font-size: 2rem; font-weight: 700; letter-spacing: 0.25em; color: #38bdf8; background: #0f172a; border-radius: 8px; padding: 0.75rem 1.5rem; display: inline-block; margin: 0.75rem 0; }
  a { color: #38bdf8; word-break: break-all; }
  .step { margin: 1rem 0; color: #94a3b8; font-size: 0.9rem; }
  #status { margin-top: 1rem; padding: 0.5rem; border-radius: 6px; font-size: 0.85rem; }
  .pending { background: #1e3a5f; color: #93c5fd; }
  .error { background: #3b1f1f; color: #fca5a5; }
  .success { background: #1f3b2f; color: #86efac; }
</style>
</head>
<body>
<div class="card">
  <h1>Connect Tidal Account</h1>
  <div class="step">1. Open this link in your browser:</div>
  <a href="%s" target="_blank" rel="noopener">%s</a>
  <div class="step">2. Enter this code when prompted:</div>
  <div class="code">%s</div>
  <div class="step">3. This page will update automatically.</div>
  <div id="status" class="pending">Waiting for authorization...</div>
</div>
<script>
(function() {
  var deviceCode = %q;
  var done = false;
  function poll() {
    if (done) return;
    fetch('/api/tidal/poll?device_code=' + encodeURIComponent(deviceCode))
      .then(function(r) { return r.json(); })
      .then(function(data) {
        var el = document.getElementById('status');
        el.textContent = data.message;
        if (data.status === 'connected') {
          el.className = 'success';
          done = true;
          setTimeout(function() { window.close(); }, 3000);
        } else if (data.status === 'error' || data.status === 'expired') {
          el.className = 'error';
          done = true;
        } else {
          el.className = 'pending';
        }
      })
      .catch(function() {
        // Network error — retry.
      });
  }
  setInterval(poll, 4000);
  poll();
})();
</script>
</body>
</html>`
