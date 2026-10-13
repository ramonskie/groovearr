package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/metadata"
)

// newDiscoveryClient builds a Client whose API layer points at a mock server.
// Unlike newResolverClient (exact path -> body) the handler here may inspect the
// full request, so the ambiguous "artist title" split and query parameters can
// be asserted.
func newDiscoveryClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()

	srv := httptest.NewServer(handler)
	api := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0,
	}
	return &Client{cfg: MusicBrainzConfig{}, api: api, log: testLogger()}, srv.Close
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, body)
}

// ─── SearchAlbums ──────────────────────────────────────────────────────

func TestSearchAlbums_MapsReleaseGroup(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, `{"release-groups":[{"id":"rg-1","title":"Nevermind","first-release-date":"1991-09-24","primary-type":"Album"}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), "Nirvana Nevermind", 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	got := results[0]
	if got.ProviderID != "rg-1" {
		t.Errorf("ProviderID = %q, want rg-1 (release-group MBID)", got.ProviderID)
	}
	if got.ProviderName != "musicbrainz" {
		t.Errorf("ProviderName = %q, want musicbrainz", got.ProviderName)
	}
	if got.Title != "Nevermind" {
		t.Errorf("Title = %q, want Nevermind", got.Title)
	}
	if got.ArtistName != "Nirvana" {
		t.Errorf("ArtistName = %q, want Nirvana", got.ArtistName)
	}
	if got.Year != 1991 {
		t.Errorf("Year = %d, want 1991", got.Year)
	}
	if got.Type != "album" {
		t.Errorf("Type = %q, want album (lower-cased primary type)", got.Type)
	}
}

func TestSearchAlbums_MultiWordArtist(t *testing.T) {
	// The query is the space-joined "artist title" the catalog engine builds, so
	// the split is ambiguous. Only the 2-word artist split exists on the server;
	// SearchAlbums must try it after the 1-word split comes back empty.
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query().Get("query")
		if strings.Contains(q, `artist:"The Things"`) {
			writeJSON(w, `{"release-groups":[{"id":"rg-things","title":"Turned Away","primary-type":"Single"}]}`)
			return
		}
		writeJSON(w, `{"release-groups":[]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), "The Things Turned Away", 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].ProviderID != "rg-things" {
		t.Errorf("ProviderID = %q, want rg-things", results[0].ProviderID)
	}
	if results[0].ArtistName != "The Things" {
		t.Errorf("ArtistName = %q, want The Things", results[0].ArtistName)
	}
}

func TestSearchAlbums_ExplicitSeparator(t *testing.T) {
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[{"id":"rg-d","title":"Discovery","primary-type":"Album"}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), "Daft Punk - Discovery", 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(results) != 1 || results[0].ProviderID != "rg-d" {
		t.Fatalf("results = %+v, want one rg-d", results)
	}
	if !strings.Contains(sawQuery, `artist:"Daft Punk"`) || !strings.Contains(sawQuery, `releasegroup:"Discovery"`) {
		t.Errorf("query = %q, want explicit artist/title split on the dash", sawQuery)
	}
}

func TestSearchAlbums_NoMatch(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"release-groups":[]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), "Some Unknown Album", 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if results != nil {
		t.Errorf("results = %+v, want nil", results)
	}
}

func TestSearchAlbums_EmptyQuery(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for an empty query")
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), "   ", 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if results != nil {
		t.Errorf("results = %+v, want nil", results)
	}
}

func TestSearchAlbums_RateLimited(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer cleanup()

	_, err := client.SearchAlbums(context.Background(), "Nirvana Nevermind", 5)
	if err == nil {
		t.Fatal("expected rate-limit error, got nil")
	}
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v, want metadata.ErrRateLimited", err)
	}
}

