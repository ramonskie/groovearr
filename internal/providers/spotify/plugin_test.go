package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
)

// ─── Test helpers ─────────────────────────────────────────────────────

// hostRewriteClient returns an http.Client that rewrites requests to the
// given host so they land on the test server instead.
// Uses hostRewriteTransport defined in playlist_test.go.
func hostRewriteClient(serverURL string, targetHost string) *http.Client {
	return &http.Client{
		Transport: &hostRewriteTransport{
			targetHost: targetHost,
			serverURL:  serverURL,
		},
	}
}

// urlRewriteClient returns an http.Client that rewrites all URLs to the
// test server. Uses urlRewriteTransport defined in api_test.go.
func urlRewriteClientAny(serverURL string) *http.Client {
	return &http.Client{
		Transport: &urlRewriteTransport{serverURL: serverURL},
	}
}

// newFreePlugin creates a Plugin in free mode with a mock oembed server.
func newFreePlugin(t *testing.T) (*Plugin, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case path == "/oembed" && r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case path == "/oembed" && r.Method == http.MethodGet:
			resp := OEmbedResponse{
				HTML:         `<iframe src="https://open.spotify.com/embed/track/abc123" title="Test Track"></iframe>`,
				Title:        "Test Track",
				Version:      "1.0",
				ProviderName: "Spotify",
				ProviderURL:  "https://spotify.com",
				Type:         "rich",
			}
			cover := "https://i.scdn.co/image/abc123"
			resp.ThumbnailURL = &cover
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	client := hostRewriteClient(server.URL, "open.spotify.com")

	p := &Plugin{
		cfg:          &SpotifyConfig{Mode: "free"},
		dlPath:       "/tmp",
		oembedClient: client,
	}

	return p, server
}

// newDevPlugin creates a Plugin in dev mode with a mock Spotify API server.
func newDevPlugin(t *testing.T, handler http.HandlerFunc) (*Plugin, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(handler)

	cfg := &SpotifyConfig{
		Mode:         "dev",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Tokens:       SpotifyTokens{AccessToken: "test-access-token", ExpiresAt: time.Now().Add(1 * time.Hour).Unix()},
	}

	client := &SpotifyClient{
		cfg: cfg,
		http: &http.Client{
			Transport: &authTransport{
				cfg:       cfg,
				transport: &urlRewriteTransport{serverURL: server.URL},
				refreshFunc: func(ctx context.Context, refreshToken, clientID string) (string, int, error) {
					return "", 0, errors.New("no refresh in test")
				},
			},
			Timeout: defaultTimeout,
		},
	}

	p := &Plugin{
		cfg:    cfg,
		dlPath: "/tmp",
		client: client,
		api:    NewAPI(client),
	}

	return p, server
}

// ─── Search ───────────────────────────────────────────────────────────

