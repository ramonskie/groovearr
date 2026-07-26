package discogs

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopWriter{}, nil))
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// newTestPlugin creates a Plugin backed by a mock HTTP server.
func newTestPlugin(t *testing.T, handler http.HandlerFunc) (*Plugin, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)

	client := &Client{
		cfg:        DiscogsConfig{},
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

// ─── IsConfigured ──────────────────────────────────────────────────────────

func TestPlugin_IsConfigured(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	if !p.IsConfigured() {
		t.Error("IsConfigured should always return true for Discogs")
	}
}

// ─── CheckConnection ───────────────────────────────────────────────────────

func TestPlugin_CheckConnection_Success(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}})
	})
	p, cleanup := newTestPlugin(t, handler)
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

func TestPlugin_CapabilityStatus_Configured(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	status := p.CapabilityStatus()
	if status["metadata"] != "configured" {
		t.Errorf("metadata status = %q, want configured", status["metadata"])
	}
}

func TestPlugin_CapabilityStatus_Connected(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	_ = p.CheckConnection(context.Background())
	status := p.CapabilityStatus()
	if status["metadata"] != "connected" {
		t.Errorf("metadata status = %q, want connected", status["metadata"])
	}
}

// ─── SearchArtists ─────────────────────────────────────────────────────────

func TestPlugin_SearchArtists(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.URL.Query().Get("q") // read to avoid lint warning
		writeJSON(w, map[string]any{
			"results": []map[string]any{
				{
					"id":          123,
					"title":       "Daft Punk",
					"cover_image": "https://img.discogs.com/daft.jpg",
					"thumb":       "https://img.discogs.com/daft_thumb.jpg",
				},
				{
					"id":          456,
					"title":       "Daft Punk Tribute",
					"cover_image": "",
					"thumb":       "",
				},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	artists, err := p.SearchArtists(context.Background(), "Daft Punk", 10)
	if err != nil {
		t.Fatalf("SearchArtists: %v", err)
	}
	if len(artists) != 2 {
		t.Fatalf("got %d artists, want 2", len(artists))
	}

	if artists[0].ProviderID != "123" {
		t.Errorf("ProviderID = %q, want 123", artists[0].ProviderID)
	}
	if artists[0].Name != "Daft Punk" {
		t.Errorf("Name = %q, want Daft Punk", artists[0].Name)
	}
	if artists[0].ImageURL != "https://img.discogs.com/daft.jpg" {
		t.Errorf("ImageURL = %q, want daft.jpg", artists[0].ImageURL)
	}

	// Second artist has no image.
	if artists[1].ImageURL != "" {
		t.Errorf("ImageURL should be empty, got %q", artists[1].ImageURL)
	}
}

func TestPlugin_SearchArtists_Empty(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}})
	})
	p, cleanup := newTestPlugin(t, handler)
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
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"releases": []map[string]any{
				{
					"id":     1001,
					"title":  "Discovery",
					"year":   2001,
					"type":   "master",
					"thumb":  "https://img.discogs.com/discovery.jpg",
					"artist": "Daft Punk",
				},
				{
					"id":     1002,
					"title":  "Homework",
					"year":   1997,
					"type":   "master",
					"thumb":  "",
					"artist": "Daft Punk",
				},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	albums, err := p.GetArtistAlbums(context.Background(), "123", 10)
	if err != nil {
		t.Fatalf("GetArtistAlbums: %v", err)
	}
	if len(albums) != 2 {
		t.Fatalf("got %d albums, want 2", len(albums))
	}

	if albums[0].ProviderID != "1001" {
		t.Errorf("ProviderID = %q, want 1001", albums[0].ProviderID)
	}
	if albums[0].ProviderName != "discogs" {
		t.Errorf("ProviderName = %q, want discogs", albums[0].ProviderName)
	}
	if albums[0].Title != "Discovery" {
		t.Errorf("Title = %q, want Discovery", albums[0].Title)
	}
	if albums[0].Type != "album" {
		t.Errorf("Type = %q, want album (master→album)", albums[0].Type)
	}
	if albums[0].Year != 2001 {
		t.Errorf("Year = %d, want 2001", albums[0].Year)
	}
	if albums[0].ArtistName != "Daft Punk" {
		t.Errorf("ArtistName = %q, want Daft Punk", albums[0].ArtistName)
	}
	if albums[0].CoverURL != "https://img.discogs.com/discovery.jpg" {
		t.Errorf("CoverURL = %q", albums[0].CoverURL)
	}
}

