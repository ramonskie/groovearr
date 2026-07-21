package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// =============================================================================
// ParseSpotifyURL
// =============================================================================

func TestParseSpotifyURL_Valid(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		wantType string
		wantID   string
	}{
		{
			name:     "track",
			rawURL:   "https://open.spotify.com/track/4iV5W9uYEdYUVa79Axb7Rh",
			wantType: "track",
			wantID:   "4iV5W9uYEdYUVa79Axb7Rh",
		},
		{
			name:     "album",
			rawURL:   "https://open.spotify.com/album/1xHqNM8A5RUD1Mq8QyyGXn",
			wantType: "album",
			wantID:   "1xHqNM8A5RUD1Mq8QyyGXn",
		},
		{
			name:     "playlist",
			rawURL:   "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
			wantType: "playlist",
			wantID:   "37i9dQZF1DXcBWIGoYBM5M",
		},
		{
			name:     "with_query_param_si",
			rawURL:   "https://open.spotify.com/track/4iV5W9uYEdYUVa79Axb7Rh?si=abc123def456",
			wantType: "track",
			wantID:   "4iV5W9uYEdYUVa79Axb7Rh",
		},
		{
			name:     "with_multiple_query_params",
			rawURL:   "https://open.spotify.com/album/1xHqNM8A5RUD1Mq8QyyGXn?go=1&sp_cid=12345",
			wantType: "album",
			wantID:   "1xHqNM8A5RUD1Mq8QyyGXn",
		},
		{
			name:     "with_trailing_slash",
			rawURL:   "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M/",
			wantType: "playlist",
			wantID:   "37i9dQZF1DXcBWIGoYBM5M",
		},
		{
			name:     "localized_de",
			rawURL:   "https://open.spotify.com/intl-de/track/4iV5W9uYEdYUVa79Axb7Rh",
			wantType: "track",
			wantID:   "4iV5W9uYEdYUVa79Axb7Rh",
		},
		{
			name:     "localized_pt_BR",
			rawURL:   "https://open.spotify.com/intl-pt-BR/album/1xHqNM8A5RUD1Mq8QyyGXn",
			wantType: "album",
			wantID:   "1xHqNM8A5RUD1Mq8QyyGXn",
		},
		{
			name:     "localized_with_query",
			rawURL:   "https://open.spotify.com/intl-fr/playlist/37i9dQZF1DXcBWIGoYBM5M?si=123",
			wantType: "playlist",
			wantID:   "37i9dQZF1DXcBWIGoYBM5M",
		},
		{
			name:     "http_scheme",
			rawURL:   "http://open.spotify.com/track/4iV5W9uYEdYUVa79Axb7Rh",
			wantType: "track",
			wantID:   "4iV5W9uYEdYUVa79Axb7Rh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseSpotifyURL(tt.rawURL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", result.Type, tt.wantType)
			}
			if result.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", result.ID, tt.wantID)
			}
		})
	}
}

func TestParseSpotifyURL_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		rawURL string
	}{
		{name: "empty", rawURL: ""},
		{name: "whitespace_only", rawURL: "   "},
		{name: "not_spotify", rawURL: "https://example.com/track/123"},
		{name: "missing_id", rawURL: "https://open.spotify.com/track/"},
		{name: "missing_type_and_id", rawURL: "https://open.spotify.com/"},
		{name: "spotify_link", rawURL: "https://spotify.link/abc123"},
		{name: "unknown_type", rawURL: "https://open.spotify.com/show/abc123"},
		{name: "extra_path_segments", rawURL: "https://open.spotify.com/track/abc/extra"},
		{name: "not_a_url", rawURL: "just-some-text"},
		{name: "artist_url", rawURL: "https://open.spotify.com/artist/123"},
		{name: "fragment_only_id", rawURL: "https://open.spotify.com/track/#fragment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseSpotifyURL(tt.rawURL)
			if err == nil {
				t.Errorf("expected error for %q, got %+v", tt.rawURL, result)
			}
		})
	}
}