func TestPlugin_Search_FreeMode_ValidURL(t *testing.T) {
	p, server := newFreePlugin(t)
	defer server.Close()

	ctx := context.Background()
	tracks, albums, err := p.Search(ctx, "https://open.spotify.com/track/abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}
	if tracks[0].Title != "Test Track" {
		t.Errorf("Title = %q, want %q", tracks[0].Title, "Test Track")
	}
	if tracks[0].CoverURL != "https://i.scdn.co/image/abc123" {
		t.Errorf("CoverURL = %q, want %q", tracks[0].CoverURL, "https://i.scdn.co/image/abc123")
	}
	if tracks[0].Username != "spotify" {
		t.Errorf("Username = %q, want %q", tracks[0].Username, "spotify")
	}
	if len(albums) != 0 {
		t.Errorf("expected 0 albums in free mode, got %d", len(albums))
	}
}

func TestPlugin_Search_FreeMode_InvalidURL(t *testing.T) {
	p, server := newFreePlugin(t)
	defer server.Close()

	ctx := context.Background()
	tracks, _, err := p.Search(ctx, "not a spotify url")
	if err != nil {
		t.Fatalf("unexpected error for free-text query: %v", err)
	}
	if tracks != nil {
		t.Errorf("expected nil tracks for non-URL query, got %d", len(tracks))
	}
}

func TestPlugin_Search_FreeMode_EmptyURL(t *testing.T) {
	p, _ := newFreePlugin(t)

	ctx := context.Background()
	tracks, _, err := p.Search(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error for empty query: %v", err)
	}
	if tracks != nil {
		t.Errorf("expected nil tracks for empty query, got %d", len(tracks))
	}
}

func TestPlugin_Search_DevMode_Success(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/search" && r.URL.Query().Get("type") == "track":
			writeJSON(w, map[string]any{
				"tracks": map[string]any{
					"href":   "https://api.spotify.com/v1/search?type=track&q=test",
					"limit":  50,
					"offset": 0,
					"total":  1,
					"items": []map[string]any{{
						"id":           "track123",
						"name":         "Test Track",
						"duration_ms":  200000,
						"track_number": 1,
						"disc_number":  1,
						"popularity":   80,
						"href":         "https://api.spotify.com/v1/tracks/track123",
						"uri":          "spotify:track:track123",
						"album": map[string]any{
							"id":           "album123",
							"name":         "Test Album",
							"release_date": "2024-01-15",
							"total_tracks": 10,
							"images":        []map[string]any{{"url": "https://i.scdn.co/cover.jpg"}},
							"artists":       []map[string]any{{"name": "Test Artist"}},
						},
						"artists": []map[string]any{{"name": "Test Artist"}},
					}},
				},
			})
		case r.URL.Path == "/v1/search" && r.URL.Query().Get("type") == "album":
			writeJSON(w, map[string]any{
				"albums": map[string]any{
					"href":   "https://api.spotify.com/v1/search?type=album&q=test",
					"limit":  20,
					"offset": 0,
					"total":  1,
					"items": []map[string]any{{
						"id":           "album123",
						"name":         "Test Album",
						"release_date": "2024-01-15",
						"total_tracks": 10,
						"images":        []map[string]any{{"url": "https://i.scdn.co/cover.jpg"}},
						"artists":       []map[string]any{{"name": "Test Artist"}},
					}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	tracks, albums, err := p.Search(ctx, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}
	if tracks[0].Title != "Test Track" {
		t.Errorf("Title = %q, want %q", tracks[0].Title, "Test Track")
	}
	if tracks[0].Artist != "Test Artist" {
		t.Errorf("Artist = %q, want %q", tracks[0].Artist, "Test Artist")
	}
	if tracks[0].Album != "Test Album" {
		t.Errorf("Album = %q, want %q", tracks[0].Album, "Test Album")
	}
	if tracks[0].CoverURL != "https://i.scdn.co/cover.jpg" {
		t.Errorf("CoverURL = %q", tracks[0].CoverURL)
	}
	if tracks[0].Duration != 200000 {
		t.Errorf("Duration = %d, want 200000", tracks[0].Duration)
	}

	if len(albums) != 1 {
		t.Fatalf("expected 1 album, got %d", len(albums))
	}
	if albums[0].AlbumTitle != "Test Album" {
		t.Errorf("AlbumTitle = %q", albums[0].AlbumTitle)
	}
	if albums[0].Artist != "Test Artist" {
		t.Errorf("Album Artist = %q", albums[0].Artist)
	}
	if albums[0].Year != "2024" {
		t.Errorf("Year = %q, want %q", albums[0].Year, "2024")
	}
}

func TestPlugin_Search_DevMode_AlbumSearchNonFatal(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/search" && r.URL.Query().Get("type") == "track" {
			writeJSON(w, map[string]any{
				"tracks": map[string]any{
					"items": []map[string]any{{
						"id":           "track123",
						"name":         "Test Track",
						"duration_ms":  200000,
						"track_number": 1,
						"disc_number":  1,
						"href":         "https://api.spotify.com/v1/tracks/track123",
						"uri":          "spotify:track:track123",
						"album": map[string]any{
							"id":           "album123",
							"name":         "Test Album",
							"release_date": "2024-01-15",
							"total_tracks": 10,
							"images":        []map[string]any{{"url": "https://i.scdn.co/cover.jpg"}},
							"artists":       []map[string]any{{"name": "Test Artist"}},
						},
						"artists": []map[string]any{{"name": "Test Artist"}},
					}},
					"total": 1,
				},
			})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tracks, albums, err := p.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tracks) != 1 {
		t.Errorf("expected 1 track, got %d", len(tracks))
	}
	if len(albums) != 0 {
		t.Errorf("expected 0 albums on error, got %d", len(albums))
	}
}

// ─── CheckConnection ──────────────────────────────────────────────────

func TestPlugin_CheckConnection_Free_Reachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := hostRewriteClient(server.URL, "open.spotify.com")
	p := &Plugin{
		cfg:          &SpotifyConfig{Mode: "free"},
		oembedClient: client,
	}

	err := p.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("expected nil error (reachable), got: %v", err)
	}
	if !p.Connected() {
		t.Error("expected Connected() to return true after successful check")
	}
}

func TestPlugin_CheckConnection_Free_Unreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := server.Listener.Addr().String()
	server.Close()

	client := hostRewriteClient("http://"+addr, "open.spotify.com")
	p := &Plugin{
		cfg:          &SpotifyConfig{Mode: "free"},
		oembedClient: client,
	}

	err := p.CheckConnection(context.Background())
	if err == nil {
		t.Fatal("expected error (unreachable), got nil")
	}
	if p.Connected() {
		t.Error("expected Connected() to return false after failed check")
	}
}

func TestPlugin_CheckConnection_Dev_OK(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"id": "user123"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	err := p.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if !p.Connected() {
		t.Error("expected Connected() to return true")
	}
}

func TestPlugin_CheckConnection_Dev_Unauthorized(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	err := p.CheckConnection(context.Background())
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if p.Connected() {
		t.Error("expected Connected() to return false after 401")
	}
}

func TestPlugin_CheckConnection_Dev_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := server.Listener.Addr().String()
	server.Close()

	cfg := &SpotifyConfig{
		Mode:   "dev",
		Tokens: SpotifyTokens{AccessToken: "test-token"},
	}
	client := &SpotifyClient{
		cfg: cfg,
		http: &http.Client{
			Transport: &authTransport{
				cfg:       cfg,
				transport: &urlRewriteTransport{serverURL: "http://" + addr},
				refreshFunc: func(ctx context.Context, refreshToken, clientID string) (string, int, error) {
					return "", 0, errors.New("no refresh in test")
				},
			},
			Timeout: defaultTimeout,
		},
	}
	p := &Plugin{
		cfg:    cfg,
		client: client,
		api:    NewAPI(client),
	}

	err := p.CheckConnection(context.Background())
	if err == nil {
		t.Fatal("expected network error, got nil")
	}
	if p.Connected() {
		t.Error("expected Connected() to be false after network error")
	}
}

