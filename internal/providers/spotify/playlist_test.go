package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/playlist"
)

// hostRewriteTransport rewrites requests to targetHost → test server.
// Used by plugin_test.go's hostRewriteClient helper to intercept oEmbed calls.
type hostRewriteTransport struct {
	targetHost string
	serverURL  string
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.targetHost {
		u, _ := url.Parse(t.serverURL)
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
	}
	return http.DefaultTransport.RoundTrip(req)
}

// ─── Interface compliance ─────────────────────────────────────────────

func TestPluginImplementsPlaylistSource(t *testing.T) {
	var _ playlist.PlaylistSourceProvider = (*Plugin)(nil)
}

func TestPlugin_PlaylistSourceInterface(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	ps := p.PlaylistSource()
	if ps == nil {
		t.Fatal("PlaylistSource() returned nil in dev mode")
	}
	src, ok := ps.(playlist.Source)
	if !ok {
		t.Fatal("PlaylistSource() does not return a playlist.Source")
	}
	if src.Name() != "spotify" {
		t.Errorf("Name() = %q, want spotify", src.Name())
	}
	if src.DisplayName() != "Spotify" {
		t.Errorf("DisplayName() = %q, want Spotify", src.DisplayName())
	}
}

// ─── PlaylistSource ───────────────────────────────────────────────────

func TestPlugin_PlaylistSource_Dev(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	ps := p.PlaylistSource()
	if ps == nil {
		t.Fatal("PlaylistSource() returned nil in dev mode")
	}
	if ps != p {
		t.Error("PlaylistSource() should return self in dev mode")
	}
}

func TestPlugin_PlaylistSource_Free(t *testing.T) {
	p := &Plugin{
		cfg: &SpotifyConfig{
			Mode:   "free",
			Tokens: SpotifyTokens{},
		},
	}
	ps := p.PlaylistSource()
	if ps == nil {
		t.Fatal("PlaylistSource() returned nil in free mode (free mode is always configured)")
	}
	if ps != p {
		t.Error("PlaylistSource() should return self in free mode")
	}
}

func TestPlugin_PlaylistSource_DevNoCredentials(t *testing.T) {
	p := &Plugin{
		cfg: &SpotifyConfig{
			Mode:   "dev",
			Tokens: SpotifyTokens{AccessToken: ""},
		},
	}
	ps := p.PlaylistSource()
	if ps != nil {
		t.Errorf("PlaylistSource() should return nil without credentials, got %v", ps)
	}
}

// ─── GetUserPlaylists dev mode ────────────────────────────────────────