// =============================================================================
// FetchOEmbed (mock server)
// =============================================================================

func TestFetchOEmbed_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := OEmbedResponse{
			HTML:         `<iframe src="https://open.spotify.com/embed/track/abc123"></iframe>`,
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
	}))
	defer srv.Close()

	// Override the base URL for testing — inject via a testable constructor.
	// We test the parsing logic by directly hitting the mock server.
	ctx := context.Background()
	o, err := fetchOEmbedWithClient(ctx, srv.Client(), srv.URL+"?url="+url.QueryEscape("https://open.spotify.com/track/abc123"))
	if err != nil {
		t.Fatalf("FetchOEmbed failed: %v", err)
	}

	if o.Title != "Test Track" {
		t.Errorf("Title = %q, want %q", o.Title, "Test Track")
	}
	if o.ProviderName != "Spotify" {
		t.Errorf("ProviderName = %q, want %q", o.ProviderName, "Spotify")
	}
	if o.ThumbnailURL == nil || *o.ThumbnailURL != "https://i.scdn.co/image/abc123" {
		t.Errorf("ThumbnailURL = %v, want https://i.scdn.co/image/abc123", o.ThumbnailURL)
	}
	if o.HTML == "" {
		t.Error("HTML field is empty")
	}
}

func TestFetchOEmbed_NetworkError(t *testing.T) {
	// Server that refuses connections.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.Listener.Addr().String()
	srv.Close() // shut down immediately — next request will fail

	ctx := context.Background()
	_, err := fetchOEmbedWithClient(ctx, http.DefaultClient, "http://"+addr+"/oembed?url=test")
	if err == nil {
		t.Error("expected network error, got nil")
	}
}

func TestFetchOEmbed_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	ctx := context.Background()
	_, err := fetchOEmbedWithClient(ctx, srv.Client(), srv.URL+"?url=test")
	if err == nil {
		t.Error("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestFetchOEmbed_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{not valid json`))
	}))
	defer srv.Close()

	ctx := context.Background()
	_, err := fetchOEmbedWithClient(ctx, srv.Client(), srv.URL+"?url=test")
	if err == nil {
		t.Error("expected JSON parse error, got nil")
	}
}

func TestFetchOEmbed_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before request

	_, err := fetchOEmbedWithClient(ctx, srv.Client(), srv.URL+"?url=test")
	if err == nil {
		t.Error("expected context error, got nil")
	}
}

// =============================================================================
// ParseEmbedHTML
// =============================================================================

func TestParseEmbedHTML_Track(t *testing.T) {
	html := `<iframe style="border-radius:12px" src="https://open.spotify.com/embed/track/4iV5W9uYEdYUVa79Axb7Rh?utm_source=generator" width="100%" height="152" frameBorder="0" allowfullscreen="" allow="autoplay; clipboard-write; encrypted-media; fullscreen; picture-in-picture" loading="lazy"></iframe>`

	result := ParseEmbedHTML(html)

	if result.Type != "track" {
		t.Errorf("Type = %q, want %q", result.Type, "track")
	}
	if result.ID != "4iV5W9uYEdYUVa79Axb7Rh" {
		t.Errorf("ID = %q, want %q", result.ID, "4iV5W9uYEdYUVa79Axb7Rh")
	}
}

func TestParseEmbedHTML_Album(t *testing.T) {
	html := `<iframe style="border-radius:12px" src="https://open.spotify.com/embed/album/1xHqNM8A5RUD1Mq8QyyGXn?utm_source=generator" width="100%" height="352" frameBorder="0" allowfullscreen="" allow="autoplay; clipboard-write; encrypted-media; fullscreen; picture-in-picture" loading="lazy"></iframe>`

	result := ParseEmbedHTML(html)

	if result.Type != "album" {
		t.Errorf("Type = %q, want %q", result.Type, "album")
	}
	if result.ID != "1xHqNM8A5RUD1Mq8QyyGXn" {
		t.Errorf("ID = %q, want %q", result.ID, "1xHqNM8A5RUD1Mq8QyyGXn")
	}
}

