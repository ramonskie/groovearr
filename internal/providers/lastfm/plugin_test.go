package lastfm

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopWriter{}, nil))
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// ─── Test helpers ──────────────────────────────────────────────────────────

// newTestPlugin creates a Plugin backed by a mock HTTP server.
func newTestPlugin(t *testing.T, handler http.Handler) (*Plugin, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)

	client := &Client{
		apiKey:     "test-api-key",
		httpClient: srv.Client(),
		log:        testLogger(),
		baseURL:    srv.URL,
	}
	p := &Plugin{
		client: client,
		log:    testLogger(),
	}
	return p, srv.Close
}

// newTestPluginNoKey creates a Plugin with an empty API key.
func newTestPluginNoKey(t *testing.T, handler http.Handler) (*Plugin, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)

	client := &Client{
		apiKey:     "",
		httpClient: srv.Client(),
		log:        testLogger(),
		baseURL:    srv.URL,
	}
	p := &Plugin{
		client: client,
		log:    testLogger(),
	}
	return p, srv.Close
}

// lfmHandler returns a handler that dispatches based on the "method" query param.
type lfmHandler struct {
	artistSearch   func(w http.ResponseWriter, artist string, limit int)
	topAlbums      func(w http.ResponseWriter, artist string, limit int)
	albumInfo      func(w http.ResponseWriter, artist, album string)
}

func (h *lfmHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Query().Get("method")

	switch method {
	case "artist.search":
		if h.artistSearch != nil {
			artist := r.URL.Query().Get("artist")
			limit := 1
			_ = limit // default
			h.artistSearch(w, artist, limit)
			return
		}
	case "artist.gettopalbums":
		if h.topAlbums != nil {
			artist := r.URL.Query().Get("artist")
			limit := 1
			h.topAlbums(w, artist, limit)
			return
		}
	case "album.getinfo":
		if h.albumInfo != nil {
			artist := r.URL.Query().Get("artist")
			album := r.URL.Query().Get("album")
			h.albumInfo(w, artist, album)
			return
		}
	}
	http.Error(w, "unexpected method", http.StatusBadRequest)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

// ─── IsConfigured ──────────────────────────────────────────────────────────

func TestPlugin_IsConfigured_WithKey(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	if !p.IsConfigured() {
		t.Error("IsConfigured should be true when api_key is set")
	}
}

func TestPlugin_IsConfigured_EmptyKey(t *testing.T) {
	p, cleanup := newTestPluginNoKey(t, nil)
	defer cleanup()

	if p.IsConfigured() {
		t.Error("IsConfigured should be false when api_key is empty")
	}
}

// ─── CheckConnection ───────────────────────────────────────────────────────

func TestPlugin_CheckConnection_Success(t *testing.T) {
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": []any{},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	err := p.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection should succeed: %v", err)
	}
	if !p.Connected() {
		t.Error("Connected should be true after successful check")
	}
}

func TestPlugin_CheckConnection_Failure(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	err := p.CheckConnection(context.Background())
	if err == nil {
		t.Fatal("CheckConnection should fail with 500")
	}
	if p.Connected() {
		t.Error("Connected should be false after failed check")
	}
}

// ─── CapabilityStatus ──────────────────────────────────────────────────────

func TestPlugin_CapabilityStatus_NotConfigured(t *testing.T) {
	p, cleanup := newTestPluginNoKey(t, nil)
	defer cleanup()

	status := p.CapabilityStatus()
	if status["discovery"] != "not_configured" {
		t.Errorf("discovery status = %q, want not_configured", status["discovery"])
	}
	if status["metadata"] != "not_configured" {
		t.Errorf("metadata status = %q, want not_configured", status["metadata"])
	}
}

func TestPlugin_CapabilityStatus_Configured(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	status := p.CapabilityStatus()
	if status["discovery"] != "configured" {
		t.Errorf("discovery status = %q, want configured", status["discovery"])
	}
	if status["metadata"] != "configured" {
		t.Errorf("metadata status = %q, want configured", status["metadata"])
	}
}

