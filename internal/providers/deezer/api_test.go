package deezer

import (
	"context"
	"encoding/json"
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

func TestGetTrackByISRC(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/track/isrc:US1" {
			t.Errorf("path = %q, want /track/isrc:US1", got)
		}
		_, _ = w.Write([]byte(`{
			"id": 3135556,
			"title": "Harder Better Faster Stronger",
			"duration": 224,
			"isrc": "US1",
			"track_position": 2,
			"disk_number": 1,
			"artist": {"id": 27, "name": "Daft Punk"},
			"album": {"id": 302127, "title": "Discovery", "nb_tracks": 14}
		}`))
	})
	defer srv.Close()

	track, err := client.GetTrackByISRC(context.Background(), "US1")
	if err != nil || track == nil {
		t.Fatalf("GetTrackByISRC = (%v, %v), want track", track, err)
	}
	if track.ID != 3135556 || track.ISRC != "US1" || track.Album.ID != 302127 || track.Album.NbTracks != 14 {
		t.Fatalf("track = %+v", track)
	}
}

func TestGetTrackByISRC_NotFound(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"type":"DataException","message":"no data","code":800}}`))
	})
	defer srv.Close()

	track, err := client.GetTrackByISRC(context.Background(), "US1")
	if err != nil {
		t.Fatalf("GetTrackByISRC err = %v, want nil for a miss", err)
	}
	if track != nil {
		t.Fatalf("track = %+v, want nil", track)
	}
}

func TestGetTrackByISRC_HTTP404(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	track, err := client.GetTrackByISRC(context.Background(), "US1")
	if err != nil {
		t.Fatalf("GetTrackByISRC err = %v, want nil for a 404 miss", err)
	}
	if track != nil {
		t.Fatalf("track = %+v, want nil", track)
	}
}

func TestTrackIdentityKeepsIdentityWhenAlbumContextBusy(t *testing.T) {
	api, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/album/9" {
			t.Errorf("path = %q, want /album/9", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv.Close()

	var track Track
	if err := json.Unmarshal([]byte(`{
		"id": 7, "title": "Song", "duration": 200, "isrc": "US1",
		"track_position": 3, "disk_number": 1,
		"artist": {"id": 1, "name": "Artist"},
		"album": {"id": 9, "title": "Album", "nb_tracks": 0}
	}`), &track); err != nil {
		t.Fatalf("unmarshal track: %v", err)
	}

	id, err := (&DownloadClient{api: api, log: testLogger()}).trackIdentity(context.Background(), &track)
	if err != nil {
		t.Fatalf("trackIdentity: %v, want the identity when optional album context is busy", err)
	}
	if id == nil || id.ProviderTrackID != "7" || id.ProviderAlbumID != "9" {
		t.Fatalf("identity = %+v, want the track identity without album context", id)
	}
	if id.AlbumTrackCount != 0 || id.AlbumType != "" {
		t.Fatalf("album context = %+v, want unknown context", id)
	}
}

func TestTrackIdentityPropagatesAlbumContextRateLimit(t *testing.T) {
	api, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/album/9" {
			t.Errorf("path = %q, want /album/9", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer srv.Close()

	var track Track
	if err := json.Unmarshal([]byte(`{
		"id": 7, "title": "Song", "duration": 200, "isrc": "US1",
		"track_position": 3, "disk_number": 1,
		"artist": {"id": 1, "name": "Artist"},
		"album": {"id": 9, "title": "Album", "nb_tracks": 0}
	}`), &track); err != nil {
		t.Fatalf("unmarshal track: %v", err)
	}

	id, err := (&DownloadClient{api: api, log: testLogger()}).trackIdentity(context.Background(), &track)
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("trackIdentity error = %v, want metadata.ErrRateLimited", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil on a rate limit", id)
	}
}

func TestTrackIdentityPropagatesAlbumContextCancellation(t *testing.T) {
	api, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s after cancellation", r.URL.Path)
		http.NotFound(w, r)
	})
	defer srv.Close()

	var track Track
	if err := json.Unmarshal([]byte(`{
		"id": 7, "title": "Song", "duration": 200, "isrc": "US1",
		"track_position": 3, "disk_number": 1,
		"artist": {"id": 1, "name": "Artist"},
		"album": {"id": 9, "title": "Album", "nb_tracks": 0}
	}`), &track); err != nil {
		t.Fatalf("unmarshal track: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id, err := (&DownloadClient{api: api, log: testLogger()}).trackIdentity(ctx, &track)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("trackIdentity error = %v, want context.Canceled", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil after cancellation", id)
	}
}

func TestTrackIdentityMapping(t *testing.T) {
	var track Track
	if err := json.Unmarshal([]byte(`{
		"id": 7, "title": "Song", "duration": 200, "isrc": "US1",
		"track_position": 3, "disk_number": 1,
		"artist": {"id": 1, "name": "Artist"},
		"album": {"id": 9, "title": "Album", "nb_tracks": 12, "record_type": "album"}
	}`), &track); err != nil {
		t.Fatalf("unmarshal track: %v", err)
	}
	id, err := (&DownloadClient{}).trackIdentity(context.Background(), &track)
	if err != nil {
		t.Fatalf("trackIdentity: %v", err)
	}
	if id.ProviderName != "deezer" || id.ProviderTrackID != "7" || id.ProviderAlbumID != "9" {
		t.Fatalf("identity = %+v", id)
	}
	if id.Title != "Song" || id.ArtistName != "Artist" || id.AlbumTitle != "Album" {
		t.Fatalf("identity text = %+v", id)
	}
	if id.DurationMs != 200000 || id.AlbumTrackCount != 12 || id.TrackNumber != 3 || id.ISRC != "US1" {
		t.Fatalf("identity numbers = %+v", id)
	}
}