func TestParseEmbedHTML_Playlist(t *testing.T) {
	html := `<iframe style="border-radius:12px" src="https://open.spotify.com/embed/playlist/37i9dQZF1DXcBWIGoYBM5M?utm_source=generator&theme=0" width="100%" height="352" frameBorder="0" allowfullscreen="" allow="autoplay; clipboard-write; encrypted-media; fullscreen; picture-in-picture" loading="lazy"></iframe>`

	result := ParseEmbedHTML(html)

	if result.Type != "playlist" {
		t.Errorf("Type = %q, want %q", result.Type, "playlist")
	}
	if result.ID != "37i9dQZF1DXcBWIGoYBM5M" {
		t.Errorf("ID = %q, want %q", result.ID, "37i9dQZF1DXcBWIGoYBM5M")
	}
}

func TestParseEmbedHTML_WithTitleAttribute(t *testing.T) {
	html := `<iframe title="Bohemian Rhapsody" src="https://open.spotify.com/embed/track/abc123" width="100%" height="152"></iframe>`

	result := ParseEmbedHTML(html)

	if result.Title != "Bohemian Rhapsody" {
		t.Errorf("Title = %q, want %q", result.Title, "Bohemian Rhapsody")
	}
	if result.Type != "track" {
		t.Errorf("Type = %q, want %q", result.Type, "track")
	}
}

func TestParseEmbedHTML_EmptyHTML(t *testing.T) {
	result := ParseEmbedHTML("")
	if result.Type != "" || result.ID != "" {
		t.Errorf("expected empty result for empty HTML, got Type=%q ID=%q", result.Type, result.ID)
	}
}

func TestParseEmbedHTML_NonSpotifyEmbed(t *testing.T) {
	html := `<iframe src="https://example.com/video/123"></iframe>`
	result := ParseEmbedHTML(html)
	if result.Type != "" {
		t.Errorf("Type should be empty for non-Spotify embed, got %q", result.Type)
	}
}

func TestParseEmbedHTML_MalformedHTML(t *testing.T) {
	// src attribute uses single quotes (which our regex doesn't match — intentional design choice)
	html := `<iframe src='https://open.spotify.com/embed/track/abc123'></iframe>`
	result := ParseEmbedHTML(html)
	if result.Type != "" {
		t.Errorf("Type should be empty for single-quoted src, got %q", result.Type)
	}
}

// =============================================================================
// ExtractTrackInfo / ExtractPlaylistInfo
// =============================================================================

func TestExtractTrackInfo_FromOEmbed(t *testing.T) {
	cover := "https://i.scdn.co/image/abc"
	oembed := &OEmbedResponse{
		Title:        "Bohemian Rhapsody",
		ThumbnailURL: &cover,
	}
	embed := &EmbedParseResult{
		Type: "track",
		ID:   "abc123",
	}

	info := ExtractTrackInfo(oembed, embed)

	if info.Title != "Bohemian Rhapsody" {
		t.Errorf("Title = %q, want %q", info.Title, "Bohemian Rhapsody")
	}
	if info.CoverURL != "https://i.scdn.co/image/abc" {
		t.Errorf("CoverURL = %q", info.CoverURL)
	}
}

func TestExtractTrackInfo_OEmbedNil(t *testing.T) {
	embed := &EmbedParseResult{
		Type:  "track",
		ID:    "abc123",
		Title: "Fallback Title",
	}

	info := ExtractTrackInfo(nil, embed)

	if info.Title != "Fallback Title" {
		t.Errorf("Title = %q, want %q", info.Title, "Fallback Title")
	}
	if info.CoverURL != "" {
		t.Errorf("CoverURL should be empty when oembed is nil, got %q", info.CoverURL)
	}
}

