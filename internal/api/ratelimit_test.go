package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
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

// testBuckets mirrors defaultRateBuckets with a small download window so a
// short burst trips the limiter deterministically. Other buckets stay generous
// so only the download route is exercised.
func testBuckets(downloadMax int) []rateLimitBucket {
	return []rateLimitBucket{
		{name: "search", max: 10000, window: time.Minute},
		{name: "download", max: downloadMax, window: time.Minute},
		{name: "scan", max: 10000, window: time.Minute},
		{name: "enrich", max: 10000, window: time.Minute},
		{name: "duplicates", max: 10000, window: time.Minute},
		{name: "login", max: 10000, window: time.Minute},
	}
}

// TestDownloadRoutesShareRateLimitBucket is the R3.1 guard: both library
// download routes must draw from the single shared "download" bucket, so a
// burst on one path trips the other. If either route were left unwrapped (or
// given its own bucket), the cross-path 429 below would not fire.
func TestDownloadRoutesShareRateLimitBucket(t *testing.T) {
	s := newDownloadServer(t, t.TempDir(), map[int64]*domain.Track{})
	limiter := newIPRateLimiter(testBuckets(3), testAPILogger())
	t.Cleanup(limiter.Shutdown)
	s.rateLimiter = limiter

	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Three allowed track requests reach the handler (404: unknown id) and
	// fill the shared bucket to max.
	for i := 0; i < 3; i++ {
		if rec := get("/api/library/tracks/1/download"); rec.Code != http.StatusNotFound {
			t.Fatalf("track download request %d = %d, want 404 before the bucket fills (%s)",
				i+1, rec.Code, rec.Body.String())
		}
	}

	// The album route must contend for the SAME bucket: the next download
	// request trips the limiter even though it targets a different path.
	rec := get("/api/library/albums/1/download")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("album download after track burst = %d, want 429 (shared download bucket)", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("429 response missing Retry-After header")
	}
	if got := rec.Header().Get("X-RateLimit-Bucket"); got != "download" {
		t.Errorf("X-RateLimit-Bucket = %q, want %q", got, "download")
	}

	// A non-download route in the same table stays unlimited — the bucket is
	// scoped to downloads, not applied globally.
	if rec := get("/api/health"); rec.Code == http.StatusTooManyRequests {
		t.Error("unrelated route was rate limited by the download bucket")
	}
}