// ─── Connected / IsConfigured ─────────────────────────────────────────

func TestPlugin_Connected_StateTransitions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := hostRewriteClient(server.URL, "open.spotify.com")
	p := &Plugin{
		cfg:          &SpotifyConfig{Mode: "free"},
		oembedClient: client,
	}

	if p.Connected() {
		t.Error("expected Connected() to be false before first check")
	}

	if err := p.CheckConnection(context.Background()); err != nil {
		t.Fatalf("CheckConnection failed: %v", err)
	}
	if !p.Connected() {
		t.Error("expected Connected() to be true after successful check")
	}
}

func TestPlugin_IsConfigured_FreeMode(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "free"}}
	if !p.IsConfigured() {
		t.Error("free mode should always be configured")
	}
}

func TestPlugin_IsConfigured_DevMode_NoToken(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "dev"}}
	if p.IsConfigured() {
		t.Error("dev mode without token should not be configured")
	}
}

func TestPlugin_IsConfigured_DevMode_WithCredentials(t *testing.T) {
	p := &Plugin{
		cfg: &SpotifyConfig{
			Mode:         "dev",
			ClientID:     "test-id",
			ClientSecret: "test-secret",
			Tokens: SpotifyTokens{
				AccessToken: "valid-token",
				ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
			},
		},
	}
	if !p.IsConfigured() {
		t.Error("dev mode with credentials should be configured")
	}
}

