package tidal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/metadata"
)

func newTestIdentityClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	api, _ := newTestAPI(t, handler)
	return &Client{
		api:     api,
		streams: newStreamClient(api),
		log:     discardLogger(),
		token:   tidalToken{AccessToken: "test-token"},
	}
}

func TestGetTrackIdentityMapsAlbumContext(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/123":
			fmt.Fprint(w, `{"id":123,"title":"Song","duration":214,"trackNumber":4,"volumeNumber":1,"isrc":"CH4881102606","artist":{"id":1,"name":"DJ Antoine"},"album":{"id":10,"title":"WOW"}}`)
		case "/albums/10":
			fmt.Fprint(w, `{"id":10,"title":"WOW","type":"ALBUM","numTracks":17}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	id, err := client.GetTrackIdentity(context.Background(), "123")
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v", err)
	}
	if id == nil {
		t.Fatal("GetTrackIdentity returned nil")
	}
	if id.ProviderName != pluginName || id.ProviderTrackID != "123" || id.ProviderAlbumID != "10" {
		t.Errorf("provider identity = %+v, want tidal/123 album 10", id)
	}
	if id.AlbumTitle != "WOW" || id.AlbumType != "album" || id.AlbumTrackCount != 17 {
		t.Errorf("album context = %+v, want WOW/album/17", id)
	}
	if id.TrackNumber != 4 || id.DiscNumber != 1 || id.DurationMs != 214000 || id.ISRC != "CH4881102606" {
		t.Errorf("track context = %+v", id)
	}
	if id.ArtistName != "DJ Antoine" {
		t.Errorf("artist = %q, want DJ Antoine", id.ArtistName)
	}
}

func TestGetTrackIdentityPropagatesAlbumContextCancellation(t *testing.T) {
	var cancel context.CancelFunc
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/123":
			fmt.Fprint(w, `{"id":123,"title":"Song","duration":214,"trackNumber":4,"volumeNumber":1,"isrc":"CH4881102606","artist":{"id":1,"name":"DJ Antoine"},"album":{"id":10,"title":"WOW"}}`)
		case "/albums/10":
			cancel()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
				t.Errorf("album request context was not canceled")
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	defer stop()

	id, err := client.GetTrackIdentity(ctx, "123")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetTrackIdentity error = %v, want context.Canceled", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil after cancellation", id)
	}
}

func TestGetTrackIdentityKeepsIdentityWhenAlbumContextFails(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/123":
			fmt.Fprint(w, `{"id":123,"title":"Song","duration":214,"trackNumber":4,"volumeNumber":1,"isrc":"CH4881102606","artist":{"id":1,"name":"DJ Antoine"},"album":{"id":10,"title":"WOW"}}`)
		case "/albums/10":
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	id, err := client.GetTrackIdentity(context.Background(), "123")
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v", err)
	}
	if id == nil || id.ProviderTrackID != "123" || id.ProviderAlbumID != "10" {
		t.Fatalf("identity = %+v, want the track identity without album context", id)
	}
	if id.AlbumTrackCount != 0 || id.AlbumType != "" {
		t.Fatalf("album context = %+v, want unknown context", id)
	}
}

// Tidal surfaces 5xx as a plain httpStatusError rather than metadata.ErrProviderBusy;
// this test locks the optional-context contract for that path.
func TestGetTrackIdentityKeepsIdentityWhenAlbumBusy(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/123":
			fmt.Fprint(w, `{"id":123,"title":"Song","duration":214,"trackNumber":4,"volumeNumber":1,"isrc":"CH4881102606","artist":{"id":1,"name":"DJ Antoine"},"album":{"id":10,"title":"WOW"}}`)
		case "/albums/10":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	id, err := client.GetTrackIdentity(context.Background(), "123")
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v, want the track identity when optional album context is busy", err)
	}
	if id == nil || id.ProviderTrackID != "123" || id.ProviderAlbumID != "10" {
		t.Fatalf("identity = %+v, want the track identity without album context", id)
	}
	if id.AlbumTrackCount != 0 || id.AlbumType != "" {
		t.Fatalf("album context = %+v, want unknown context", id)
	}
}

func TestGetTrackIdentityPropagatesAlbumRateLimit(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks/123":
			fmt.Fprint(w, `{"id":123,"title":"Song","duration":214,"trackNumber":4,"volumeNumber":1,"isrc":"CH4881102606","artist":{"id":1,"name":"DJ Antoine"},"album":{"id":10,"title":"WOW"}}`)
		case "/albums/10":
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	id, err := client.GetTrackIdentity(context.Background(), "123")
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("GetTrackIdentity error = %v, want metadata.ErrRateLimited", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil on a rate limit", id)
	}
}

func TestLookupTrackByISRCParksSameAlbumTrackClash(t *testing.T) {
	albumCalls := 0
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks":
			fmt.Fprint(w, `{"items":[
				{"id":1,"title":"One","duration":200,"isrc":"CH4881102606","album":{"id":10,"title":"WOW"}},
				{"id":2,"title":"Two","duration":200,"isrc":"CH4881102606","album":{"id":10,"title":"WOW"}}
			],"totalNumberOfItems":2}`)
		case "/albums/10":
			albumCalls++
			fmt.Fprint(w, `{"id":10,"title":"WOW","type":"ALBUM","numTracks":17}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	id, err := client.LookupTrackByISRC(context.Background(), "CH4881102606")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil for a same-album track clash", id)
	}
	if albumCalls != 0 {
		t.Fatalf("album calls = %d, want 0 for an ambiguous ISRC", albumCalls)
	}
}

