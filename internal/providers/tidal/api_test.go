package tidal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestAPI creates an apiClient wired to a test HTTP server. Base URLs point
// at the server so all v1/v2 paths hit the handler; a tiny rate interval keeps
// tests fast.
func newTestAPI(t *testing.T, handler http.HandlerFunc) (*apiClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := newAPI(server.URL, server.URL, 5*time.Second, time.Millisecond)
	c.SetToken("test-token")
	c.SetCountry("US")
	return c, server
}

// trackItem returns a minimal v1 track JSON object.
func trackItem(id int64, title string) string {
	return fmt.Sprintf(`{"id":%d,"title":%q,"duration":200,"isrc":"US-ABC-12-34567","audioQuality":"LOSSLESS","artist":{"id":1,"name":"Mock Artist"},"album":{"id":10,"title":"Mock Album"}}`, id, title)
}

// ─── Search ─────────────────────────────────────────────────────────────

func TestSearchTracks(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %s, want /search", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("query") != "Mock Query" {
			t.Errorf("query = %q, want Mock Query", q.Get("query"))
		}
		if q.Get("types") != "TRACKS" {
			t.Errorf("types = %q, want TRACKS", q.Get("types"))
		}
		if q.Get("limit") != "5" || q.Get("offset") != "10" {
			t.Errorf("limit/offset = %s/%s, want 5/10", q.Get("limit"), q.Get("offset"))
		}
		if q.Get("countryCode") != "US" {
			t.Errorf("countryCode = %q, want US", q.Get("countryCode"))
		}
		fmt.Fprintf(w, `{"tracks":{"items":[%s],"totalNumberOfItems":1},"albums":{"items":[],"totalNumberOfItems":0},"artists":{"items":[],"totalNumberOfItems":0}}`, trackItem(11, "Mock Track"))
	})

	tracks, err := api.SearchTracks(context.Background(), "Mock Query", 5, 10)
	if err != nil {
		t.Fatalf("SearchTracks failed: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0].ID != 11 || tracks[0].Title != "Mock Track" {
		t.Errorf("track = %+v, want id 11 title Mock Track", tracks[0])
	}
}

func TestSearchAlbums(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("types") != "ALBUMS" {
			t.Errorf("types = %q, want ALBUMS", r.URL.Query().Get("types"))
		}
		fmt.Fprint(w, `{"tracks":{"items":[],"totalNumberOfItems":0},"albums":{"items":[{"id":21,"title":"Mock Album","cover":"a1b2c3d4-e5f6-7890-abcd-ef1234567890","numTracks":8,"releaseDate":"2020-01-01","artists":[{"id":1,"name":"Mock Artist","type":"MAIN"}]}],"totalNumberOfItems":1},"artists":{"items":[],"totalNumberOfItems":0}}`)
	})

	albums, err := api.SearchAlbums(context.Background(), "Mock Album", 5, 0)
	if err != nil {
		t.Fatalf("SearchAlbums failed: %v", err)
	}
	if len(albums) != 1 {
		t.Fatalf("got %d albums, want 1", len(albums))
	}
	if albums[0].Title != "Mock Album" || albums[0].NumTracks != 8 {
		t.Errorf("album = %+v", albums[0])
	}
	if albums[0].Artist.Name != "" || len(albums[0].Artists) == 0 {
		t.Errorf("expected artist in artists array only, got artist=%+v artists=%d", albums[0].Artist, len(albums[0].Artists))
	}
}

func TestSearchArtists(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("types") != "ARTISTS" {
			t.Errorf("types = %q, want ARTISTS", r.URL.Query().Get("types"))
		}
		fmt.Fprint(w, `{"tracks":{"items":[],"totalNumberOfItems":0},"albums":{"items":[],"totalNumberOfItems":0},"artists":{"items":[{"id":31,"name":"Mock Artist"}],"totalNumberOfItems":1}}`)
	})

	artists, err := api.SearchArtists(context.Background(), "Mock Artist", 10)
	if err != nil {
		t.Fatalf("SearchArtists failed: %v", err)
	}
	if len(artists) != 1 || artists[0].ID != 31 {
		t.Errorf("artists = %+v, want single artist id 31", artists)
	}
}