func TestPlugin_GetArtistAlbums_InvalidID(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	_, err := p.GetArtistAlbums(context.Background(), "not-a-number", 10)
	if err == nil {
		t.Fatal("expected error for invalid artist ID")
	}
}

// ─── GetAlbumTracks ────────────────────────────────────────────────────────

func TestPlugin_GetAlbumTracks(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"title": "Discovery",
			"year":  2001,
			"artists": []map[string]any{
				{"name": "Daft Punk"},
			},
			"tracklist": []map[string]any{
				{"position": "1", "title": "One More Time", "duration": "5:20"},
				{"position": "2", "title": "Aerodynamic", "duration": "3:27"},
				{"position": "3", "title": "Digital Love", "duration": "4:58"},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	tracks, err := p.GetAlbumTracks(context.Background(), "1001")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3", len(tracks))
	}

	if tracks[0].ArtistName != "Daft Punk" {
		t.Errorf("ArtistName = %q, want Daft Punk", tracks[0].ArtistName)
	}
	if tracks[0].AlbumTitle != "Discovery" {
		t.Errorf("AlbumTitle = %q, want Discovery", tracks[0].AlbumTitle)
	}
	if tracks[0].Title != "One More Time" {
		t.Errorf("Title = %q, want One More Time", tracks[0].Title)
	}
	if tracks[0].TrackNumber != 1 {
		t.Errorf("TrackNumber = %d, want 1", tracks[0].TrackNumber)
	}
	if tracks[0].DiscNumber != 1 {
		t.Errorf("DiscNumber = %d, want 1", tracks[0].DiscNumber)
	}
	// "5:20" = (5*60+20)*1000 = 320000
	if tracks[0].DurationMs != 320000 {
		t.Errorf("DurationMs = %d, want 320000", tracks[0].DurationMs)
	}
}

