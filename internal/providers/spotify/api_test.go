package spotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ─── Test helpers ─────────────────────────────────────────────────────

// newTestAPI creates an API instance wired to a test HTTP server.
// Requests are routed through authTransport (which injects auth headers)
// and then redirected to the test server via urlRewriteTransport.
func newTestAPI(handler http.HandlerFunc) (*API, *httptest.Server) {
	server := httptest.NewServer(handler)
	cfg := &SpotifyConfig{
		Mode:   "dev",
		Tokens: SpotifyTokens{AccessToken: "test-access-token"},
	}

	log := slog.New(slog.DiscardHandler)
	client := &SpotifyClient{
		cfg: cfg,
		log: log,
		http: &http.Client{
			Transport: &authTransport{
				cfg:       cfg,
				log:       log,
				transport: &urlRewriteTransport{serverURL: server.URL},
				refreshFunc: func(ctx context.Context, refreshToken, clientID string, _ *slog.Logger) (string, int, error) {
					return "", 0, errors.New("no refresh in test")
				},
			},
			Timeout: defaultTimeout,
		},
	}
	return NewAPI(client, log), server
}

// urlRewriteTransport redirects Spotify API requests to a test server.
type urlRewriteTransport struct {
	serverURL string
}

func (t *urlRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(t.serverURL)
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(req)
}

// ─── Search Tracks ────────────────────────────────────────────────────