func TestGetTrackIdentityNotFound(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	id, err := client.GetTrackIdentity(context.Background(), "999")
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil", id)
	}
}

func TestGetTrackIdentityInvalidID(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL.Path)
	})

	id, err := client.GetTrackIdentity(context.Background(), "not-a-number")
	if !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("GetTrackIdentity error = %v, want discovery.ErrNotFound", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil", id)
	}
}

func TestLookupTrackByISRCVerifiesExactISRC(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tracks":
			if got := r.URL.Query().Get("isrc"); got != "CH4881102606" {
				t.Errorf("isrc query = %q, want CH4881102606", got)
			}
			fmt.Fprint(w, `{"items":[
				{"id":1,"title":"Wrong","duration":200,"isrc":"OTHER","album":{"id":10,"title":"WOW"}},
				{"id":2,"title":"Song","duration":214,"trackNumber":4,"volumeNumber":1,"isrc":"CH4881102606","artist":{"id":3,"name":"DJ Antoine"},"album":{"id":10,"title":"WOW"}}
			],"totalNumberOfItems":2}`)
		case "/albums/10":
			fmt.Fprint(w, `{"id":10,"title":"WOW","type":"ALBUM","numTracks":17}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	id, err := client.LookupTrackByISRC(context.Background(), "CH-488-11-02606")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id == nil || id.ProviderTrackID != "2" {
		t.Fatalf("identity = %+v, want the exact-ISRC track id 2", id)
	}
	if id.ProviderAlbumID != "10" || id.AlbumTitle != "WOW" || id.AlbumTrackCount != 17 || id.AlbumType != "album" {
		t.Errorf("album context = %+v", id)
	}
	if id.DurationMs != 214000 || id.ISRC != "CH4881102606" || id.TrackNumber != 4 {
		t.Errorf("track context = %+v", id)
	}
}

func TestLookupTrackByISRCNoExactMatch(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":1,"title":"Wrong","isrc":"OTHER","album":{"id":10,"title":"WOW"}}],"totalNumberOfItems":1}`)
	})

	id, err := client.LookupTrackByISRC(context.Background(), "CH4881102606")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil", id)
	}
}

func TestLookupTrackByISRCParksAmbiguousAlbums(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tracks" {
			t.Errorf("unexpected path %s (must not fetch one ambiguous album)", r.URL.Path)
		}
		fmt.Fprint(w, `{"items":[
			{"id":1,"title":"Song","duration":214,"isrc":"CH4881102606","album":{"id":10,"title":"Album"}},
			{"id":2,"title":"Song","duration":214,"isrc":"CH4881102606","album":{"id":11,"title":"Compilation"}}
		],"totalNumberOfItems":2}`)
	})

	id, err := client.LookupTrackByISRC(context.Background(), "CH4881102606")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil for two exact-ISRC albums", id)
	}
}

func TestGetAlbumTracksMapsNotFound(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	_, err := client.GetAlbumTracks(context.Background(), "10")
	if !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("err = %v, want discovery.ErrNotFound", err)
	}
}

func TestLookupTrackByISRCRequiresAlbumContext(t *testing.T) {
	client := newTestIdentityClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":1,"title":"Song","isrc":"CH4881102606","album":{"id":0,"title":""}}],"totalNumberOfItems":1}`)
	})

	id, err := client.LookupTrackByISRC(context.Background(), "CH4881102606")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil without a materializable album", id)
	}
}
