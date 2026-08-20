package api

import (
	"io"
	"log/slog"
	"testing"
)

// TestRateLimitBucketsRegistered ensures every bucket name used by the routes
// exists in defaultRateBuckets — an unknown bucket makes allow() deny every
// request (429), silently breaking the endpoint.
func TestRateLimitBucketsRegistered(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	limiter := newIPRateLimiter(defaultRateBuckets(), logger)

	used := []string{"search", "download", "scan", "enrich", "duplicates", "login"}
	for _, bucket := range used {
		ok, _ := limiter.allow("127.0.0.1", bucket)
		if !ok {
			t.Errorf("bucket %q not allowed — is it missing from defaultRateBuckets?", bucket)
		}
	}
}