func TestPlugin_IsConfigured_DevMode_MissingCredentials(t *testing.T) {
	p := &Plugin{
		cfg: &SpotifyConfig{
			Mode: "dev",
			Tokens: SpotifyTokens{
				AccessToken: "expired-token",
				ExpiresAt:   time.Now().Add(-1 * time.Hour).Unix(),
			},
		},
	}
	if p.IsConfigured() {
		t.Error("dev mode without client_id and client_secret should not be configured")
	}
}

func TestPlugin_IsConfigured_DevMode_NoCredentials(t *testing.T) {
	p := &Plugin{
		cfg: &SpotifyConfig{
			Mode: "dev",
			Tokens: SpotifyTokens{
				AccessToken: "token-with-no-expiry",
				ExpiresAt:   0,
			},
		},
	}
	if p.IsConfigured() {
		t.Error("dev mode without client_id and client_secret should not be configured")
	}
}

// ─── Download ─────────────────────────────────────────────────────────

func TestPlugin_Download_Error(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "free"}}
	id, err := p.Download(context.Background(), "user", "file", 1024)
	if id != "" {
		t.Errorf("expected empty download ID, got %q", id)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "downloading") {
		t.Errorf("error = %q, should mention 'downloading'", err.Error())
	}
}

// ─── Download tracking (no-ops) ───────────────────────────────────────

func TestPlugin_GetDownloads_Empty(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "free"}}
	records, err := p.GetDownloads(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 downloads, got %d", len(records))
	}
}

func TestPlugin_GetDownloadStatus_NotFound(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "free"}}
	rec, err := p.GetDownloadStatus(context.Background(), "any-id")
	if rec != nil {
		t.Error("expected nil record")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no downloads") {
		t.Errorf("error = %q, should mention 'no downloads'", err.Error())
	}
}

func TestPlugin_CancelDownload_Noop(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "free"}}
	if err := p.CancelDownload(context.Background(), "any-id", true); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestPlugin_ClearCompleted_Noop(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "free"}}
	if err := p.ClearCompleted(context.Background()); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

// ─── Name / DisplayName ───────────────────────────────────────────────

func TestPlugin_Name(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "free"}}
	if p.Name() != "spotify" {
		t.Errorf("Name() = %q, want %q", p.Name(), "spotify")
	}
}

