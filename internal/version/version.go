// Package version carries the build version stamped into the binary at link
// time. It is the single source of truth for "which build is this", surfaced by
// GET /api/version and rendered in the UI sidebar.
package version

// Version is the released version (for example "v0.9.0") or a development
// marker. It defaults to "dev" for a plain `go build`/`go run` and is overridden
// by the release build via:
//
//	-ldflags "-X github.com/ramonskie/groovearr/internal/version.Version=v0.9.0"
//
// Both the Makefile and the Dockerfile pass it, so a binary built either way
// reports its real version instead of "dev".
var Version = "dev"