func TestSearchAlbums_HonorsLimit(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query().Get("query")
		switch {
		case strings.Contains(q, `artist:"Alpha Beta Gamma"`):
			writeJSON(w, `{"release-groups":[{"id":"rg-c","title":"Third","primary-type":"Album"}]}`)
		case strings.Contains(q, `artist:"Alpha Beta"`):
			writeJSON(w, `{"release-groups":[{"id":"rg-b","title":"Second","primary-type":"Album"}]}`)
		case strings.Contains(q, `artist:"Alpha"`):
			writeJSON(w, `{"release-groups":[{"id":"rg-a","title":"First","primary-type":"Album"}]}`)
		default:
			writeJSON(w, `{"release-groups":[]}`)
		}
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), "Alpha Beta Gamma Delta", 2)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (limit honored)", len(results))
	}
	if results[0].ProviderID != "rg-a" || results[1].ProviderID != "rg-b" {
		t.Errorf("results = %+v, want rg-a then rg-b", results)
	}

	one, err := client.SearchAlbums(context.Background(), "Alpha Beta Gamma Delta", 1)
	if err != nil {
		t.Fatalf("SearchAlbums (limit 1): %v", err)
	}
	if len(one) != 1 || one[0].ProviderID != "rg-a" {
		t.Errorf("results = %+v, want exactly rg-a", one)
	}
}