func TestSearchCombined(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("types") != "TRACKS,ALBUMS" {
			t.Errorf("types = %q, want TRACKS,ALBUMS", r.URL.Query().Get("types"))
		}
		fmt.Fprintf(w, `{"tracks":{"items":[%s],"totalNumberOfItems":1},"albums":{"items":[{"id":22,"title":"Mock Album"}],"totalNumberOfItems":1},"artists":{"items":[],"totalNumberOfItems":0}}`, trackItem(12, "Mock Track"))
	})

	tracks, albums, err := api.Search(context.Background(), "Mock Query", 5, 0)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(tracks) != 1 || len(albums) != 1 {
		t.Errorf("tracks=%d albums=%d, want 1/1", len(tracks), len(albums))
	}
}

// ─── Album ──────────────────────────────────────────────────────────────

func TestGetAlbum(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/albums/123" {
			t.Errorf("path = %s, want /albums/123", r.URL.Path)
		}
		fmt.Fprint(w, `{"id":123,"title":"Mock Album","numTracks":8,"releaseDate":"2020-01-01","artists":[{"id":1,"name":"Mock Artist","type":"MAIN"}]}`)
	})

	album, err := api.GetAlbum(context.Background(), "123")
	if err != nil {
		t.Fatalf("GetAlbum failed: %v", err)
	}
	if album.ID != 123 || album.Title != "Mock Album" || album.NumTracks != 8 {
		t.Errorf("album = %+v", album)
	}
	if len(album.Artists) == 0 || album.Artists[0].Name != "Mock Artist" {
		t.Errorf("album.Artists = %+v", album.Artists)
	}
}

func TestGetAlbumTracksPagination(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/albums/123/tracks" {
			t.Errorf("path = %s, want /albums/123/tracks", r.URL.Path)
		}
		offset := 0
		fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
		var items []string
		switch offset {
		case 0:
			for i := 0; i < 100; i++ {
				items = append(items, trackItem(int64(i), fmt.Sprintf("Mock Track %d", i)))
			}
		case 100:
			for i := 100; i < 105; i++ {
				items = append(items, trackItem(int64(i), fmt.Sprintf("Mock Track %d", i)))
			}
		default:
			t.Errorf("unexpected offset %d", offset)
		}
		fmt.Fprintf(w, `{"items":[%s],"totalNumberOfItems":105}`, strings.Join(items, ","))
	})

	tracks, err := api.GetAlbumTracks(context.Background(), "123")
	if err != nil {
		t.Fatalf("GetAlbumTracks failed: %v", err)
	}
	if len(tracks) != 105 {
		t.Errorf("got %d tracks, want 105", len(tracks))
	}
}

// ─── Artist ─────────────────────────────────────────────────────────────

func TestGetArtistAlbums(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artists/77/albums" {
			t.Errorf("path = %s, want /artists/77/albums", r.URL.Path)
		}
		fmt.Fprint(w, `{"items":[{"id":41,"title":"Mock Album One"},{"id":42,"title":"Mock Album Two"}],"totalNumberOfItems":2}`)
	})

	albums, err := api.GetArtistAlbums(context.Background(), "77", 10)
	if err != nil {
		t.Fatalf("GetArtistAlbums failed: %v", err)
	}
	if len(albums) != 2 {
		t.Errorf("got %d albums, want 2", len(albums))
	}
}

func TestGetArtistTopTracks(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artists/77/toptracks" {
			t.Errorf("path = %s, want /artists/77/toptracks", r.URL.Path)
		}
		fmt.Fprintf(w, `{"items":[%s],"totalNumberOfItems":1}`, trackItem(51, "Mock Top Track"))
	})

	tracks, err := api.GetArtistTopTracks(context.Background(), "77", 10)
	if err != nil {
		t.Fatalf("GetArtistTopTracks failed: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Title != "Mock Top Track" {
		t.Errorf("tracks = %+v", tracks)
	}
}

func TestGetSimilarArtists(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artists/77/similar" {
			t.Errorf("path = %s, want /artists/77/similar", r.URL.Path)
		}
		fmt.Fprint(w, `{"items":[{"id":61,"name":"Mock Similar Artist"}],"totalNumberOfItems":1}`)
	})

	artists, err := api.GetSimilarArtists(context.Background(), "77", 10)
	if err != nil {
		t.Fatalf("GetSimilarArtists failed: %v", err)
	}
	if len(artists) != 1 || artists[0].Name != "Mock Similar Artist" {
		t.Errorf("artists = %+v", artists)
	}
}

// ─── Playlist ───────────────────────────────────────────────────────────