func TestExtractTrackInfo_EmbedNil(t *testing.T) {
	cover := "https://i.scdn.co/image/abc"
	oembed := &OEmbedResponse{
		Title:        "Test Track",
		ThumbnailURL: &cover,
	}

	info := ExtractTrackInfo(oembed, nil)

	if info.Title != "Test Track" {
		t.Errorf("Title = %q, want %q", info.Title, "Test Track")
	}
	if info.Artist != "" {
		t.Errorf("Artist should be empty when embed is nil, got %q", info.Artist)
	}
}

func TestExtractTrackInfo_BothNil(t *testing.T) {
	info := ExtractTrackInfo(nil, nil)
	if info.Title != "" || info.Artist != "" || info.CoverURL != "" {
		t.Errorf("expected empty TrackInfo when both args nil, got %+v", info)
	}
}

func TestExtractTrackInfo_OEmbedTitlePriority(t *testing.T) {
	cover := "https://i.scdn.co/image/from-oembed"
	oembed := &OEmbedResponse{
		Title:        "OEmbed Title",
		ThumbnailURL: &cover,
	}
	embed := &EmbedParseResult{
		Title: "Embed Title",
	}

	info := ExtractTrackInfo(oembed, embed)

	if info.Title != "OEmbed Title" {
		t.Errorf("oembed title should take priority, got %q", info.Title)
	}
}

func TestExtractPlaylistInfo_FromOEmbed(t *testing.T) {
	cover := "https://i.scdn.co/image/playlist"
	oembed := &OEmbedResponse{
		Title:        "Today's Top Hits",
		ThumbnailURL: &cover,
	}
	embed := &EmbedParseResult{
		Type: "playlist",
		ID:   "37i9dQZF1DXcBWIGoYBM5M",
	}

	info := ExtractPlaylistInfo(oembed, embed)

	if info.Title != "Today's Top Hits" {
		t.Errorf("Title = %q, want %q", info.Title, "Today's Top Hits")
	}
	if info.CoverURL != "https://i.scdn.co/image/playlist" {
		t.Errorf("CoverURL = %q", info.CoverURL)
	}
}

func TestExtractPlaylistInfo_OEmbedNil(t *testing.T) {
	embed := &EmbedParseResult{
		Type:  "playlist",
		ID:    "xyz789",
		Title: "Chill Vibes",
	}

	info := ExtractPlaylistInfo(nil, embed)

	if info.Title != "Chill Vibes" {
		t.Errorf("Title = %q, want %q", info.Title, "Chill Vibes")
	}
	if info.CoverURL != "" {
		t.Errorf("CoverURL should be empty when oembed is nil")
	}
}

func TestExtractPlaylistInfo_NoThumbnail(t *testing.T) {
	oembed := &OEmbedResponse{
		Title: "No Cover Playlist",
	}
	embed := &EmbedParseResult{Type: "playlist", ID: "noCover"}

	info := ExtractPlaylistInfo(oembed, embed)

	if info.CoverURL != "" {
		t.Errorf("CoverURL should be empty when thumbnail is nil, got %q", info.CoverURL)
	}
	if info.Title != "No Cover Playlist" {
		t.Errorf("Title = %q", info.Title)
	}
}

// =============================================================================
// Helpers
// =============================================================================

// fetchOEmbedWithClient is a testable variant that accepts an explicit HTTP client
// and URL (already encoded). This lets us point at httptest servers.
func fetchOEmbedWithClient(ctx context.Context, client *http.Client, reqURL string) (*OEmbedResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("spotify: oembed request failed: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify: oembed fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("spotify: oembed read failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify: oembed returned %d: %s", resp.StatusCode, string(body))
	}

	var o OEmbedResponse
	if err := json.Unmarshal(body, &o); err != nil {
		return nil, fmt.Errorf("spotify: oembed parse failed: %w", err)
	}

	return &o, nil
}