func TestSearchAlbums_UnspacedSeparator(t *testing.T) {
	// A single token with no spaces around the dash has no word boundary; the
	// unspaced separator fallback must still produce the artist/title split.
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query().Get("query")
		if strings.Contains(q, `artist:"Nirvana"`) && strings.Contains(q, `releasegroup:"Nevermind"`) {
			writeJSON(w, `{"release-groups":[{"id":"rg-n","title":"Nevermind","primary-type":"Album"}]}`)
			return
		}
		writeJSON(w, `{"release-groups":[]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), "Nirvana-Nevermind", 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(results) != 1 || results[0].ProviderID != "rg-n" {
		t.Fatalf("results = %+v, want one rg-n", results)
	}
}

func TestAlbumSearchCandidates(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []albumQueryCandidate
	}{
		{
			name:  "spaced separator is authoritative",
			query: "Daft Punk - Discovery",
			want:  []albumQueryCandidate{{artist: "Daft Punk", title: "Discovery"}},
		},
		{
			name:  "space-joined tries word boundaries shortest artist first",
			query: "The Things Turned Away",
			want: []albumQueryCandidate{
				{artist: "The", title: "Things Turned Away"},
				{artist: "The Things", title: "Turned Away"},
				{artist: "The Things Turned", title: "Away"},
			},
		},
		{
			name:  "single token with unspaced separator",
			query: "Nirvana-Nevermind",
			want:  []albumQueryCandidate{{artist: "Nirvana", title: "Nevermind"}},
		},
		{
			name:  "longest sensible artist split added beyond the word cap",
			query: "Alpha Beta Gamma Delta Epsilon Zeta",
			want: []albumQueryCandidate{
				{artist: "Alpha", title: "Beta Gamma Delta Epsilon Zeta"},
				{artist: "Alpha Beta", title: "Gamma Delta Epsilon Zeta"},
				{artist: "Alpha Beta Gamma", title: "Delta Epsilon Zeta"},
				{artist: "Alpha Beta Gamma Delta Epsilon", title: "Zeta"},
			},
		},
		{
			name:  "single word yields no candidates",
			query: "Inception",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := albumSearchCandidates(tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("albumSearchCandidates(%q) = %#v, want %#v", tt.query, got, tt.want)
			}
		})
	}
}

// ─── GetAlbumTracks ────────────────────────────────────────────────────

func TestGetAlbumTracks_MapsTracks(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release-group/rg-1":
			writeJSON(w, `{"releases":[{"id":"rel-1","title":"Nevermind"}]}`)
		case "/release/rel-1":
			writeJSON(w, `{"id":"rel-1","title":"Nevermind","media":[{"tracks":[
				{"number":"1","title":"Smells Like Teen Spirit","length":301000,"recording":{"id":"rec-1","title":"Smells Like Teen Spirit","isrcs":["USGF19942501"],"artist-credit":[{"name":"Nirvana","joinphrase":""}]}},
				{"number":"2","title":"In Bloom","length":254000,"recording":{"id":"rec-2","title":"In Bloom","artist-credit":[]}}
			]}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	tracks, err := client.GetAlbumTracks(context.Background(), "rg-1")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	first := tracks[0]
	if first.ProviderID != "1" {
		t.Errorf("ProviderID = %q, want the release-scoped track number 1", first.ProviderID)
	}
	if first.ArtistName != "Nirvana" {
		t.Errorf("ArtistName = %q, want Nirvana", first.ArtistName)
	}
	if first.AlbumTitle != "Nevermind" {
		t.Errorf("AlbumTitle = %q, want Nevermind", first.AlbumTitle)
	}
	if first.Title != "Smells Like Teen Spirit" {
		t.Errorf("Title = %q, want Smells Like Teen Spirit", first.Title)
	}
	if first.TrackNumber != 1 || first.DiscNumber != 1 {
		t.Errorf("disc/track = %d/%d, want 1/1", first.DiscNumber, first.TrackNumber)
	}
	if first.DurationMs != 301000 {
		t.Errorf("DurationMs = %d, want 301000", first.DurationMs)
	}
	if first.ISRC != "USGF19942501" {
		t.Errorf("ISRC = %q, want USGF19942501", first.ISRC)
	}
}

func TestGetAlbumTracks_PicksOfficialReleaseDeterministically(t *testing.T) {
	var lookedUp string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release-group/rg-1":
			// A non-official edition is listed first; the official original
			// second. The representative release must be the official one.
			writeJSON(w, `{"primary-type":"Album","releases":[
				{"id":"rel-regional","title":"Nevermind","status":"Bootleg","date":"1990-01-01"},
				{"id":"rel-original","title":"Nevermind","status":"Official","date":"1991-09-24"}
			]}`)
		case "/release/rel-original":
			lookedUp = "rel-original"
			writeJSON(w, `{"id":"rel-original","title":"Nevermind","media":[{"position":1,"tracks":[
				{"number":"1","title":"Smells Like Teen Spirit","length":301000,"recording":{"id":"rec-1","title":"Smells Like Teen Spirit","artist-credit":[]}}
			]}]}`)
		case "/release/rel-regional":
			lookedUp = "rel-regional"
			writeJSON(w, `{"id":"rel-regional","title":"Nevermind","media":[]}`)
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	tracks, err := client.GetAlbumTracks(context.Background(), "rg-1")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if lookedUp != "rel-original" {
		t.Errorf("looked up release %q, want rel-original (official beats earlier bootleg)", lookedUp)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(tracks))
	}
}

func TestGetAlbumTracks_MultiDiscDiscNumbers(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release-group/rg-1":
			writeJSON(w, `{"primary-type":"Album","releases":[{"id":"rel-1","title":"Double Album","status":"Official","date":"2000-01-01"}]}`)
		case "/release/rel-1":
			writeJSON(w, `{"id":"rel-1","title":"Double Album","media":[
				{"position":1,"tracks":[
					{"number":"1","title":"Disc One A","length":1000,"recording":{"id":"r1","title":"Disc One A","artist-credit":[]}}
				]},
				{"position":2,"tracks":[
					{"number":"1","title":"Disc Two A","length":2000,"recording":{"id":"r2","title":"Disc Two A","artist-credit":[]}}
				]}
			]}`)
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	tracks, err := client.GetAlbumTracks(context.Background(), "rg-1")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	if tracks[0].DiscNumber != 1 || tracks[1].DiscNumber != 2 {
		t.Errorf("disc numbers = %d,%d, want 1,2 (real medium index)", tracks[0].DiscNumber, tracks[1].DiscNumber)
	}
	if tracks[0].TrackNumber != 1 || tracks[1].TrackNumber != 1 {
		t.Errorf("track numbers = %d,%d, want 1,1 (numbering restarts per disc)", tracks[0].TrackNumber, tracks[1].TrackNumber)
	}
}

func TestGetAlbumTracks_NoReleases(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"releases":[]}`)
	})
	defer cleanup()

	tracks, err := client.GetAlbumTracks(context.Background(), "rg-empty")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if tracks != nil {
		t.Errorf("tracks = %+v, want nil", tracks)
	}
}

func TestGetAlbumTracks_EmptyID(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for an empty id")
	})
	defer cleanup()

	tracks, err := client.GetAlbumTracks(context.Background(), "  ")
	if err != nil {
		t.Fatalf("GetAlbumTracks: %v", err)
	}
	if tracks != nil {
		t.Errorf("tracks = %+v, want nil", tracks)
	}
}

// ─── SearchArtists ─────────────────────────────────────────────────────

func TestSearchArtists_MapsMBIDs(t *testing.T) {
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artist/" {
			http.NotFound(w, r)
			return
		}
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"artists":[{"id":"mb-1","name":"Nirvana"},{"id":"mb-2","name":"Nirvana UK"}]}`)
	})
	defer cleanup()

	artists, err := client.SearchArtists(context.Background(), "Nirvana", 10)
	if err != nil {
		t.Fatalf("SearchArtists: %v", err)
	}
	if len(artists) != 2 {
		t.Fatalf("artists = %d, want 2", len(artists))
	}
	if artists[0].ProviderID != "mb-1" || artists[0].Name != "Nirvana" {
		t.Errorf("artists[0] = %+v, want mb-1/Nirvana", artists[0])
	}
	if artists[0].ProviderName != "musicbrainz" {
		t.Errorf("ProviderName = %q, want musicbrainz", artists[0].ProviderName)
	}
	if !strings.Contains(sawQuery, `artist:"Nirvana"`) {
		t.Errorf("query = %q, want an artist search", sawQuery)
	}
}