func TestSearchTracks(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" {
			t.Errorf("path = %s, want /v1/search", r.URL.Path)
		}
		if r.URL.Query().Get("type") != "track" {
			t.Errorf("type = %s, want track", r.URL.Query().Get("type"))
		}
		if r.URL.Query().Get("q") != "Bohemian Rhapsody" {
			t.Errorf("q = %s, want Bohemian Rhapsody", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"tracks": {
				"href": "https://api.spotify.com/v1/search?type=track&q=Bohemian+Rhapsody",
				"limit": 5,
				"next": null,
				"offset": 0,
				"previous": null,
				"total": 2,
				"items": [
					{
						"album": {
							"album_type": "album",
							"total_tracks": 12,
							"external_urls": {"spotify": "https://open.spotify.com/album/1"},
							"href": "https://api.spotify.com/v1/albums/1",
							"id": "album1",
							"images": [],
							"name": "A Night at the Opera",
							"release_date": "1975-11-21",
							"release_date_precision": "day",
							"type": "album",
							"uri": "spotify:album:1",
							"artists": [{
								"external_urls": {"spotify": "https://open.spotify.com/artist/1"},
								"href": "https://api.spotify.com/v1/artists/1",
								"id": "artist1",
								"name": "Queen",
								"type": "artist",
								"uri": "spotify:artist:1"
							}]
						},
						"artists": [{
							"external_urls": {"spotify": "https://open.spotify.com/artist/1"},
							"href": "https://api.spotify.com/v1/artists/1",
							"id": "artist1",
							"name": "Queen",
							"type": "artist",
							"uri": "spotify:artist:1"
						}],
						"disc_number": 1,
						"duration_ms": 354320,
						"explicit": false,
						"external_ids": {"isrc": "GBUM71029604"},
						"external_urls": {"spotify": "https://open.spotify.com/track/1"},
						"href": "https://api.spotify.com/v1/tracks/1",
						"id": "track1",
						"name": "Bohemian Rhapsody",
						"popularity": 82,
						"track_number": 11,
						"type": "track",
						"uri": "spotify:track:1",
						"is_local": false
					},
					{
						"album": {
							"album_type": "compilation",
							"total_tracks": 20,
							"external_urls": {"spotify": "https://open.spotify.com/album/2"},
							"href": "https://api.spotify.com/v1/albums/2",
							"id": "album2",
							"images": [],
							"name": "Greatest Hits",
							"release_date": "1981-01-01",
							"release_date_precision": "day",
							"type": "album",
							"uri": "spotify:album:2",
							"artists": [{
								"external_urls": {"spotify": "https://open.spotify.com/artist/1"},
								"href": "https://api.spotify.com/v1/artists/1",
								"id": "artist1",
								"name": "Queen",
								"type": "artist",
								"uri": "spotify:artist:1"
							}]
						},
						"artists": [{
							"external_urls": {"spotify": "https://open.spotify.com/artist/1"},
							"href": "https://api.spotify.com/v1/artists/1",
							"id": "artist1",
							"name": "Queen",
							"type": "artist",
							"uri": "spotify:artist:1"
						}],
						"disc_number": 1,
						"duration_ms": 354320,
						"explicit": false,
						"external_ids": {},
						"external_urls": {"spotify": "https://open.spotify.com/track/2"},
						"href": "https://api.spotify.com/v1/tracks/2",
						"id": "track2",
						"name": "Bohemian Rhapsody (Live)",
						"popularity": 45,
						"track_number": 1,
						"type": "track",
						"uri": "spotify:track:2",
						"is_local": false
					}
				]
			}
		}`)
	}))
	defer server.Close()

	result, err := api.SearchTracks(context.Background(), "Bohemian Rhapsody", 5, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Errorf("total = %d, want 2", result.Total)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(result.Items))
	}
	if result.Items[0].Name != "Bohemian Rhapsody" {
		t.Errorf("first track name = %q", result.Items[0].Name)
	}
	if result.Items[0].Artists[0].Name != "Queen" {
		t.Errorf("first track artist = %q", result.Items[0].Artists[0].Name)
	}
	if result.Items[0].Album.Name != "A Night at the Opera" {
		t.Errorf("first track album = %q", result.Items[0].Album.Name)
	}
}

// ─── Search Albums ────────────────────────────────────────────────────

func TestSearchAlbums(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "album" {
			t.Errorf("type = %s, want album", r.URL.Query().Get("type"))
		}
		if r.URL.Query().Get("q") != "Dark Side" {
			t.Errorf("q = %s, want Dark Side", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"albums": {
				"href": "https://api.spotify.com/v1/search?type=album&q=Dark+Side",
				"limit": 3,
				"next": null,
				"offset": 0,
				"previous": null,
				"total": 1,
				"items": [{
					"album_type": "album",
					"total_tracks": 10,
					"external_urls": {"spotify": "https://open.spotify.com/album/a1"},
					"href": "https://api.spotify.com/v1/albums/a1",
					"id": "a1",
					"images": [],
					"name": "The Dark Side of the Moon",
					"release_date": "1973-03-01",
					"release_date_precision": "day",
					"type": "album",
					"uri": "spotify:album:a1",
					"artists": [{
						"external_urls": {"spotify": "https://open.spotify.com/artist/ar1"},
						"href": "https://api.spotify.com/v1/artists/ar1",
						"id": "ar1",
						"name": "Pink Floyd",
						"type": "artist",
						"uri": "spotify:artist:ar1"
					}]
				}]
			}
		}`)
	}))
	defer server.Close()

	result, err := api.SearchAlbums(context.Background(), "Dark Side", 3, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Errorf("total = %d, want 1", result.Total)
	}
	if result.Items[0].Name != "The Dark Side of the Moon" {
		t.Errorf("album name = %q", result.Items[0].Name)
	}
	if result.Items[0].Artists[0].Name != "Pink Floyd" {
		t.Errorf("album artist = %q", result.Items[0].Artists[0].Name)
	}
}

// ─── Get Track ────────────────────────────────────────────────────────

