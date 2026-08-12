package tidal

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/plugin"
)

// fakePlugin implements plugin.BasePlugin without being a *Client, so handler
// type assertions against *Client fail predictably.
type fakePlugin struct{}

func (fakePlugin) Name() string                   { return "tidal" }
func (fakePlugin) DisplayName() string            { return "Fake Tidal" }
func (fakePlugin) IsConfigured() bool             { return true }
func (fakePlugin) CheckConnection(ctx context.Context) error { return nil }
func (fakePlugin) Connected() bool                { return true }
func (fakePlugin) CapabilityStatus() map[string]string {
	return map[string]string{"download": "connected"}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// ─── /api/tidal/login ───────────────────────────────────────────────────

func TestHandleTidalLoginPluginNotFound(t *testing.T) {
	reg := plugin.NewRegistry()
	h := handleTidalLogin(nil, reg, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/tidal/login", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHandleTidalLoginWrongType(t *testing.T) {
	reg := plugin.NewRegistry()
	if err := reg.Register(fakePlugin{}); err != nil {
		t.Fatal(err)
	}
	h := handleTidalLogin(nil, reg, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/tidal/login", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ─── /api/tidal/poll ────────────────────────────────────────────────────

func TestHandleTidalPollMissingDeviceCode(t *testing.T) {
	h := handleTidalPoll(nil, nil, discardLogger(), nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/tidal/poll", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assertPollJSON(t, rr, "error", "missing device_code parameter")
}

func TestHandleTidalPollUnknownDeviceCode(t *testing.T) {
	h := handleTidalPoll(nil, nil, discardLogger(), nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/tidal/poll?device_code=unknown", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assertPollJSON(t, rr, "expired", "")
}

func TestHandleTidalPollExpiredAuth(t *testing.T) {
	reg := plugin.NewRegistry()
	if err := reg.Register(fakePlugin{}); err != nil {
		t.Fatal(err)
	}
	h := handleTidalPoll(nil, reg, discardLogger(), nil, nil)

	pendingAuthsMu.Lock()
	pendingAuths["expired-code"] = pendingAuth{
		deviceCode: "expired-code",
		userCode:   "ABCD",
		expiresAt:  time.Now().Add(-time.Minute),
	}
	pendingAuthsMu.Unlock()
	defer func() {
		pendingAuthsMu.Lock()
		delete(pendingAuths, "expired-code")
		pendingAuthsMu.Unlock()
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/tidal/poll?device_code=expired-code", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assertPollJSON(t, rr, "expired", "")

	// Expired entry should be cleaned up.
	pendingAuthsMu.Lock()
	_, exists := pendingAuths["expired-code"]
	pendingAuthsMu.Unlock()
	if exists {
		t.Error("expired auth not removed from pendingAuths")
	}
}

func TestHandleTidalPollPluginNotRegistered(t *testing.T) {
	reg := plugin.NewRegistry()
	h := handleTidalPoll(nil, reg, discardLogger(), nil, nil)

	pendingAuthsMu.Lock()
	pendingAuths["no-plugin"] = pendingAuth{
		deviceCode: "no-plugin",
		userCode:   "WXYZ",
		expiresAt:  time.Now().Add(time.Minute),
	}
	pendingAuthsMu.Unlock()
	defer func() {
		pendingAuthsMu.Lock()
		delete(pendingAuths, "no-plugin")
		pendingAuthsMu.Unlock()
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/tidal/poll?device_code=no-plugin", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assertPollJSON(t, rr, "error", "tidal plugin not found")
}

func assertPollJSON(t *testing.T, rr *httptest.ResponseRecorder, wantStatus, wantMessageSubstring string) {
	t.Helper()
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, rr.Body.String())
	}
	if body["status"] != wantStatus {
		t.Errorf("status field = %q, want %q (body=%s)", body["status"], wantStatus, rr.Body.String())
	}
	if wantMessageSubstring != "" && !strings.Contains(body["message"], wantMessageSubstring) {
		t.Errorf("message = %q, want substring %q", body["message"], wantMessageSubstring)
	}
}