func TestSearchArtists_EmptyQuery(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for an empty query")
	})
	defer cleanup()

	artists, err := client.SearchArtists(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("SearchArtists: %v", err)
	}
	if artists != nil {
		t.Errorf("artists = %+v, want nil", artists)
	}
}

// ─── GetArtistAlbums ───────────────────────────────────────────────────

func TestGetArtistAlbums_ByArtistMBID(t *testing.T) {
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[
			{"id":"rg-1","title":"Nevermind","first-release-date":"1991-09-24","primary-type":"Album","artist-credit":[{"name":"Nirvana","joinphrase":""}]},
			{"id":"rg-2","title":"Bleach","first-release-date":"1989","primary-type":"Album","artist-credit":[{"name":"Nirvana","joinphrase":""}]}
		]}`)
	})
	defer cleanup()

	albums, err := client.GetArtistAlbums(context.Background(), "mb-1", 10)
	if err != nil {
		t.Fatalf("GetArtistAlbums: %v", err)
	}
	if len(albums) != 2 {
		t.Fatalf("albums = %d, want 2", len(albums))
	}
	if !strings.Contains(sawQuery, "arid:"+escapeLucene("mb-1")) {
		t.Errorf("query = %q, want an arid (escaped artist MBID) query", sawQuery)
	}
	first := albums[0]
	if first.ProviderID != "rg-1" || first.Title != "Nevermind" {
		t.Errorf("albums[0] = %+v, want rg-1/Nevermind", first)
	}
	if first.ArtistName != "Nirvana" {
		t.Errorf("ArtistName = %q, want Nirvana (from artist-credit)", first.ArtistName)
	}
	if first.Year != 1991 || first.Type != "album" {
		t.Errorf("year/type = %d/%q, want 1991/album", first.Year, first.Type)
	}
}

func TestGetArtistAlbums_EscapesArtistMBID(t *testing.T) {
	// Defense in depth: the MBID is server-derived but must still be escaped
	// before it is interpolated into the Lucene query.
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[]}`)
	})
	defer cleanup()

	if _, err := client.GetArtistAlbums(context.Background(), "mb-1*", 10); err != nil {
		t.Fatalf("GetArtistAlbums: %v", err)
	}
	if want := "arid:" + escapeLucene("mb-1*"); !strings.Contains(sawQuery, want) {
		t.Errorf("query = %q, want %q (Lucene metacharacters escaped)", sawQuery, want)
	}
}

func TestGetArtistAlbums_EmptyID(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for an empty id")
	})
	defer cleanup()

	albums, err := client.GetArtistAlbums(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("GetArtistAlbums: %v", err)
	}
	if albums != nil {
		t.Errorf("albums = %+v, want nil", albums)
	}
}