func TestGetTrack(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/tracks/t1") {
			t.Errorf("path = %s, want /tracks/t1", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"album": {
				"album_type": "single",
				"total_tracks": 1,
				"external_urls": {"spotify": "https://open.spotify.com/album/a"},
				"href": "https://api.spotify.com/v1/albums/a",
				"id": "albumID",
				"images": [{"url": "https://i.scdn.co/image/ab", "height": 640, "width": 640}],
				"name": "Single",
				"release_date": "2024-01-01",
				"release_date_precision": "day",
				"type": "album",
				"uri": "spotify:album:a",
				"artists": [{
					"external_urls": {"spotify": "https://open.spotify.com/artist/ar"},
					"href": "https://api.spotify.com/v1/artists/ar",
					"id": "artistID",
					"name": "Test Artist",
					"type": "artist",
					"uri": "spotify:artist:ar"
				}]
			},
			"artists": [{
				"external_urls": {"spotify": "https://open.spotify.com/artist/ar"},
				"href": "https://api.spotify.com/v1/artists/ar",
				"id": "artistID",
				"name": "Test Artist",
				"type": "artist",
				"uri": "spotify:artist:ar"
			}],
			"disc_number": 1,
			"duration_ms": 200000,
			"explicit": false,
			"external_ids": {"isrc": "US123456"},
			"external_urls": {"spotify": "https://open.spotify.com/track/t1"},
			"href": "https://api.spotify.com/v1/tracks/t1",
			"id": "t1",
			"name": "Test Track",
			"popularity": 50,
			"track_number": 1,
			"type": "track",
			"uri": "spotify:track:t1",
			"is_local": false
		}`)
	}))
	defer server.Close()

	track, err := api.GetTrack(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if track.ID != "t1" {
		t.Errorf("id = %q, want t1", track.ID)
	}
	if track.Name != "Test Track" {
		t.Errorf("name = %q", track.Name)
	}
	if track.DurationMs != 200000 {
		t.Errorf("duration = %d", track.DurationMs)
	}
	if track.Artists[0].Name != "Test Artist" {
		t.Errorf("artist = %q", track.Artists[0].Name)
	}
}

func TestGetTrackWithMarket(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("market") != "US" {
			t.Errorf("market = %q, want US", r.URL.Query().Get("market"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"album": {"album_type":"album","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"a","images":[],"name":"","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
			"artists":[],
			"disc_number":1,
			"duration_ms":1000,
			"explicit":false,
			"external_ids":{},
			"external_urls":{"spotify":""},
			"href":"",
			"id":"t2",
			"name":"Market Track",
			"popularity":0,
			"track_number":1,
			"type":"track",
			"uri":"",
			"is_local":false
		}`)
	}))
	defer server.Close()

	track, err := api.GetTrack(context.Background(), "t2", "US")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if track.ID != "t2" {
		t.Errorf("id = %q", track.ID)
	}
}

// ─── Get Album ────────────────────────────────────────────────────────

