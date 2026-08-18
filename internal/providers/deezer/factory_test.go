package deezer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestFactoryValidateConfig(t *testing.T) {
	// Empty config should be valid (plugin starts unconfigured).
	if err := Factory.ValidateConfig(json.RawMessage(`{}`)); err != nil {
		t.Errorf("empty config should be valid: %v", err)
	}

	// Valid config with quality.
	valid := json.RawMessage(`{"arl":"token123","quality":"flac","allow_fallback":true}`)
	if err := Factory.ValidateConfig(valid); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	// Invalid quality.
	invalid := json.RawMessage(`{"arl":"token123","quality":"wav"}`)
	if err := Factory.ValidateConfig(invalid); err == nil {
		t.Error("invalid quality should return error")
	}

	// Invalid JSON.
	if err := Factory.ValidateConfig(json.RawMessage(`{bad`)); err == nil {
		t.Error("invalid JSON should return error")
	}

	// ARL empty, bad quality — should skip quality check (not configured).
	noARL := json.RawMessage(`{"arl":"","quality":"wav"}`)
	if err := Factory.ValidateConfig(noARL); err != nil {
		t.Errorf("empty ARL with bad quality should be valid (quality check skipped): %v", err)
	}
}

func TestFactoryDefaultConfig(t *testing.T) {
	def := Factory.DefaultConfig()
	if !json.Valid(def) {
		t.Error("default config is not valid JSON")
	}
	var cfg DeezerConfig
	if err := json.Unmarshal(def, &cfg); err != nil {
		t.Fatalf("default config unmarshal: %v", err)
	}
}

func TestFactoryName(t *testing.T) {
	if Factory.Name() != "deezer" {
		t.Errorf("name = %q, want deezer", Factory.Name())
	}
	if Factory.DisplayName() != "Deezer" {
		t.Errorf("display = %q, want Deezer", Factory.DisplayName())
	}
	caps := Factory.Capabilities()
	hasDownload := false
	hasPlaylist := false
	for _, c := range caps {
		if c == "download" {
			hasDownload = true
		}
		if c == "playlist" {
			hasPlaylist = true
		}
	}
	if !hasDownload {
		t.Error("capabilities missing 'download'")
	}
	if !hasPlaylist {
		t.Error("capabilities missing 'playlist'")
	}
}

func TestCapabilityStatus(t *testing.T) {
	// Nothing configured and nothing verified: no green, no yellow.
	c := &DownloadClient{cfg: DeezerConfig{}}
	status := c.CapabilityStatus()
	if status["download"] != "not_configured" {
		t.Errorf("download status = %q, want not_configured", status["download"])
	}
	if status["discovery"] != "not_configured" {
		t.Errorf("discovery status = %q, want not_configured", status["discovery"])
	}
	if status["metadata"] != "not_configured" {
		t.Errorf("metadata status = %q, want not_configured", status["metadata"])
	}

	// ARL set and authenticated: all capabilities connected.
	t.Run("authenticated all connected", func(t *testing.T) {
		c := &DownloadClient{cfg: DeezerConfig{ARL: "token"}}
		c.tokenMu.Lock()
		c.authenticated = true
		c.tokenMu.Unlock()
		status := c.CapabilityStatus()
		for _, cap := range []string{"download", "playlist", "discovery", "metadata"} {
			if status[cap] != "connected" {
				t.Errorf("%s status = %q, want connected", cap, status[cap])
			}
		}
	})

	// ARL set but auth failing: downloads stay yellow (configured), while
	// discovery/metadata must not claim connected.
	t.Run("arl unauthenticated", func(t *testing.T) {
		c := &DownloadClient{cfg: DeezerConfig{ARL: "token"}}
		status := c.CapabilityStatus()
		if status["download"] != "configured" {
			t.Errorf("download status = %q, want configured", status["download"])
		}
		if status["metadata"] != "not_configured" {
			t.Errorf("metadata status = %q, want not_configured", status["metadata"])
		}
	})

	// Metadata-only mode (no ARL) with a successful public-API check: only
	// discovery/metadata are verified and should read connected.
	t.Run("metadata only verified", func(t *testing.T) {
		c := &DownloadClient{cfg: DeezerConfig{}}
		c.tokenMu.Lock()
		c.publicHealthy = true
		c.tokenMu.Unlock()
		status := c.CapabilityStatus()
		if status["download"] != "not_configured" {
			t.Errorf("download status = %q, want not_configured", status["download"])
		}
		if status["discovery"] != "connected" {
			t.Errorf("discovery status = %q, want connected", status["discovery"])
		}
		if status["metadata"] != "connected" {
			t.Errorf("metadata status = %q, want connected", status["metadata"])
		}
	})
}

// fakeRoundTripper returns a canned response for every request.
type fakeRoundTripper struct {
	status int
	body   string
	err    error
}

