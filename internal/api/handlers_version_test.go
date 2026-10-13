package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramonskie/groovearr/internal/version"
)

// TestHandleVersion verifies the handler reflects the link-time-stamped version
// (internal/version), which the sidebar renders.
func TestHandleVersion(t *testing.T) {
	prev := version.Version
	version.Version = "v9.9.9-test"
	t.Cleanup(func() { version.Version = prev })

	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleVersion(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got versionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != "v9.9.9-test" {
		t.Errorf("version = %q, want %q", got.Version, "v9.9.9-test")
	}
}