func TestPlugin_CapabilityStatus_Connected(t *testing.T) {
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": []any{},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	_ = p.CheckConnection(context.Background())
	status := p.CapabilityStatus()
	if status["discovery"] != "connected" {
		t.Errorf("discovery status = %q, want connected", status["discovery"])
	}
	if status["metadata"] != "connected" {
		t.Errorf("metadata status = %q, want connected", status["metadata"])
	}
}

// ─── SearchArtists ─────────────────────────────────────────────────────────

func TestPlugin_SearchArtists(t *testing.T) {
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": []map[string]any{
							{
								"name": "Daft Punk",
								"mbid": "056e4f3e-d505-4dad-8ec1-d04f521cbb56",
								"image": []map[string]any{
									{"#text": "https://lastfm.freetls.fastly.net/i/u/300x300/daft.jpg", "size": "large"},
								},
							},
							{
								"name": "NoMBID Artist",
								"mbid": "",
								"image": []map[string]any{},
							},
						},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	artists, err := p.SearchArtists(context.Background(), "Daft Punk", 10)
	if err != nil {
		t.Fatalf("SearchArtists: %v", err)
	}
	if len(artists) != 2 {
		t.Fatalf("got %d artists, want 2", len(artists))
	}

	// Artist with MBID should use composite "name||mbid" format.
	if artists[0].ProviderID != "Daft Punk||056e4f3e-d505-4dad-8ec1-d04f521cbb56" {
		t.Errorf("ProviderID = %q, want \"Daft Punk||056e4f3e-...\"", artists[0].ProviderID)
	}
	if artists[0].Name != "Daft Punk" {
		t.Errorf("Name = %q, want Daft Punk", artists[0].Name)
	}
	if artists[0].ImageURL != "https://lastfm.freetls.fastly.net/i/u/300x300/daft.jpg" {
		t.Errorf("ImageURL = %q", artists[0].ImageURL)
	}

	// Artist without MBID should fall back to Name as ProviderID.
	if artists[1].ProviderID != "NoMBID Artist" {
		t.Errorf("ProviderID = %q, want NoMBID Artist (name fallback)", artists[1].ProviderID)
	}
}

func TestPlugin_SearchArtists_SingleResult(t *testing.T) {
	// Last.fm API returns a single object (not array) when there's exactly 1 result.
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": map[string]any{
							"name":  "Solo Artist",
							"mbid":  "abc123",
							"image": []any{},
						},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	artists, err := p.SearchArtists(context.Background(), "Solo Artist", 10)
	if err != nil {
		t.Fatalf("SearchArtists: %v", err)
	}
	if len(artists) != 1 {
		t.Fatalf("got %d artists, want 1", len(artists))
	}
	if artists[0].Name != "Solo Artist" {
		t.Errorf("Name = %q, want Solo Artist", artists[0].Name)
	}
}

func TestPlugin_SearchArtists_Empty(t *testing.T) {
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": "",
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	artists, err := p.SearchArtists(context.Background(), "Nonexistent", 10)
	if err != nil {
		t.Fatalf("SearchArtists: %v", err)
	}
	if len(artists) != 0 {
		t.Errorf("expected 0 artists, got %d", len(artists))
	}
}

// ─── GetArtistAlbums ───────────────────────────────────────────────────────