func TestPlugin_GetUserPlaylists_Dev_SinglePage(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/playlists" {
			t.Errorf("path = %s, want /v1/me/playlists", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(json.RawMessage(`{
			"href": "https://api.spotify.com/v1/me/playlists",
			"limit": 50,
			"next": null,
			"offset": 0,
			"previous": null,
			"total": 3,
			"items": [
				{
					"collaborative": false,
					"description": "Rock classics",
					"external_urls": {"spotify": "https://open.spotify.com/playlist/a"},
					"href": "https://api.spotify.com/v1/playlists/a",
					"id": "pl-a",
					"images": [{"url": "https://i.scdn.co/image/a"}],
					"name": "Rock Classics",
					"owner": {
						"external_urls": {"spotify": ""},
						"href": "",
						"id": "user1",
						"type": "user",
						"uri": "",
						"display_name": "Alice"
					},
					"public": true,
					"snapshot_id": "s1",
					"tracks": {"href": "", "total": 42},
					"type": "playlist",
					"uri": "spotify:playlist:a"
				},
				{
					"collaborative": false,
					"description": null,
					"external_urls": {"spotify": "https://open.spotify.com/playlist/b"},
					"href": "https://api.spotify.com/v1/playlists/b",
					"id": "pl-b",
					"images": [],
					"name": "Chill Vibes",
					"owner": {
						"external_urls": {"spotify": ""},
						"href": "",
						"id": "user1",
						"type": "user",
						"uri": "",
						"display_name": null
					},
					"public": false,
					"snapshot_id": "s2",
					"tracks": null,
					"type": "playlist",
					"uri": "spotify:playlist:b"
				},
				{
					"collaborative": true,
					"description": "Shared picks",
					"external_urls": {"spotify": "https://open.spotify.com/playlist/c"},
					"href": "https://api.spotify.com/v1/playlists/c",
					"id": "pl-c",
					"images": [{"url": "https://i.scdn.co/image/c1"}, {"url": "https://i.scdn.co/image/c2"}],
					"name": "Collab Mix",
					"owner": {
						"external_urls": {"spotify": ""},
						"href": "",
						"id": "user2",
						"type": "user",
						"uri": "",
						"display_name": "Bob"
					},
					"public": true,
					"snapshot_id": "s3",
					"tracks": {"href": "", "total": 15},
					"type": "playlist",
					"uri": "spotify:playlist:c"
				}
			]
		}`))
	}))
	defer server.Close()

	result, err := p.GetUserPlaylists(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("got %d playlists, want 3", len(result))
	}

	if result[0].SourceID != "pl-a" {
		t.Errorf("playlist[0].SourceID = %q, want pl-a", result[0].SourceID)
	}
	if result[0].Name != "Rock Classics" {
		t.Errorf("playlist[0].Name = %q, want Rock Classics", result[0].Name)
	}
	if result[0].Description != "Rock classics" {
		t.Errorf("playlist[0].Description = %q", result[0].Description)
	}
	if result[0].TrackCount != 42 {
		t.Errorf("playlist[0].TrackCount = %d, want 42", result[0].TrackCount)
	}
	if result[0].CoverURL != "https://i.scdn.co/image/a" {
		t.Errorf("playlist[0].CoverURL = %q", result[0].CoverURL)
	}
	if result[0].OwnerName != "Alice" {
		t.Errorf("playlist[0].OwnerName = %q, want Alice", result[0].OwnerName)
	}

	// Second: null description, no images, null display_name.
	if result[1].Name != "Chill Vibes" {
		t.Errorf("playlist[1].Name = %q", result[1].Name)
	}
	if result[1].Description != "" {
		t.Errorf("playlist[1].Description should be empty, got %q", result[1].Description)
	}
	if result[1].CoverURL != "" {
		t.Errorf("playlist[1].CoverURL should be empty, got %q", result[1].CoverURL)
	}
	if result[1].TrackCount != 0 {
		t.Errorf("playlist[1].TrackCount = %d, want 0", result[1].TrackCount)
	}
	if result[1].OwnerName != "user1" {
		t.Errorf("playlist[1].OwnerName = %q, want user1 (fallback to ID)", result[1].OwnerName)
	}
}