func (f *fakeRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

// metadataOnlyClient builds a DownloadClient with no ARL whose public-API
// client talks to a fake transport, so CheckConnection needs no network.
func metadataOnlyClient(rt http.RoundTripper) *DownloadClient {
	dc := &DownloadClient{cfg: DeezerConfig{}, log: slog.New(slog.DiscardHandler)}
	api := New(DeezerConfig{}, slog.New(slog.DiscardHandler))
	api.httpClient.Transport = rt
	dc.api = api
	return dc
}

func TestCapabilityAccess(t *testing.T) {
	c := &DownloadClient{}
	access := c.CapabilityAccess()
	if access["discovery"] != "public" {
		t.Errorf("discovery access = %q, want public", access["discovery"])
	}
	if access["metadata"] != "public" {
		t.Errorf("metadata access = %q, want public", access["metadata"])
	}
	if acc, ok := access["download"]; ok {
		t.Errorf("download access present (%q), should be omitted (defaults to account)", acc)
	}
}

func TestCheckConnectionMetadataOnly(t *testing.T) {
	t.Run("public api reachable marks healthy", func(t *testing.T) {
		c := metadataOnlyClient(&fakeRoundTripper{status: http.StatusOK, body: `{"data":[]}`})
		if err := c.CheckConnection(context.Background()); err != nil {
			t.Fatalf("CheckConnection: %v", err)
		}
		c.tokenMu.RLock()
		healthy := c.publicHealthy
		c.tokenMu.RUnlock()
		if !healthy {
			t.Error("publicHealthy = false, want true after successful check")
		}
		if !c.Connected() {
			t.Error("Connected() = false, want true in metadata-only mode")
		}
	})

	t.Run("public api down marks unhealthy", func(t *testing.T) {
		c := metadataOnlyClient(&fakeRoundTripper{status: http.StatusInternalServerError, body: `oops`})
		if err := c.CheckConnection(context.Background()); err == nil {
			t.Fatal("CheckConnection returned nil, want error for HTTP 500")
		}
		c.tokenMu.RLock()
		healthy := c.publicHealthy
		c.tokenMu.RUnlock()
		if healthy {
			t.Error("publicHealthy = true, want false after failed check")
		}
	})

	t.Run("network error marks unhealthy", func(t *testing.T) {
		c := metadataOnlyClient(&fakeRoundTripper{err: errors.New("boom")})
		if err := c.CheckConnection(context.Background()); err == nil {
			t.Fatal("CheckConnection returned nil, want error for network failure")
		}
		c.tokenMu.RLock()
		healthy := c.publicHealthy
		c.tokenMu.RUnlock()
		if healthy {
			t.Error("publicHealthy = true, want false after network failure")
		}
	})
}

// routeRoundTripper answers by host: the public metadata API (api.deezer.com)
// and the auth gateway (www.deezer.com) get distinct canned responses.
type routeRoundTripper struct {
	apiBody string
	gwBody  string
	gwErr   error
}

func (r *routeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "deezer.com") && !strings.Contains(req.URL.Host, "api.") {
		if r.gwErr != nil {
			return nil, r.gwErr
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(r.gwBody)), Header: make(http.Header)}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(r.apiBody)), Header: make(http.Header)}, nil
}

// arlClient builds a DownloadClient with an ARL whose public-API client and
// auth gateway both talk to fake transports, so CheckConnection needs no
// network.
func arlClient(rt *routeRoundTripper) *DownloadClient {
	dc := &DownloadClient{cfg: DeezerConfig{ARL: "token"}, log: slog.New(slog.DiscardHandler)}
	api := New(DeezerConfig{}, slog.New(slog.DiscardHandler))
	api.httpClient.Transport = rt
	dc.api = api
	dc.client = &http.Client{Transport: rt}
	return dc
}

func TestCheckConnectionWithARL(t *testing.T) {
	const gwOK = `{"error":[],"results":{"USER":{"USER_ID":123,"OPTIONS":{"license_token":"lt"}},"checkForm":"cf"}}`
	const gwFail = `{"error":["AUTH_FAILED"],"results":{}}`

	t.Run("auth ok keeps all capabilities connected", func(t *testing.T) {
		c := arlClient(&routeRoundTripper{apiBody: `{"data":[]}`, gwBody: gwOK})
		if err := c.CheckConnection(context.Background()); err != nil {
			t.Fatalf("CheckConnection: %v", err)
		}
		if !c.Connected() {
			t.Error("Connected() = false, want true with public API + auth ok")
		}
		status := c.CapabilityStatus()
		for _, cap := range []string{"download", "playlist", "discovery", "metadata"} {
			if status[cap] != "connected" {
				t.Errorf("%s status = %q, want connected", cap, status[cap])
			}
		}
	})

	t.Run("auth fails but public api ok keeps metadata connected", func(t *testing.T) {
		c := arlClient(&routeRoundTripper{apiBody: `{"data":[]}`, gwBody: gwFail})
		if err := c.CheckConnection(context.Background()); err == nil {
			t.Fatal("CheckConnection returned nil, want auth error")
		}
		if !c.Connected() {
			t.Error("Connected() = false, want true when public API works even without auth")
		}
		status := c.CapabilityStatus()
		if status["download"] != "configured" {
			t.Errorf("download status = %q, want configured", status["download"])
		}
		if status["discovery"] != "connected" || status["metadata"] != "connected" {
			t.Errorf("discovery/metadata should stay connected via public API, got %q/%q", status["discovery"], status["metadata"])
		}
	})

	t.Run("auth gateway unreachable clears stale authentication", func(t *testing.T) {
		c := arlClient(&routeRoundTripper{apiBody: `{"data":[]}`, gwBody: "", gwErr: errors.New("boom")})
		c.tokenMu.Lock()
		c.authenticated = true
		c.tokenMu.Unlock()
		if err := c.CheckConnection(context.Background()); err == nil {
			t.Fatal("CheckConnection returned nil, want error when gateway unreachable")
		}
		c.tokenMu.RLock()
		auth := c.authenticated
		c.tokenMu.RUnlock()
		if auth {
			t.Error("authenticated = true after failed auth, want false (stale status)")
		}
	})
}