func TestGetAlbum(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/albums/al1") {
			t.Errorf("path = %s, want /albums/al1", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"album_type": "album",
			"total_tracks": 8,
			"external_urls": {"spotify": "https://open.spotify.com/album/al1"},
			"href": "https://api.spotify.com/v1/albums/al1",
			"id": "al1",
			"images": [],
			"name": "Test Album",
			"release_date": "2023-06-15",
			"release_date_precision": "day",
			"type": "album",
			"uri": "spotify:album:al1",
			"artists": [{
				"external_urls": {"spotify": ""},
				"href": "",
				"id": "a1",
				"name": "Album Artist",
				"type": "artist",
				"uri": ""
			}],
			"tracks": {
				"href": "",
				"limit": 50,
				"next": null,
				"offset": 0,
				"previous": null,
				"total": 8,
				"items": [{
					"artists": [{"external_urls":{"spotify":""},"href":"","id":"a1","name":"Album Artist","type":"artist","uri":""}],
					"disc_number": 1,
					"duration_ms": 180000,
					"explicit": false,
					"external_urls": {"spotify": ""},
					"href": "",
					"id": "at1",
					"name": "Album Track 1",
					"track_number": 1,
					"type": "track",
					"uri": "",
					"is_local": false
				}]
			},
			"copyrights": [{"text": "2023 Label", "type": "C"}],
			"external_ids": {"upc": "123456789"},
			"genres": [],
			"label": "Test Label",
			"popularity": 70
		}`)
	}))
	defer server.Close()

	album, err := api.GetAlbum(context.Background(), "al1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if album.ID != "al1" {
		t.Errorf("id = %q", album.ID)
	}
	if album.Name != "Test Album" {
		t.Errorf("name = %q", album.Name)
	}
	if album.TotalTracks != 8 {
		t.Errorf("total_tracks = %d", album.TotalTracks)
	}
	if len(album.Tracks.Items) != 1 {
		t.Errorf("tracks items = %d", len(album.Tracks.Items))
	}
	if album.Tracks.Items[0].Name != "Album Track 1" {
		t.Errorf("first track = %q", album.Tracks.Items[0].Name)
	}
	if album.Copyrights[0].Text != "2023 Label" {
		t.Errorf("copyright = %q", album.Copyrights[0].Text)
	}
	if album.Label != "Test Label" {
		t.Errorf("label = %q", album.Label)
	}
}

// ─── Get Artist ───────────────────────────────────────────────────────

func TestGetArtist(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/artists/ar1") {
			t.Errorf("path = %s, want /artists/ar1", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"external_urls": {"spotify": "https://open.spotify.com/artist/ar1"},
			"followers": {"href": null, "total": 5000000},
			"genres": ["rock", "alternative"],
			"href": "https://api.spotify.com/v1/artists/ar1",
			"id": "ar1",
			"images": [{"url": "https://i.scdn.co/image/abc", "height": 640, "width": 640}],
			"name": "Test Band",
			"popularity": 85,
			"type": "artist",
			"uri": "spotify:artist:ar1"
		}`)
	}))
	defer server.Close()

	artist, err := api.GetArtist(context.Background(), "ar1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if artist.ID != "ar1" {
		t.Errorf("id = %q", artist.ID)
	}
	if artist.Name != "Test Band" {
		t.Errorf("name = %q", artist.Name)
	}
	if artist.Popularity != 85 {
		t.Errorf("popularity = %d", artist.Popularity)
	}
	if len(artist.Genres) != 2 || artist.Genres[0] != "rock" {
		t.Errorf("genres = %v", artist.Genres)
	}
	if artist.Followers.Total != 5000000 {
		t.Errorf("followers = %d", artist.Followers.Total)
	}
}

// ─── Get Playlist ─────────────────────────────────────────────────────