func TestPlugin_GetAlbumTracks_ParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"4:32", 272000},
		{"", 0},
		{"1:00", 60000},
		{"0:30", 30000},
		{"10:15", 615000},
		{"invalid", 0},
	}
	for _, tt := range tests {
		got := parseDuration(tt.input)
		if got != tt.expected {
			t.Errorf("parseDuration(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestPlugin_GetAlbumTracks_NonNumericPosition(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"title":    "Vinyl Release",
			"artists":  []map[string]any{{"name": "Artist"}},
			"tracklist": []map[string]any{
				{"position": "A1", "title": "Side A Track 1", "duration": "3:00"},
				{"position": "B2", "title": "Side B Track 2", "duration": "4:00"},
				{"position": "", "title": "Empty Position", "duration": "2:00"},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	tracks, err := p.GetAlbumTracks(context.Background(), "2001")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3", len(tracks))
	}

	// "A1" is non-numeric → fallback to index+1 = 1.
	if tracks[0].TrackNumber != 1 {
		t.Errorf("Track[0] Number = %d, want 1 (fallback for A1)", tracks[0].TrackNumber)
	}
	// "B2" is non-numeric → fallback to index+1 = 2.
	if tracks[1].TrackNumber != 2 {
		t.Errorf("Track[1] Number = %d, want 2 (fallback for B2)", tracks[1].TrackNumber)
	}
	// "" is non-numeric → fallback to index+1 = 3.
	if tracks[2].TrackNumber != 3 {
		t.Errorf("Track[2] Number = %d, want 3 (fallback for empty)", tracks[2].TrackNumber)
	}
}

func TestPlugin_GetAlbumTracks_InvalidID(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	_, err := p.GetAlbumTracks(context.Background(), "not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid release ID")
	}
}

func TestPlugin_GetAlbumTracks_NilRelease(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	tracks, err := p.GetAlbumTracks(context.Background(), "999999")
	if err != nil {
		t.Fatalf("GetAlbumTracks should not error on 404: %v", err)
	}
	if tracks != nil {
		t.Errorf("expected nil tracks for not-found release, got %d", len(tracks))
	}
}

// ─── SearchAlbums ──────────────────────────────────────────────────────────

func TestPlugin_SearchAlbums(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"results": []map[string]any{
				{
					"id":     3001,
					"title":  "Random Access Memories",
					"year":   2013,
					"type":   "master",
					"thumb":  "https://img.discogs.com/ram.jpg",
					"artist": "Daft Punk",
				},
				{
					"id":     3002,
					"title":  "Random Access Memories (Deluxe)",
					"year":   2013,
					"type":   "release",
					"thumb":  "",
					"artist": "Daft Punk",
				},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	albums, err := p.SearchAlbums(context.Background(), "Random Access Memories", 10)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(albums) != 2 {
		t.Fatalf("got %d albums, want 2", len(albums))
	}

	if albums[0].Type != "album" {
		t.Errorf("master type should map to album, got %q", albums[0].Type)
	}
	if albums[1].Type != "release" {
		t.Errorf("release type should stay release, got %q", albums[1].Type)
	}
	if albums[0].Title != "Random Access Memories" {
		t.Errorf("Title = %q", albums[0].Title)
	}
}

func TestPlugin_SearchAlbums_Empty(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	albums, err := p.SearchAlbums(context.Background(), "Nonexistent", 10)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(albums) != 0 {
		t.Errorf("expected 0 albums, got %d", len(albums))
	}
}

// ─── Interface compliance check ────────────────────────────────────────────

func TestPlugin_ImplementsMetadataProvider(t *testing.T) {
	var _ metadata.Provider = (*Plugin)(nil)
}

// ─── metadata.Provider ──────────────────────────────────────────────────────

func TestPlugin_IsMetadataAvailable(t *testing.T) {
	p, cleanup := newTestPlugin(t, nil)
	defer cleanup()

	if !p.IsMetadataAvailable() {
		t.Error("IsMetadataAvailable should always return true for Discogs")
	}
}

func TestPlugin_SearchArtistImage(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"results": []map[string]any{
				{
					"id":          123,
					"title":       "Daft Punk",
					"cover_image": "https://img.discogs.com/daft.jpg",
					"thumb":       "https://img.discogs.com/daft_thumb.jpg",
				},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	result, err := p.SearchArtistImage(context.Background(), "Daft Punk")
	if err != nil {
		t.Fatalf("SearchArtistImage: %v", err)
	}
	if result == nil {
		t.Fatal("expected artist image result, got nil")
	}
	if result.ImageURL != "https://img.discogs.com/daft.jpg" {
		t.Errorf("ImageURL = %q", result.ImageURL)
	}
	if result.Source != "discogs" {
		t.Errorf("Source = %q, want discogs", result.Source)
	}
}

func TestPlugin_SearchArtistImage_NoResults(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	result, err := p.SearchArtistImage(context.Background(), "Nonexistent")
	if err != nil {
		t.Fatalf("SearchArtistImage: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for no matches")
	}
}

func TestPlugin_SearchArtistImage_NoImage(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"results": []map[string]any{
				{"id": 123, "title": "NoImage Artist", "cover_image": "", "thumb": ""},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	result, err := p.SearchArtistImage(context.Background(), "NoImage Artist")
	if err != nil {
		t.Fatalf("SearchArtistImage: %v", err)
	}
	if result != nil {
		t.Error("expected nil result when artist has no image")
	}
}

func TestPlugin_SearchCover(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"results": []map[string]any{
				{
					"id":     1001,
					"title":  "Discovery",
					"year":   2001,
					"thumb":  "https://img.discogs.com/discovery_thumb.jpg",
					"artist": "Daft Punk",
				},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	result, err := p.SearchCover(context.Background(), "Daft Punk", "Discovery")
	if err != nil {
		t.Fatalf("SearchCover: %v", err)
	}
	if result == nil {
		t.Fatal("expected cover result, got nil")
	}
	if result.ImageURL != "https://img.discogs.com/discovery_thumb.jpg" {
		t.Errorf("ImageURL = %q", result.ImageURL)
	}
	if result.Source != "discogs" {
		t.Errorf("Source = %q, want discogs", result.Source)
	}
}

func TestPlugin_SearchCover_NoResults(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	result, err := p.SearchCover(context.Background(), "Unknown", "Album")
	if err != nil {
		t.Fatalf("SearchCover: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for no matches")
	}
}

func TestPlugin_SearchAlbum(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"results": []map[string]any{
				{
					"id":     1001,
					"title":  "Random Access Memories",
					"thumb":  "",
					"artist": "Daft Punk",
				},
			},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	album := p.SearchAlbum(context.Background(), "Daft Punk", "Get Lucky")
	if album != "Random Access Memories" {
		t.Errorf("SearchAlbum = %q, want Random Access Memories", album)
	}
}

func TestPlugin_SearchAlbum_NoResults(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	album := p.SearchAlbum(context.Background(), "Unknown", "Track")
	if album != "" {
		t.Errorf("SearchAlbum = %q, want empty", album)
	}
}

func TestPlugin_EnrichTrack(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if _, ok := r.URL.Query()["q"]; ok && q != "" {
			// SearchAlbums request
			writeJSON(w, map[string]any{
				"results": []map[string]any{
					{
						"id":     5001,
						"title":  "Get Lucky",
						"year":   2013,
						"thumb":  "",
						"artist": "Daft Punk",
					},
				},
			})
			return
		}
		// GetRelease request
		writeJSON(w, map[string]any{
			"title":    "Get Lucky",
			"year":     2013,
			"artists":  []map[string]any{{"name": "Daft Punk"}},
			"tracklist": []any{},
		})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	md, err := p.EnrichTrack(context.Background(), &domain.Track{Title: "Get Lucky"})
	if err != nil {
		t.Fatalf("EnrichTrack: %v", err)
	}
	if md == nil {
		t.Fatal("expected metadata, got nil")
	}
	if md.ReleaseDate != "2013" {
		t.Errorf("ReleaseDate = %q, want 2013", md.ReleaseDate)
	}
}

func TestPlugin_EnrichTrack_NoMatch(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{}})
	})
	p, cleanup := newTestPlugin(t, handler)
	defer cleanup()

	md, err := p.EnrichTrack(context.Background(), &domain.Track{Title: "Unknown"})
	if err != nil {
		t.Fatalf("EnrichTrack: %v", err)
	}
	if md != nil {
		t.Error("expected nil metadata for no match")
	}
}

// ─── FlexInt ───────────────────────────────────────────────────────────────

func TestFlexInt_UnmarshalInt(t *testing.T) {
	var fi FlexInt
	if err := json.Unmarshal([]byte("2001"), &fi); err != nil {
		t.Fatalf("unmarshal int: %v", err)
	}
	if int(fi) != 2001 {
		t.Errorf("got %d, want 2001", fi)
	}
}

func TestFlexInt_UnmarshalString(t *testing.T) {
	var fi FlexInt
	if err := json.Unmarshal([]byte(`"2001"`), &fi); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if int(fi) != 2001 {
		t.Errorf("got %d, want 2001", fi)
	}
}

func TestFlexInt_UnmarshalInvalid(t *testing.T) {
	var fi FlexInt
	if err := json.Unmarshal([]byte("true"), &fi); err == nil {
		t.Fatal("expected error for bool value")
	}
}

// ─── Helpers ───────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}