func TestGetUserPlaylistsV2Folders(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/my-collection/playlists/folders" {
			t.Errorf("path = %s, want v2 folders path", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("folderId") != "root" || q.Get("includeOnly") != "PLAYLIST" {
			t.Errorf("folderId/includeOnly = %s/%s, want root/PLAYLIST", q.Get("folderId"), q.Get("includeOnly"))
		}
		fmt.Fprint(w, `{"items":[
			{"trn":"trn:playlist:abc-123","itemType":"PLAYLIST","name":"Mock Playlist Alpha","data":{"uuid":"abc-123","title":"Mock Playlist Alpha","numberOfTracks":8,"description":"test playlist","type":"USER"}}
		],"totalNumberOfItems":1,"cursor":null}`)
	})

	playlists, err := api.GetUserPlaylists(context.Background(), 50, 0)
	if err != nil {
		t.Fatalf("GetUserPlaylists failed: %v", err)
	}
	if len(playlists) != 1 {
		t.Fatalf("got %d playlists, want 1", len(playlists))
	}
	p := playlists[0]
	if p.Data == nil || p.Data.UUID != "abc-123" || p.Data.NumberOfTracks != 8 {
		t.Errorf("playlist data = %+v", p.Data)
	}
	if p.TRN != "trn:playlist:abc-123" || p.Name != "Mock Playlist Alpha" {
		t.Errorf("playlist top-level = trn %q name %q", p.TRN, p.Name)
	}
}

func TestGetPlaylistTracks(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/playlists/pl-1":
			fmt.Fprint(w, `{"title":"Mock Playlist Alpha"}`)
		case r.URL.Path == "/playlists/pl-1/tracks":
			fmt.Fprintf(w, `{"items":[%s],"totalNumberOfItems":1}`, trackItem(71, "Mock Playlist Track"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})

	tracks, name, err := api.GetPlaylistTracks(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("GetPlaylistTracks failed: %v", err)
	}
	if name != "Mock Playlist Alpha" {
		t.Errorf("name = %q, want Mock Playlist Alpha", name)
	}
	if len(tracks) != 1 || tracks[0].Title != "Mock Playlist Track" {
		t.Errorf("tracks = %+v", tracks)
	}
}

// ─── doRequest ──────────────────────────────────────────────────────────

func TestDoRequestAuthAndCountry(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		if r.URL.Query().Get("countryCode") != "US" {
			t.Errorf("countryCode = %q, want US", r.URL.Query().Get("countryCode"))
		}
		fmt.Fprint(w, `{"ok":true}`)
	})

	_, err := api.doRequest(context.Background(), http.MethodGet, api.v1BaseURL+"/albums/1", nil)
	if err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}
}

func TestDoRequestNoTokenNoAuthHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := newAPI(server.URL, server.URL, 5*time.Second, time.Millisecond)
	if _, err := c.doRequest(context.Background(), http.MethodGet, c.v1BaseURL+"/albums/1", nil); err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}
}

func TestDoRequest429RetrySucceeds(t *testing.T) {
	attempts := 0
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})

	if _, err := api.doRequest(context.Background(), http.MethodGet, api.v1BaseURL+"/albums/1", nil); err != nil {
		t.Fatalf("doRequest failed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

func TestDoRequest429ExhaustsRetries(t *testing.T) {
	attempts := 0
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := api.doRequest(context.Background(), http.MethodGet, api.v1BaseURL+"/albums/1", nil)
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error = %q, want 429 mention", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestDoRequestErrorBody(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"errors":[{"detail":"mock invalid request"}],"userMessage":"nope"}`)
	})

	_, err := api.doRequest(context.Background(), http.MethodGet, api.v1BaseURL+"/albums/1", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "mock invalid request") {
		t.Errorf("error = %q, want detail from error body", err)
	}
}

func TestDoRequestPlainErrorBody(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `boom`)
	})

	_, err := api.doRequest(context.Background(), http.MethodGet, api.v1BaseURL+"/albums/1", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want HTTP 500", err)
	}
}

func TestDoRequestInvalidJSONBody(t *testing.T) {
	api, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not-json`)
	})

	_, err := api.SearchTracks(context.Background(), "Mock", 5, 0)
	if err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("err = %v, want unmarshal error", err)
	}
}

// ─── Misc helpers ───────────────────────────────────────────────────────

func TestClamp(t *testing.T) {
	if got := clamp(0, 1, 300); got != 1 {
		t.Errorf("clamp(0,1,300) = %d, want 1", got)
	}
	if got := clamp(500, 1, 300); got != 300 {
		t.Errorf("clamp(500,1,300) = %d, want 300", got)
	}
	if got := clamp(50, 1, 300); got != 50 {
		t.Errorf("clamp(50,1,300) = %d, want 50", got)
	}
}