func TestGetPlaylist(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/playlists/pl1") || strings.Contains(r.URL.Path, "/tracks") {
			t.Errorf("path = %s, want /playlists/pl1", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"collaborative": false,
			"description": "A test playlist",
			"external_urls": {"spotify": "https://open.spotify.com/playlist/pl1"},
			"followers": {"href": null, "total": 42},
			"href": "https://api.spotify.com/v1/playlists/pl1",
			"id": "pl1",
			"images": [{"url": "https://i.scdn.co/image/xyz", "height": null, "width": null}],
			"name": "My Test Playlist",
			"owner": {
				"external_urls": {"spotify": "https://open.spotify.com/user/u1"},
				"href": "https://api.spotify.com/v1/users/u1",
				"id": "u1",
				"type": "user",
				"uri": "spotify:user:u1",
				"display_name": "TestUser"
			},
			"public": true,
			"snapshot_id": "snap123",
			"tracks": {
				"href": "https://api.spotify.com/v1/playlists/pl1/tracks",
				"limit": 20,
				"next": null,
				"offset": 0,
				"previous": null,
				"total": 2,
				"items": [
					{
						"added_at": "2024-01-01T00:00:00Z",
						"added_by": {
							"external_urls": {"spotify": ""},
							"href": "",
							"id": "u1",
							"type": "user",
							"uri": "",
							"display_name": "TestUser"
						},
						"is_local": false,
						"track": {
							"album": {"album_type":"album","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"a","images":[],"name":"","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
							"artists": [{"external_urls":{"spotify":""},"href":"","id":"ar","name":"Playlist Artist","type":"artist","uri":""}],
							"disc_number": 1,
							"duration_ms": 200000,
							"explicit": false,
							"external_ids": {},
							"external_urls": {"spotify": ""},
							"href": "",
							"id": "pt1",
							"name": "Playlist Track 1",
							"popularity": 60,
							"track_number": 1,
							"type": "track",
							"uri": "",
							"is_local": false
						}
					},
					{
						"added_at": "2024-02-01T00:00:00Z",
						"is_local": false,
						"track": {
							"album": {"album_type":"single","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"b","images":[],"name":"","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
							"artists": [{"external_urls":{"spotify":""},"href":"","id":"ar","name":"Playlist Artist","type":"artist","uri":""}],
							"disc_number": 1,
							"duration_ms": 250000,
							"explicit": false,
							"external_ids": {},
							"external_urls": {"spotify": ""},
							"href": "",
							"id": "pt2",
							"name": "Playlist Track 2",
							"popularity": 40,
							"track_number": 1,
							"type": "track",
							"uri": "",
							"is_local": false
						}
					}
				]
			},
			"type": "playlist",
			"uri": "spotify:playlist:pl1"
		}`)
	}))
	defer server.Close()

	playlist, err := api.GetPlaylist(context.Background(), "pl1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if playlist.ID != "pl1" {
		t.Errorf("id = %q", playlist.ID)
	}
	if playlist.Name != "My Test Playlist" {
		t.Errorf("name = %q", playlist.Name)
	}
	if playlist.Owner.ID != "u1" {
		t.Errorf("owner = %q", playlist.Owner.ID)
	}
	if playlist.Followers.Total != 42 {
		t.Errorf("followers = %d", playlist.Followers.Total)
	}
	if playlist.Tracks.Total != 2 {
		t.Errorf("tracks total = %d", playlist.Tracks.Total)
	}
	if len(playlist.Tracks.Items) != 2 {
		t.Fatalf("tracks items = %d", len(playlist.Tracks.Items))
	}
	if playlist.Tracks.Items[0].Track.Name != "Playlist Track 1" {
		t.Errorf("first track = %q", playlist.Tracks.Items[0].Track.Name)
	}
	if playlist.Tracks.Items[0].AddedAt == nil || *playlist.Tracks.Items[0].AddedAt != "2024-01-01T00:00:00Z" {
		t.Errorf("added_at = %v", playlist.Tracks.Items[0].AddedAt)
	}
}

// ─── Get Playlist Tracks ──────────────────────────────────────────────

func TestGetPlaylistTracks(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/playlists/pl2/tracks") {
			t.Errorf("path = %s, want /playlists/pl2/tracks", r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("limit = %q, want 10", r.URL.Query().Get("limit"))
		}
		if r.URL.Query().Get("offset") != "5" {
			t.Errorf("offset = %q, want 5", r.URL.Query().Get("offset"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"href": "",
			"limit": 10,
			"next": null,
			"offset": 5,
			"previous": "",
			"total": 15,
			"items": [{
				"added_at": "2024-03-01T00:00:00Z",
				"is_local": false,
				"track": {
					"album": {"album_type":"album","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"a","images":[],"name":"","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
					"artists": [{"external_urls":{"spotify":""},"href":"","id":"ar","name":"Artist","type":"artist","uri":""}],
					"disc_number":1,"duration_ms":200000,"explicit":false,"external_ids":{},"external_urls":{"spotify":""},"href":"","id":"t","name":"Paged Track","popularity":0,"track_number":6,"type":"track","uri":"","is_local":false
				}
			}]
		}`)
	}))
	defer server.Close()

	result, err := api.GetPlaylistTracks(context.Background(), "pl2", 10, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 15 {
		t.Errorf("total = %d", result.Total)
	}
	if result.Offset != 5 {
		t.Errorf("offset = %d", result.Offset)
	}
	if result.Limit != 10 {
		t.Errorf("limit = %d", result.Limit)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items len = %d", len(result.Items))
	}
	if result.Items[0].Track.Name != "Paged Track" {
		t.Errorf("track name = %q", result.Items[0].Track.Name)
	}
}

