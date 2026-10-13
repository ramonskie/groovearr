package api

import (
	"net/http"

	"github.com/ramonskie/groovearr/internal/version"
)

// versionResponse is the build version reported to the UI. A struct rather than
// a bare string so the payload can grow (commit, build time) without a breaking
// change for the client.
type versionResponse struct {
	Version string `json:"version"`
}

// handleVersion reports the build version stamped into the binary at link time
// (internal/version). It is authenticated but not admin-only — every signed-in
// user sees the version in the sidebar — and is side-effect free: no store or
// provider access.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, versionResponse{Version: version.Version})
}