func TestPlugin_GetArtistAlbums(t *testing.T) {
	h := &lfmHandler{
		topAlbums: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"topalbums": map[string]any{
					"album": []map[string]any{
						{
							"name":      "Discovery",
							"mbid":      "album-mbid-1",
							"playcount": 5000,
							"image": []map[string]any{
								{"#text": "https://example.com/discovery.jpg", "size": "large"},
							},
						},
						{
							"name":      "Homework",
							"mbid":      "",
							"playcount": 3000,
							"image":     []any{},
						},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	albums, err := p.GetArtistAlbums(context.Background(), "Daft Punk", 10)
	if err != nil {
		t.Fatalf("GetArtistAlbums: %v", err)
	}
	if len(albums) != 2 {
		t.Fatalf("got %d albums, want 2", len(albums))
	}

	// Album with MBID — format "artist::mbid".
	if albums[0].ProviderID != "Daft Punk::album-mbid-1" {
		t.Errorf("ProviderID = %q, want Daft Punk::album-mbid-1", albums[0].ProviderID)
	}
	if albums[0].Title != "Discovery" {
		t.Errorf("Title = %q, want Discovery", albums[0].Title)
	}
	if albums[0].Type != "album" {
		t.Errorf("Type = %q, want album", albums[0].Type)
	}
	if albums[0].ProviderName != "lastfm" {
		t.Errorf("ProviderName = %q, want lastfm", albums[0].ProviderName)
	}
	if albums[0].ArtistName != "Daft Punk" {
		t.Errorf("ArtistName = %q, want Daft Punk", albums[0].ArtistName)
	}
	if albums[0].CoverURL != "https://example.com/discovery.jpg" {
		t.Errorf("CoverURL = %q", albums[0].CoverURL)
	}

	// Album without MBID — format "artist::name".
	if albums[1].ProviderID != "Daft Punk::Homework" {
		t.Errorf("ProviderID = %q, want Daft Punk::Homework (name fallback)", albums[1].ProviderID)
	}
}

func TestPlugin_GetArtistAlbums_Empty(t *testing.T) {
	h := &lfmHandler{
		topAlbums: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"topalbums": map[string]any{
					"album": "",
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	albums, err := p.GetArtistAlbums(context.Background(), "Unknown", 10)
	if err != nil {
		t.Fatalf("GetArtistAlbums: %v", err)
	}
	if len(albums) != 0 {
		t.Errorf("expected 0 albums, got %d", len(albums))
	}
}

// ─── GetAlbumTracks ────────────────────────────────────────────────────────

func TestPlugin_GetAlbumTracks_SingleTrack(t *testing.T) {
	h := &lfmHandler{
		albumInfo: func(w http.ResponseWriter, artist, album string) {
			writeJSON(w, map[string]any{
				"album": map[string]any{
					"name":   "Single Track Album",
					"artist": "Solo Artist",
					"tracks": map[string]any{
						"track": map[string]any{
							"name":     "The Only Track",
							"duration": "245",
						},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	tracks, err := p.GetAlbumTracks(context.Background(), "Solo Artist::Single Track Album")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}

	if tracks[0].Title != "The Only Track" {
		t.Errorf("Title = %q", tracks[0].Title)
	}
	if tracks[0].ArtistName != "Solo Artist" {
		t.Errorf("ArtistName = %q", tracks[0].ArtistName)
	}
	if tracks[0].AlbumTitle != "Single Track Album" {
		t.Errorf("AlbumTitle = %q", tracks[0].AlbumTitle)
	}
	if tracks[0].TrackNumber != 1 {
		t.Errorf("TrackNumber = %d, want 1", tracks[0].TrackNumber)
	}
	// 245 seconds → 245000 ms.
	if tracks[0].DurationMs != 245000 {
		t.Errorf("DurationMs = %d, want 245000", tracks[0].DurationMs)
	}
}

func TestPlugin_GetAlbumTracks_MultiTrack(t *testing.T) {
	h := &lfmHandler{
		albumInfo: func(w http.ResponseWriter, artist, album string) {
			writeJSON(w, map[string]any{
				"album": map[string]any{
					"name":   "Discovery",
					"artist": "Daft Punk",
					"tracks": map[string]any{
						"track": []map[string]any{
							{"name": "One More Time", "duration": "320"},
							{"name": "Aerodynamic", "duration": "207"},
							{"name": "Digital Love", "duration": "298"},
						},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	tracks, err := p.GetAlbumTracks(context.Background(), "Daft Punk::Discovery")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3", len(tracks))
	}

	// Check track 1.
	if tracks[0].Title != "One More Time" {
		t.Errorf("Track[0].Title = %q", tracks[0].Title)
	}
	if tracks[0].DurationMs != 320000 {
		t.Errorf("Track[0].DurationMs = %d, want 320000", tracks[0].DurationMs)
	}

	// Check track 2.
	if tracks[1].Title != "Aerodynamic" {
		t.Errorf("Track[1].Title = %q", tracks[1].Title)
	}
	if tracks[1].TrackNumber != 2 {
		t.Errorf("Track[1].TrackNumber = %d, want 2", tracks[1].TrackNumber)
	}

	// Check track 3.
	if tracks[2].Title != "Digital Love" {
		t.Errorf("Track[2].Title = %q", tracks[2].Title)
	}
	if tracks[2].DurationMs != 298000 {
		t.Errorf("Track[2].DurationMs = %d, want 298000", tracks[2].DurationMs)
	}
}

func TestPlugin_GetAlbumTracks_InvalidID(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	// Missing "::" separator.
	_, err := p.GetAlbumTracks(context.Background(), "NoSeparator")
	if err == nil {
		t.Fatal("expected error for invalid album ID format")
	}
}

func TestPlugin_GetAlbumTracks_NilAlbum(t *testing.T) {
	h := &lfmHandler{
		albumInfo: func(w http.ResponseWriter, artist, album string) {
			// Return empty album data (simulating API returning no album).
			writeJSON(w, map[string]any{"album": map[string]any{}})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	tracks, err := p.GetAlbumTracks(context.Background(), "Unknown::Album")
	if err != nil {
		t.Fatalf("GetAlbumTracks should not error on empty album: %v", err)
	}
	if len(tracks) != 0 {
		t.Errorf("expected 0 tracks, got %d", len(tracks))
	}
}

// ─── SearchAlbums ──────────────────────────────────────────────────────────

func TestPlugin_SearchAlbums(t *testing.T) {
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": []map[string]any{
							{
								"name":  "Daft Punk",
								"mbid":  "056e4f3e-d505-4dad-8ec1-d04f521cbb56",
								"image": []any{},
							},
						},
					},
				},
			})
		},
		topAlbums: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"topalbums": map[string]any{
					"album": []map[string]any{
						{
							"name":      "Discovery",
							"mbid":      "album-mbid-1",
							"playcount": 5000,
							"image":     []any{},
						},
						{
							"name":      "Homework",
							"mbid":      "",
							"playcount": 3000,
							"image":     []any{},
						},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	albums, err := p.SearchAlbums(context.Background(), "Daft Punk", 10)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(albums) != 2 {
		t.Fatalf("got %d albums, want 2", len(albums))
	}

	if albums[0].Title != "Discovery" {
		t.Errorf("Title = %q, want Discovery", albums[0].Title)
	}
	if albums[0].ArtistName != "Daft Punk" {
		t.Errorf("ArtistName = %q, want Daft Punk", albums[0].ArtistName)
	}
	if albums[0].ProviderName != "lastfm" {
		t.Errorf("ProviderName = %q, want lastfm", albums[0].ProviderName)
	}

	// Second album has no MBID, should fall back to name with artist prefix.
	if albums[1].ProviderID != "Daft Punk::Homework" {
		t.Errorf("ProviderID = %q, want Daft Punk::Homework (name fallback)", albums[1].ProviderID)
	}
}

func TestPlugin_SearchAlbums_NoArtist(t *testing.T) {
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": "",
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	albums, err := p.SearchAlbums(context.Background(), "Nonexistent", 10)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(albums) != 0 {
		t.Errorf("expected 0 albums when no artist found, got %d", len(albums))
	}
}

// ─── Interface compliance check ────────────────────────────────────────────

func TestPlugin_ImplementsProvider(t *testing.T) {
	var _ discovery.Provider = (*Plugin)(nil)
}

func TestPlugin_ImplementsMetadataProvider(t *testing.T) {
	var _ metadata.Provider = (*Plugin)(nil)
}

// ─── metadata.Provider ─────────────────────────────────────────────────────

func TestPlugin_IsMetadataAvailable_WithKey(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	if !p.IsMetadataAvailable() {
		t.Error("IsMetadataAvailable should be true when api_key is set")
	}
}

func TestPlugin_IsMetadataAvailable_NoKey(t *testing.T) {
	p, cleanup := newTestPluginNoKey(t, nil)
	defer cleanup()

	if p.IsMetadataAvailable() {
		t.Error("IsMetadataAvailable should be false when api_key is empty")
	}
}

func TestPlugin_SearchArtistImage(t *testing.T) {
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": []map[string]any{
							{
								"name": "Daft Punk",
								"mbid": "056e4f3e-d505-4dad-8ec1-d04f521cbb56",
								"image": []map[string]any{
									{"#text": "https://lastfm.freetls.fastly.net/i/u/300x300/daft.jpg", "size": "large"},
								},
							},
						},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	result, err := p.SearchArtistImage(context.Background(), "Daft Punk")
	if err != nil {
		t.Fatalf("SearchArtistImage: %v", err)
	}
	if result == nil {
		t.Fatal("expected artist image result, got nil")
	}
	if result.ImageURL != "https://lastfm.freetls.fastly.net/i/u/300x300/daft.jpg" {
		t.Errorf("ImageURL = %q", result.ImageURL)
	}
	if result.Source != "lastfm" {
		t.Errorf("Source = %q, want lastfm", result.Source)
	}
}

func TestPlugin_SearchArtistImage_NoResults(t *testing.T) {
	h := &lfmHandler{
		artistSearch: func(w http.ResponseWriter, artist string, limit int) {
			writeJSON(w, map[string]any{
				"results": map[string]any{
					"artistmatches": map[string]any{
						"artist": "",
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	result, err := p.SearchArtistImage(context.Background(), "Nonexistent")
	if err != nil {
		t.Fatalf("SearchArtistImage: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for no matches")
	}
}

func TestPlugin_SearchCover(t *testing.T) {
	h := &lfmHandler{
		albumInfo: func(w http.ResponseWriter, artist, album string) {
			writeJSON(w, map[string]any{
				"album": map[string]any{
					"name":   "Discovery",
					"artist": "Daft Punk",
					"image": []map[string]any{
						{"#text": "https://example.com/discovery_large.jpg", "size": "large"},
					},
					"tracks": map[string]any{
						"track": []any{},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	result, err := p.SearchCover(context.Background(), "Daft Punk", "Discovery")
	if err != nil {
		t.Fatalf("SearchCover: %v", err)
	}
	if result == nil {
		t.Fatal("expected cover result, got nil")
	}
	if result.ImageURL != "https://example.com/discovery_large.jpg" {
		t.Errorf("ImageURL = %q", result.ImageURL)
	}
	if result.Source != "lastfm" {
		t.Errorf("Source = %q, want lastfm", result.Source)
	}
}

func TestPlugin_SearchCover_NoImage(t *testing.T) {
	h := &lfmHandler{
		albumInfo: func(w http.ResponseWriter, artist, album string) {
			writeJSON(w, map[string]any{
				"album": map[string]any{
					"name":   "Discovery",
					"artist": "Daft Punk",
					"image":  []any{},
					"tracks": map[string]any{
						"track": []any{},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	result, err := p.SearchCover(context.Background(), "Daft Punk", "Discovery")
	if err != nil {
		t.Fatalf("SearchCover: %v", err)
	}
	if result != nil {
		t.Error("expected nil result when album has no image")
	}
}

func TestPlugin_SearchAlbum(t *testing.T) {
	h := &lfmHandler{
		albumInfo: func(w http.ResponseWriter, artist, album string) {
			writeJSON(w, map[string]any{
				"album": map[string]any{
					"name":   "Random Access Memories",
					"artist": "Daft Punk",
					"tracks": map[string]any{
						"track": []any{},
					},
				},
			})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	album := p.SearchAlbum(context.Background(), "Daft Punk", "Get Lucky")
	if album != "Random Access Memories" {
		t.Errorf("SearchAlbum = %q, want Random Access Memories", album)
	}
}

func TestPlugin_SearchAlbum_NoResults(t *testing.T) {
	h := &lfmHandler{
		albumInfo: func(w http.ResponseWriter, artist, album string) {
			writeJSON(w, map[string]any{"album": map[string]any{}})
		},
	}
	p, cleanup := newTestPlugin(t, h)
	defer cleanup()

	album := p.SearchAlbum(context.Background(), "Unknown", "Track")
	if album != "" {
		t.Errorf("SearchAlbum = %q, want empty", album)
	}
}

func TestPlugin_EnrichTrack(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	md, err := p.EnrichTrack(context.Background(), &domain.Track{Title: "Get Lucky"})
	if err != nil {
		t.Fatalf("EnrichTrack: %v", err)
	}
	if md != nil {
		t.Error("expected nil metadata (Last.fm doesn't support enrichment)")
	}
}