func TestPlugin_GetUserPlaylists_Dev_Pagination(t *testing.T) {
	callCount := 0
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		offset := r.URL.Query().Get("offset")
		limit := r.URL.Query().Get("limit")
		if limit != "50" {
			t.Errorf("limit = %q, want 50", limit)
		}
		w.Header().Set("Content-Type", "application/json")

		switch offset {
		case "0", "":
			json.NewEncoder(w).Encode(json.RawMessage(`{
				"href": "",
				"limit": 50,
				"next": "next-page-url",
				"offset": 0,
				"previous": null,
				"total": 120,
				"items": [
					{"collaborative":false,"description":null,"external_urls":{"spotify":""},"href":"","id":"p0","images":[],"name":"Page1-A","owner":{"external_urls":{"spotify":""},"href":"","id":"u1","type":"user","uri":"","display_name":"U"},"public":true,"snapshot_id":"s","tracks":{"href":"","total":5},"type":"playlist","uri":""},
					{"collaborative":false,"description":null,"external_urls":{"spotify":""},"href":"","id":"p1","images":[],"name":"Page1-B","owner":{"external_urls":{"spotify":""},"href":"","id":"u1","type":"user","uri":"","display_name":"U"},"public":true,"snapshot_id":"s","tracks":{"href":"","total":3},"type":"playlist","uri":""}
				]
			}`))
		case "50":
			json.NewEncoder(w).Encode(json.RawMessage(`{
				"href": "",
				"limit": 50,
				"next": null,
				"offset": 50,
				"previous": "prev-page-url",
				"total": 120,
				"items": [
					{"collaborative":false,"description":null,"external_urls":{"spotify":""},"href":"","id":"p50","images":[],"name":"Page2-A","owner":{"external_urls":{"spotify":""},"href":"","id":"u1","type":"user","uri":"","display_name":"U"},"public":true,"snapshot_id":"s","tracks":{"href":"","total":7},"type":"playlist","uri":""}
				]
			}`))
		default:
			t.Errorf("unexpected offset = %q", offset)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}))
	defer server.Close()

	result, err := p.GetUserPlaylists(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2", callCount)
	}
	if len(result) != 3 {
		t.Fatalf("got %d items, want 3", len(result))
	}
	if result[0].Name != "Page1-A" {
		t.Errorf("result[0] = %q", result[0].Name)
	}
	if result[1].Name != "Page1-B" {
		t.Errorf("result[1] = %q", result[1].Name)
	}
	if result[2].Name != "Page2-A" {
		t.Errorf("result[2] = %q", result[2].Name)
	}
}

func TestPlugin_GetUserPlaylists_Dev_Empty(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(json.RawMessage(`{
			"href": "",
			"limit": 50,
			"next": null,
			"offset": 0,
			"previous": null,
			"total": 0,
			"items": []
		}`))
	}))
	defer server.Close()

	result, err := p.GetUserPlaylists(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("got %d playlists, want 0", len(result))
	}
}

func TestPlugin_GetUserPlaylists_FreeMode(t *testing.T) {
	p := &Plugin{
		cfg: &SpotifyConfig{
			Mode:   "free",
			Tokens: SpotifyTokens{},
		},
	}
	_, err := p.GetUserPlaylists(context.Background())
	if err == nil {
		t.Fatal("expected error in free mode, got nil")
	}
	if !strings.Contains(err.Error(), "no access token") {
		t.Errorf("error = %q, want containing 'no access token'", err.Error())
	}
}

// ─── GetPlaylistTracks dev mode ───────────────────────────────────────