// ─── Get User Playlists ───────────────────────────────────────────────

func TestGetUserPlaylists(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/playlists" {
			t.Errorf("path = %s, want /v1/me/playlists", r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "20" {
			t.Errorf("limit = %q, want 20", r.URL.Query().Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"href": "https://api.spotify.com/v1/me/playlists",
			"limit": 20,
			"next": null,
			"offset": 0,
			"previous": null,
			"total": 2,
			"items": [
				{
					"collaborative": false,
					"description": "First playlist",
					"external_urls": {"spotify": "https://open.spotify.com/playlist/a"},
					"href": "https://api.spotify.com/v1/playlists/a",
					"id": "a",
					"images": [],
					"name": "Playlist A",
					"owner": {
						"external_urls": {"spotify": ""},
						"href": "",
						"id": "me",
						"type": "user",
						"uri": "",
						"display_name": "Me"
					},
					"public": true,
					"snapshot_id": "s1",
					"tracks": {"href": "", "total": 10},
					"type": "playlist",
					"uri": "spotify:playlist:a"
				},
				{
					"collaborative": true,
					"description": null,
					"external_urls": {"spotify": "https://open.spotify.com/playlist/b"},
					"href": "https://api.spotify.com/v1/playlists/b",
					"id": "b",
					"images": [],
					"name": "Playlist B",
					"owner": {
						"external_urls": {"spotify": ""},
						"href": "",
						"id": "friend",
						"type": "user",
						"uri": "",
						"display_name": "Friend"
					},
					"public": false,
					"snapshot_id": "s2",
					"tracks": {"href": "", "total": 25},
					"type": "playlist",
					"uri": "spotify:playlist:b"
				}
			]
		}`)
	}))
	defer server.Close()

	result, err := api.GetUserPlaylists(context.Background(), 20, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Errorf("total = %d", result.Total)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items len = %d", len(result.Items))
	}
	if result.Items[0].Name != "Playlist A" {
		t.Errorf("first playlist = %q", result.Items[0].Name)
	}
	if result.Items[0].Tracks.Total != 10 {
		t.Errorf("first playlist tracks = %d", result.Items[0].Tracks.Total)
	}
	if result.Items[1].Name != "Playlist B" {
		t.Errorf("second playlist = %q", result.Items[1].Name)
	}
	if !result.Items[1].Collaborative {
		t.Error("second playlist should be collaborative")
	}
}

func TestGetUserPlaylistsEmpty(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"href": "https://api.spotify.com/v1/me/playlists",
			"limit": 20,
			"next": null,
			"offset": 0,
			"previous": null,
			"total": 0,
			"items": []
		}`)
	}))
	defer server.Close()

	result, err := api.GetUserPlaylists(context.Background(), 20, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("total = %d, want 0", result.Total)
	}
	if len(result.Items) != 0 {
		t.Errorf("items len = %d, want 0", len(result.Items))
	}
}

// ─── Pagination (offset + limit) ──────────────────────────────────────