func TestPlugin_DisplayName(t *testing.T) {
	p := &Plugin{cfg: &SpotifyConfig{Mode: "free"}}
	if p.DisplayName() != "Spotify" {
		t.Errorf("DisplayName() = %q, want %q", p.DisplayName(), "Spotify")
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func TestJoinArtists(t *testing.T) {
	tests := []struct {
		name     string
		artists  []SimplifiedArtist
		expected string
	}{
		{"empty", nil, ""},
		{"single", []SimplifiedArtist{{Name: "A"}}, "A"},
		{"two", []SimplifiedArtist{{Name: "A"}, {Name: "B"}}, "A, B"},
		{"three", []SimplifiedArtist{{Name: "A"}, {Name: "B"}, {Name: "C"}}, "A, B, C"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinArtists(tt.artists)
			if got != tt.expected {
				t.Errorf("joinArtists() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestDomainMapping(t *testing.T) {
	t.Run("trackToResult", func(t *testing.T) {
		trk := &Track{
			ID:         "trk1",
			Name:       "Bohemian Rhapsody",
			DurationMs: 354000,
			Album: SimplifiedAlbum{
				Name:        "A Night at the Opera",
				TotalTracks: 12,
				Images:      []Image{{URL: "https://cover.jpg"}},
				Artists:     []SimplifiedArtist{{Name: "Queen"}},
			},
			Artists:     []SimplifiedArtist{{Name: "Queen"}},
			TrackNumber: 11,
		}
		result := trackToResult(trk)
		if result.Title != "Bohemian Rhapsody" {
			t.Errorf("Title = %q", result.Title)
		}
		if result.Artist != "Queen" {
			t.Errorf("Artist = %q", result.Artist)
		}
		if result.Album != "A Night at the Opera" {
			t.Errorf("Album = %q", result.Album)
		}
		if result.Duration != 354000 {
			t.Errorf("Duration = %d", result.Duration)
		}
		if result.TrackNumber != 11 {
			t.Errorf("TrackNumber = %d", result.TrackNumber)
		}
		if result.CoverURL != "https://cover.jpg" {
			t.Errorf("CoverURL = %q", result.CoverURL)
		}
		if result.Username != "spotify" {
			t.Errorf("Username = %q", result.Username)
		}
		if result.Filename != "spotify://track/trk1" {
			t.Errorf("Filename = %q", result.Filename)
		}
	})

	t.Run("albumToResult", func(t *testing.T) {
		album := &SimplifiedAlbum{
			ID:          "alb1",
			Name:        "Dark Side of the Moon",
			TotalTracks: 10,
			ReleaseDate: "1973-03-01",
			Artists:     []SimplifiedArtist{{Name: "Pink Floyd"}},
		}
		result := albumToResult(album)
		if result.AlbumTitle != "Dark Side of the Moon" {
			t.Errorf("AlbumTitle = %q", result.AlbumTitle)
		}
		if result.Artist != "Pink Floyd" {
			t.Errorf("Artist = %q", result.Artist)
		}
		if result.TrackCount != 10 {
			t.Errorf("TrackCount = %d", result.TrackCount)
		}
		if result.Year != "1973" {
			t.Errorf("Year = %q", result.Year)
		}
		if result.Username != "spotify" {
			t.Errorf("Username = %q", result.Username)
		}
	})

	t.Run("albumYearShort", func(t *testing.T) {
		album := &SimplifiedAlbum{
			ID:          "alb2",
			Name:        "EP",
			TotalTracks: 5,
			ReleaseDate: "2024",
			Artists:     []SimplifiedArtist{{Name: "Artist"}},
		}
		result := albumToResult(album)
		if result.Year != "2024" {
			t.Errorf("Year = %q", result.Year)
		}
	})

	t.Run("trackToResult_noCover", func(t *testing.T) {
		trk := &Track{
			ID:         "trk2",
			Name:       "Track",
			DurationMs: 1000,
			Album: SimplifiedAlbum{
				Name:    "Album",
				Artists: []SimplifiedArtist{{Name: "Artist"}},
			},
			Artists: []SimplifiedArtist{{Name: "Artist"}},
		}
		result := trackToResult(trk)
		if result.CoverURL != "" {
			t.Errorf("expected empty CoverURL, got %q", result.CoverURL)
		}
	})
}

// Ensure Plugin satisfies download.Plugin at compile time.
func TestPlugin_ImplementsInterface(t *testing.T) {
	var _ downloadPlugin = (*Plugin)(nil)
	// If this compiles, Plugin implements all required methods.
}

// downloadPlugin mirrors the methods required by download.Plugin for
// compile-time assertion.
type downloadPlugin interface {
	Name() string
	DisplayName() string
	IsConfigured() bool
	CheckConnection(ctx context.Context) error
	Connected() bool
	Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error)
	Download(ctx context.Context, username, filename string, fileSize int64) (string, error)
	GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error)
	GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error)
	CancelDownload(ctx context.Context, downloadID string, remove bool) error
	ClearCompleted(ctx context.Context) error
}