func TestPlugin_GetPlaylistTracks_Dev(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasSuffix(r.URL.Path, "/tracks") {
			if r.URL.Query().Get("offset") == "50" {
				json.NewEncoder(w).Encode(json.RawMessage(`{
					"href": "", "limit": 50, "next": null, "offset": 50, "previous": "", "total": 3,
					"items": [
						{
							"added_at": "2024-03-01T00:00:00Z",
							"is_local": false,
							"track": {
								"album": {"album_type":"album","total_tracks":10,"external_urls":{"spotify":""},"href":"","id":"alb3","images":[],"name":"Album Three","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
								"artists": [{"external_urls":{"spotify":""},"href":"","id":"ar3","name":"Artist Three","type":"artist","uri":""}],
								"disc_number":1,"duration_ms":210000,"explicit":false,"external_ids":{"isrc":"GB003"},"external_urls":{"spotify":""},"href":"","id":"t3","name":"Track Three","popularity":0,"track_number":1,"type":"track","uri":"","is_local":false
							}
						}
					]
				}`))
				return
			}
			json.NewEncoder(w).Encode(json.RawMessage(`{
				"href": "", "limit": 50, "next": "next-page", "offset": 0, "previous": null, "total": 3,
				"items": [
					{
						"added_at": "2024-01-01T00:00:00Z",
						"is_local": false,
						"track": {
							"album": {"album_type":"album","total_tracks":12,"external_urls":{"spotify":""},"href":"","id":"alb1","images":[],"name":"Album One","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
							"artists": [{"external_urls":{"spotify":""},"href":"","id":"ar1","name":"Artist One","type":"artist","uri":""}],
							"disc_number":1,"duration_ms":200000,"explicit":false,"external_ids":{"isrc":"GB001"},"external_urls":{"spotify":""},"href":"","id":"t1","name":"Track One","popularity":60,"track_number":1,"type":"track","uri":"","is_local":false
						}
					},
					{
						"added_at": "2024-02-01T00:00:00Z",
						"is_local": false,
						"track": {
							"album": {"album_type":"single","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"alb2","images":[],"name":"Album Two","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
							"artists": [{"external_urls":{"spotify":""},"href":"","id":"ar2","name":"Artist Two","type":"artist","uri":""},{"external_urls":{"spotify":""},"href":"","id":"ar2b","name":"Feat Artist","type":"artist","uri":""}],
							"disc_number":1,"duration_ms":250000,"explicit":true,"external_ids":{"isrc":"GB002"},"external_urls":{"spotify":""},"href":"","id":"t2","name":"Track Two","popularity":40,"track_number":1,"type":"track","uri":"","is_local":false
						}
					}
				]
			}`))
		} else {
			// GetPlaylist response.
			json.NewEncoder(w).Encode(json.RawMessage(`{
				"collaborative": false,
				"description": null,
				"external_urls": {"spotify": ""},
				"followers": {"href": null, "total": 100},
				"href": "",
				"id": "pl123",
				"images": [],
				"name": "My Dev Playlist",
				"owner": {"external_urls":{"spotify":""},"href":"","id":"u1","type":"user","uri":"","display_name":"Me"},
				"public": true,
				"snapshot_id": "snap",
				"tracks": {"href":"","limit":50,"next":null,"offset":0,"previous":null,"total":3,"items":[]},
				"type": "playlist",
				"uri": "spotify:playlist:pl123"
			}`))
		}
	}))
	defer server.Close()

	tracks, playlistName, err := p.GetPlaylistTracks(context.Background(), "pl123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if playlistName != "My Dev Playlist" {
		t.Errorf("playlistName = %q, want My Dev Playlist", playlistName)
	}
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3", len(tracks))
	}

	if tracks[0].SourceTrackID != "t1" {
		t.Errorf("tracks[0].SourceTrackID = %q", tracks[0].SourceTrackID)
	}
	if tracks[0].Title != "Track One" {
		t.Errorf("tracks[0].Title = %q", tracks[0].Title)
	}
	if tracks[0].Artist != "Artist One" {
		t.Errorf("tracks[0].Artist = %q, want Artist One", tracks[0].Artist)
	}
	if tracks[0].Album != "Album One" {
		t.Errorf("tracks[0].Album = %q", tracks[0].Album)
	}
	if tracks[0].DurationMs != 200000 {
		t.Errorf("tracks[0].DurationMs = %d", tracks[0].DurationMs)
	}
	if tracks[0].ISRC != "GB001" {
		t.Errorf("tracks[0].ISRC = %q", tracks[0].ISRC)
	}

	// Multi-artist → picks first.
	if tracks[1].Artist != "Artist Two" {
		t.Errorf("tracks[1].Artist = %q, want Artist Two", tracks[1].Artist)
	}
	if tracks[1].ISRC != "GB002" {
		t.Errorf("tracks[1].ISRC = %q", tracks[1].ISRC)
	}

	// From page 2.
	if tracks[2].Title != "Track Three" {
		t.Errorf("tracks[2].Title = %q", tracks[2].Title)
	}
}

func TestPlugin_GetPlaylistTracks_Dev_Empty(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/tracks") {
			json.NewEncoder(w).Encode(json.RawMessage(`{"href":"","limit":50,"next":null,"offset":0,"previous":null,"total":0,"items":[]}`))
		} else {
			json.NewEncoder(w).Encode(json.RawMessage(`{
				"collaborative":false,"description":null,"external_urls":{"spotify":""},"followers":{"href":null,"total":0},"href":"","id":"empty","images":[],"name":"Empty Playlist","owner":{"external_urls":{"spotify":""},"href":"","id":"u1","type":"user","uri":"","display_name":"U"},"public":true,"snapshot_id":"s","tracks":{"href":"","limit":50,"next":null,"offset":0,"previous":null,"total":0,"items":[]},"type":"playlist","uri":""
			}`))
		}
	}))
	defer server.Close()

	tracks, name, err := p.GetPlaylistTracks(context.Background(), "empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "Empty Playlist" {
		t.Errorf("name = %q", name)
	}
	if len(tracks) != 0 {
		t.Errorf("got %d tracks, want 0", len(tracks))
	}
}