func TestPagination(t *testing.T) {
	callCount := 0
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		offset := r.URL.Query().Get("offset")
		limit := r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")

		switch offset {
		case "0", "": // offset=0 may be omitted from query string
			if limit != "2" {
				t.Errorf("page 1 limit = %q, want 2", limit)
			}
			fmt.Fprint(w, `{
				"tracks": {
					"href": "",
					"limit": 2,
					"next": "next-page",
					"offset": 0,
					"previous": null,
					"total": 5,
					"items": [
						{
							"album": {"album_type":"album","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"","images":[],"name":"","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
							"artists": [{"external_urls":{"spotify":""},"href":"","id":"a","name":"A","type":"artist","uri":""}],
							"disc_number":1,"duration_ms":1000,"explicit":false,"external_ids":{},"external_urls":{"spotify":""},"href":"","id":"t0","name":"Track 0","popularity":0,"track_number":1,"type":"track","uri":"","is_local":false
						},
						{
							"album": {"album_type":"album","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"","images":[],"name":"","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
							"artists": [{"external_urls":{"spotify":""},"href":"","id":"a","name":"A","type":"artist","uri":""}],
							"disc_number":1,"duration_ms":1000,"explicit":false,"external_ids":{},"external_urls":{"spotify":""},"href":"","id":"t1","name":"Track 1","popularity":0,"track_number":1,"type":"track","uri":"","is_local":false
						}
					]
				}
			}`)
		case "2":
			if limit != "2" {
				t.Errorf("page 2 limit = %q, want 2", limit)
			}
			fmt.Fprint(w, `{
				"tracks": {
					"href": "",
					"limit": 2,
					"next": "next-page-2",
					"offset": 2,
					"previous": "prev-page",
					"total": 5,
					"items": [
						{
							"album": {"album_type":"album","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"","images":[],"name":"","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
							"artists": [{"external_urls":{"spotify":""},"href":"","id":"a","name":"A","type":"artist","uri":""}],
							"disc_number":1,"duration_ms":1000,"explicit":false,"external_ids":{},"external_urls":{"spotify":""},"href":"","id":"t2","name":"Track 2","popularity":0,"track_number":1,"type":"track","uri":"","is_local":false
						},
						{
							"album": {"album_type":"album","total_tracks":1,"external_urls":{"spotify":""},"href":"","id":"","images":[],"name":"","release_date":"","release_date_precision":"day","type":"album","uri":"","artists":[]},
							"artists": [{"external_urls":{"spotify":""},"href":"","id":"a","name":"A","type":"artist","uri":""}],
							"disc_number":1,"duration_ms":1000,"explicit":false,"external_ids":{},"external_urls":{"spotify":""},"href":"","id":"t3","name":"Track 3","popularity":0,"track_number":1,"type":"track","uri":"","is_local":false
						}
					]
				}
			}`)
		default:
			t.Errorf("unexpected offset = %q", offset)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}))
	defer server.Close()

	// First page.
	page1, err := api.SearchTracks(context.Background(), "test", 2, 0)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if page1.Total != 5 {
		t.Errorf("page1 total = %d", page1.Total)
	}
	if len(page1.Items) != 2 {
		t.Errorf("page1 items = %d", len(page1.Items))
	}
	if page1.Items[0].Name != "Track 0" {
		t.Errorf("page1[0] = %q", page1.Items[0].Name)
	}

	// Second page.
	page2, err := api.SearchTracks(context.Background(), "test", 2, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if page2.Offset != 2 {
		t.Errorf("page2 offset = %d", page2.Offset)
	}
	if len(page2.Items) != 2 {
		t.Errorf("page2 items = %d", len(page2.Items))
	}
	if page2.Items[0].Name != "Track 2" {
		t.Errorf("page2[0] = %q", page2.Items[0].Name)
	}

	if callCount != 2 {
		t.Errorf("call count = %d, want 2", callCount)
	}
}

// ─── HTTP Error Responses ─────────────────────────────────────────────

func TestHTTPError404(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"status":404,"message":"Resource not found"}}`)
	}))
	defer server.Close()

	_, err := api.GetTrack(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "API error 404") {
		t.Errorf("error = %q, want containing 'API error 404'", err.Error())
	}
}

func TestHTTPError403(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"status":403,"message":"Insufficient scope"}}`)
	}))
	defer server.Close()

	_, err := api.GetUserPlaylists(context.Background(), 20, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "API error 403") {
		t.Errorf("error = %q, want containing 'API error 403'", err.Error())
	}
}

func TestHTTPError500(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// No JSON body — server error with empty body.
	}))
	defer server.Close()

	_, err := api.GetArtist(context.Background(), "id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want containing 'HTTP 500'", err.Error())
	}
}

