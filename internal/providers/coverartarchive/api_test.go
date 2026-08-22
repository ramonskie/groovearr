package coverartarchive

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramonskie/groovearr/internal/metadata"
)

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func newTestAPIClient(t *testing.T, handler http.HandlerFunc) (*apiClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	return &apiClient{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
		log:        testLogger(),
	}, srv
}

// TestGetReleaseImages_RateLimited429 asserts a 429 from Cover Art Archive
// surfaces the shared metadata.ErrRateLimited sentinel.
func TestGetReleaseImages_RateLimited429(t *testing.T) {
	api, srv := newTestAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer srv.Close()

	_, err := api.GetReleaseImages(context.Background(), "mbid-1")
	if err == nil {
		t.Fatal("expected error for 429, got nil")
	}
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v, want metadata.ErrRateLimited sentinel", err)
	}
}

// TestGetReleaseImages_RateLimited503 asserts a 503 (Internet Archive
// temporarily offline) also surfaces the shared sentinel.
func TestGetReleaseImages_RateLimited503(t *testing.T) {
	api, srv := newTestAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv.Close()

	_, err := api.GetReleaseImages(context.Background(), "mbid-1")
	if err == nil {
		t.Fatal("expected error for 503, got nil")
	}
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v, want metadata.ErrRateLimited sentinel", err)
	}
}

// TestGetReleaseImages_NotFoundNil asserts 404 still returns nil (not found),
// not a rate-limit error.
func TestGetReleaseImages_NotFoundNil(t *testing.T) {
	api, srv := newTestAPIClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	img, err := api.GetReleaseImages(context.Background(), "mbid-missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if img != nil {
		t.Errorf("expected nil images for 404, got %+v", img)
	}
}