// ─── GetPlaylistTracks free mode (embed fallback) ─────────────────────

func TestPlugin_GetPlaylistTracks_Free_EmbedFallback(t *testing.T) {
	// Serve a mock Spotify embed page with __NEXT_DATA__ JSON.
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextData := `{"props":{"pageProps":{"state":{"data":{"entity":{"name":"Today's Top Hits","trackList":[{"uri":"spotify:track:abc123","title":"Song One","subtitle":"Artist A","duration":200000},{"uri":"spotify:track:def456","title":"Song Two","subtitle":"Artist B, Artist C","duration":180000}]}}}}}}`
		html := `<html><script id="__NEXT_DATA__" type="application/json">` + nextData + `</script></html>`
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer embedServer.Close()

	client := urlRewriteClientAny(embedServer.URL)
	p := &Plugin{
		cfg:          &SpotifyConfig{Mode: "free"},
		dlPath:       "/tmp",
		oembedClient: client,
	}
	playlistURL := "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"

	tracks, name, err := p.GetPlaylistTracks(context.Background(), playlistURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if name != "Today's Top Hits" {
		t.Errorf("name = %q, want Today's Top Hits", name)
	}
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(tracks))
	}
	if tracks[0].Title != "Song One" || tracks[0].Artist != "Artist A" {
		t.Errorf("track[0] = %+v", tracks[0])
	}
	if tracks[1].SourceTrackID != "def456" || tracks[1].DurationMs != 180000 {
		t.Errorf("track[1] = %+v", tracks[1])
	}
}

func TestPlugin_GetPlaylistTracks_Free_InvalidURL(t *testing.T) {
	p := &Plugin{
		cfg: &SpotifyConfig{Mode: "free"},
	}
	_, _, err := p.GetPlaylistTracks(context.Background(), "not-a-url")
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
	if !strings.Contains(err.Error(), "invalid playlist URL") {
		t.Errorf("error = %q, want containing 'invalid playlist URL'", err.Error())
	}
}

func TestPlugin_GetPlaylistTracks_Free_WrongType(t *testing.T) {
	p := &Plugin{
		cfg: &SpotifyConfig{Mode: "free"},
	}
	_, _, err := p.GetPlaylistTracks(context.Background(), "https://open.spotify.com/track/4iV5W9uYEdYUVa79Axb7Rh")
	if err == nil {
		t.Fatal("expected error for track URL, got nil")
	}
	if !strings.Contains(err.Error(), "expected playlist") {
		t.Errorf("error = %q, want containing 'expected playlist'", err.Error())
	}
}

func TestPlugin_GetPlaylistTracks_Free_OEmbedError(t *testing.T) {
	oembedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer oembedServer.Close()

	client := hostRewriteClient(oembedServer.URL, "open.spotify.com")
	p := &Plugin{
		cfg:          &SpotifyConfig{Mode: "free"},
		dlPath:       "/tmp",
		oembedClient: client,
	}

	_, _, err := p.GetPlaylistTracks(context.Background(), "https://open.spotify.com/playlist/nonexistent")
	if err == nil {
		t.Fatal("expected oembed error, got nil")
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error = %q, want containing 'embed'", err.Error())
	}
}

// ─── API error propagation ────────────────────────────────────────────

func TestPlugin_GetUserPlaylists_APIError(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := p.GetUserPlaylists(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPlugin_GetPlaylistTracks_Dev_APIError(t *testing.T) {
	p, server := newDevPlugin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, _, err := p.GetPlaylistTracks(context.Background(), "pl-err")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