func TestHTTPErrorNoJSONBody(t *testing.T) {
	// Non-OK response without an error JSON body should fall back to generic HTTP status.
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "Bad Gateway")
	}))
	defer server.Close()

	_, err := api.SearchTracks(context.Background(), "query", 5, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error = %q, want containing 502", err.Error())
	}
}

// ─── Free Mode Error ──────────────────────────────────────────────────

func TestFreeModeError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called in free mode")
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := &SpotifyConfig{
		Mode:   "free",
		Tokens: SpotifyTokens{AccessToken: ""},
	}
	log := slog.New(slog.DiscardHandler)
	client := &SpotifyClient{
		cfg: cfg,
		log: log,
		http: &http.Client{
			Transport: &authTransport{
				cfg:       cfg,
				log:       log,
				transport: &urlRewriteTransport{serverURL: server.URL},
			},
			Timeout: defaultTimeout,
		},
	}
	api := NewAPI(client, log)

	tests := []struct {
		name string
		fn   func() error
	}{
		{"SearchTracks", func() error { _, e := api.SearchTracks(context.Background(), "q", 5, 0); return e }},
		{"SearchAlbums", func() error { _, e := api.SearchAlbums(context.Background(), "q", 5, 0); return e }},
		{"GetTrack", func() error { _, e := api.GetTrack(context.Background(), "id"); return e }},
		{"GetAlbum", func() error { _, e := api.GetAlbum(context.Background(), "id"); return e }},
		{"GetArtist", func() error { _, e := api.GetArtist(context.Background(), "id"); return e }},
		{"GetPlaylist", func() error { _, e := api.GetPlaylist(context.Background(), "id"); return e }},
		{"GetPlaylistTracks", func() error { _, e := api.GetPlaylistTracks(context.Background(), "id", 10, 0); return e }},
		{"GetUserPlaylists", func() error { _, e := api.GetUserPlaylists(context.Background(), 20, 0); return e }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if err == nil {
				t.Fatal("expected free mode error, got nil")
			}
			if !strings.Contains(err.Error(), "dev mode") {
				t.Errorf("error = %q, want containing 'dev mode'", err.Error())
			}
		})
	}
}

func TestFreeModeNoTokenEvenDevMode(t *testing.T) {
	// Mode is "dev" but AccessToken is empty — should also fail.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called without token")
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := &SpotifyConfig{
		Mode:   "dev",
		Tokens: SpotifyTokens{AccessToken: ""},
	}
	log := slog.New(slog.DiscardHandler)
	client := &SpotifyClient{
		cfg: cfg,
		log: log,
		http: &http.Client{
			Transport: &authTransport{
				cfg:       cfg,
				log:       log,
				transport: &urlRewriteTransport{serverURL: server.URL},
			},
			Timeout: defaultTimeout,
		},
	}
	api := NewAPI(client, log)

	_, err := api.SearchTracks(context.Background(), "q", 5, 0)
	if err == nil {
		t.Fatal("expected error for dev mode without token")
	}
	if !strings.Contains(err.Error(), "dev mode") {
		t.Errorf("error = %q, want containing 'dev mode'", err.Error())
	}
}

// ─── Boundary: zero limit / offset not sent as params ─────────────────

func TestLimitOffsetOmittedWhenZero(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When limit and offset are 0, they should NOT appear in the query string.
		if r.URL.Query().Get("limit") != "" {
			t.Errorf("limit param should be omitted, got %q", r.URL.Query().Get("limit"))
		}
		if r.URL.Query().Get("offset") != "" {
			t.Errorf("offset param should be omitted, got %q", r.URL.Query().Get("offset"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tracks":{"href":"","limit":20,"next":null,"offset":0,"previous":null,"total":0,"items":[]}}`)
	}))
	defer server.Close()

	_, err := api.SearchTracks(context.Background(), "q", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ─── Context cancellation ─────────────────────────────────────────────

func TestContextCancellation(t *testing.T) {
	api, server := newTestAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handler that doesn't respond quickly.
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := api.SearchTracks(ctx, "q", 5, 0)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}
