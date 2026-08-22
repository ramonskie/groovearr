package deezer

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

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	return &Client{
		cfg:         DeezerConfig{},
		httpClient:  srv.Client(),
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0, // no pacing in tests
	}, srv
}

// TestSearchTracks_RateLimited asserts a 429 from Deezer surfaces the shared
// metadata.ErrRateLimited sentinel so enrichment can cool it down.
func TestSearchTracks_RateLimited(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer srv.Close()

	_, err := client.SearchTracks(context.Background(), "Daft Punk", 5)
	if err == nil {
		t.Fatal("expected error for 429, got nil")
	}
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v, want metadata.ErrRateLimited sentinel", err)
	}
}

// TestSearchTracks_NotFound is a sanity check that a non-rate-limit error
// keeps its own message and is NOT mislabeled as a rate limit.
func TestSearchTracks_NotFound(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	_, err := client.SearchTracks(context.Background(), "Daft Punk", 5)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("404 error mislabeled as rate limited: %v", err)
	}
}
