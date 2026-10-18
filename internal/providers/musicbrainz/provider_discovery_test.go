package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/metadata"
)

// newDiscoveryClient builds a Client whose API layer points at a mock server.
// Unlike newResolverClient (exact path -> body) the handler here may inspect the
// full request, so the release-group query parameters and the number of requests
// can be asserted.
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
		writeJSON(w, `{"release-groups":[{"id":"rg-1","title":"Nevermind","first-release-date":"1991-09-24","primary-type":"Album","artist-credit":[{"name":"Nirvana","joinphrase":""}]}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Text: "Nirvana Nevermind"}, 5)
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

func TestSearchAlbums_StructuredInputSingleSearch(t *testing.T) {
	// The catalog carries artist and title separately, so MusicBrainz must search
	// them in ONE release-group query — no boundary guessing and no word cap. A
	// 4-word artist ("Red Hot Chili Peppers") is exactly the case the old
	// joined-string + 3-word-cap split used to break. The title term keeps its
	// case/punctuation (matching.TrimEditionSuffix only drops an edition suffix)
	// so MusicBrainz tokenizes the verbatim title.
	var calls int
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		calls++
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[{"id":"rg-ds","title":"Desecration Smile","first-release-date":"2007-02-20","primary-type":"Single"}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{
		Artist: "Red Hot Chili Peppers",
		Title:  "Desecration Smile",
	}, 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want exactly 1 (structured input must not split)", calls)
	}
	if !strings.Contains(sawQuery, `artist:"Red Hot Chili Peppers"`) {
		t.Errorf("query = %q, want the exact 4-word artist (no word cap)", sawQuery)
	}
	if !strings.Contains(sawQuery, `releasegroup:"Desecration Smile"`) {
		t.Errorf("query = %q, want the title term with case/punctuation preserved", sawQuery)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].ProviderID != "rg-ds" {
		t.Errorf("ProviderID = %q, want rg-ds", results[0].ProviderID)
	}
	if results[0].ArtistName != "Red Hot Chili Peppers" {
		t.Errorf("ArtistName = %q, want Red Hot Chili Peppers", results[0].ArtistName)
	}
	if results[0].Title != "Desecration Smile" {
		t.Errorf("Title = %q, want Desecration Smile", results[0].Title)
	}
}

func TestSearchAlbums_StructuredInputNoMatch(t *testing.T) {
	// Structured input with no release-group hit returns nil, not an error, and
	// never falls back to candidate splitting.
	var calls int
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, `{"release-groups":[]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{
		Artist: "Red Hot Chili Peppers",
		Title:  "Desecration Smile",
	}, 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if results != nil {
		t.Errorf("results = %+v, want nil", results)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want exactly 1", calls)
	}
}

func TestSearchAlbums_FreeTextSingleBareQuery(t *testing.T) {
	// Free-text (Discover UI) callers get ONE bare release-group search built from
	// the raw text: no artist/title boundary guessing, no word cap, and no
	// repeated split requests.
	var calls int
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		calls++
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[{"id":"rg-things","title":"Turned Away","primary-type":"Single"}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Text: "The Things Turned Away"}, 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want exactly 1 (single bare free-text query)", calls)
	}
	if sawQuery != "The Things Turned Away" {
		t.Errorf("query = %q, want the escaped raw text as a bare term", sawQuery)
	}
	if strings.Contains(sawQuery, "artist:") || strings.Contains(sawQuery, "releasegroup:") {
		t.Errorf("query = %q, want a bare general term (no artist/title field guessing)", sawQuery)
	}
	if len(results) != 1 || results[0].ProviderID != "rg-things" {
		t.Fatalf("results = %+v, want one rg-things", results)
	}
}

func TestSearchAlbums_FreeTextDoesNotGuessFields(t *testing.T) {
	// A dash in free text is NOT treated as an artist/title boundary anymore: the
	// Discover UI text is sent as one bare term, so the request never carries
	// artist:/releasegroup: fields.
	var calls int
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		calls++
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[{"id":"rg-d","title":"Discovery","primary-type":"Album"}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Text: "Daft Punk - Discovery"}, 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want exactly 1", calls)
	}
	if strings.Contains(sawQuery, `artist:"`) || strings.Contains(sawQuery, `releasegroup:"`) {
		t.Errorf("query = %q, want a bare term with no field guessing", sawQuery)
	}
	if !strings.Contains(sawQuery, "Daft Punk") || !strings.Contains(sawQuery, "Discovery") {
		t.Errorf("query = %q, want the raw text preserved", sawQuery)
	}
	if len(results) != 1 || results[0].ProviderID != "rg-d" {
		t.Fatalf("results = %+v, want one rg-d", results)
	}
}

func TestSearchAlbums_NoMatch(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"release-groups":[]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Text: "Some Unknown Album"}, 5)
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

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Text: "   "}, 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if results != nil {
		t.Errorf("results = %+v, want nil", results)
	}
}

func TestSearchAlbums_RateLimited(t *testing.T) {
	// A genuine throttle carries a Retry-After header → metadata.ErrRateLimited
	// so the shared cooldown parks the provider (AGENTS §8).
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer cleanup()

	_, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Text: "Nirvana Nevermind"}, 5)
	if err == nil {
		t.Fatal("expected rate-limit error, got nil")
	}
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v, want metadata.ErrRateLimited", err)
	}
}

func TestSearchAlbums_BusyNoRetryAfterIsTransient(t *testing.T) {
	// A 503 with NO Retry-After is MusicBrainz's transient "server busy" blip,
	// not a throttle: it must surface as metadata.ErrProviderBusy so the whole
	// refresh batch is not frozen behind a parked cooldown (AGENTS §8).
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer cleanup()

	_, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Text: "Nirvana Nevermind"}, 5)
	if !errors.Is(err, metadata.ErrProviderBusy) {
		t.Fatalf("error = %v, want metadata.ErrProviderBusy", err)
	}
	if errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("error = %v must NOT be ErrRateLimited (would park the shared cooldown)", err)
	}
}

func TestSearchAlbums_FreeTextHonorsLimit(t *testing.T) {
	// Free-text is one request; the caller's limit is plumbed to MusicBrainz and
	// the returned ranked window is capped at that limit.
	var sawLimit string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		sawLimit = r.URL.Query().Get("limit")
		writeJSON(w, `{"release-groups":[
			{"id":"rg-a","title":"First","primary-type":"Album"},
			{"id":"rg-b","title":"Second","primary-type":"Album"},
			{"id":"rg-c","title":"Third","primary-type":"Album"}
		]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Text: "Alpha Beta Gamma Delta"}, 2)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if sawLimit != "2" {
		t.Errorf("limit param = %q, want 2", sawLimit)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (limit honored)", len(results))
	}
	if results[0].ProviderID != "rg-a" || results[1].ProviderID != "rg-b" {
		t.Errorf("results = %+v, want rg-a then rg-b", results)
	}
}

func TestSearchAlbums_StructuredStripsFormatSuffix(t *testing.T) {
	// A library title carrying a delimited format suffix must query the bare
	// release group the provider actually lists, or "Échame la culpa - Single"
	// never returns its release group and no matcher can save it. The releasegroup
	// term is matching.TrimEditionSuffix of the title (suffix dropped, case and
	// accent kept); the artist term stays explicit and the suffix must not leak
	// into the query.
	var calls int
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		calls++
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[{"id":"rg-elc","title":"Échame La Culpa","first-release-date":"2017-11-17","primary-type":"Single"}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{
		Artist: "Luis Fonsi",
		Title:  "Échame la culpa - Single",
	}, 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want exactly 1", calls)
	}
	if !strings.Contains(sawQuery, `artist:"Luis Fonsi"`) {
		t.Errorf("query = %q, want the explicit artist term", sawQuery)
	}
	if !strings.Contains(sawQuery, `releasegroup:"Échame la culpa"`) {
		t.Errorf("query = %q, want the suffix-stripped title term (case/accent kept)", sawQuery)
	}
	if strings.Contains(strings.ToLower(sawQuery), "single") {
		t.Errorf("query = %q, must not contain the format suffix %q", sawQuery, "single")
	}
	if len(results) != 1 || results[0].ProviderID != "rg-elc" {
		t.Fatalf("results = %+v, want one rg-elc", results)
	}
}

// TestSearchAlbums_StructuredPreservesPunctuation pins the reason the query term
// uses TrimEditionSuffix rather than NormalizeTitle: the title reaches
// MusicBrainz with its case and punctuation intact (only Lucene-escaped), while a
// trailing format suffix is still stripped. Titles like "Don't Stop", "R&B" and
// "AC/DC" must not be reduced to a lower-cased, punctuation-free key.
func TestSearchAlbums_StructuredPreservesPunctuation(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		wantTerm string
	}{
		{"apostrophe kept, single stripped", "Don't Stop - Single", `releasegroup:"Don't Stop"`},
		{"slash kept (Lucene-escaped)", "AC/DC", `releasegroup:"AC\/DC"`},
		{"ampersand kept (Lucene-escaped)", "R&B", `releasegroup:"R\&B"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			if _, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Artist: "Artist", Title: tc.title}, 5); err != nil {
				t.Fatalf("SearchAlbums: %v", err)
			}
			if !strings.Contains(sawQuery, tc.wantTerm) {
				t.Errorf("query = %q, want it to contain %q (punctuation preserved)", sawQuery, tc.wantTerm)
			}
			if strings.Contains(strings.ToLower(sawQuery), "single") {
				t.Errorf("query = %q, must not leak the stripped format suffix", sawQuery)
			}
		})
	}
}

// TestSearchAlbums_TitleOnlySearchesReleaseGroup covers the empty/unknown-artist
// regression: with a title but no artist the structured branch must still run and
// send releasegroup:"…" alone (no artist: term, no empty query).
func TestSearchAlbums_TitleOnlySearchesReleaseGroup(t *testing.T) {
	var calls int
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		calls++
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[{"id":"rg-t","title":"Untitled","primary-type":"Album"}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Title: "Untitled"}, 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want exactly 1 (title-only must still search)", calls)
	}
	if sawQuery != `releasegroup:"Untitled"` {
		t.Errorf("query = %q, want a bare releasegroup term with no artist field", sawQuery)
	}
	if strings.Contains(sawQuery, "artist:") {
		t.Errorf("query = %q, must not include an artist term when the artist is empty", sawQuery)
	}
	if len(results) != 1 || results[0].ProviderID != "rg-t" {
		t.Fatalf("results = %+v, want one rg-t", results)
	}
}

// TestSearchAlbums_ArtistOnlyFallsBackToBareQuery covers the artist-only case:
// with an artist but no title there is no releasegroup term to search on, so the
// provider must fall back to the same rendered phrase the other discovery
// providers use (q.Query() == the artist) as a single bare query instead of
// silently returning nothing.
func TestSearchAlbums_ArtistOnlyFallsBackToBareQuery(t *testing.T) {
	var calls int
	var sawQuery string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		calls++
		sawQuery = r.URL.Query().Get("query")
		writeJSON(w, `{"release-groups":[{"id":"rg-a","title":"Artist","primary-type":"Album"}]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Artist: "Nils Frahm"}, 5)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want exactly 1 (artist-only must still search)", calls)
	}
	if sawQuery != "Nils Frahm" {
		t.Errorf("query = %q, want the bare rendered artist phrase", sawQuery)
	}
	if len(results) != 1 || results[0].ProviderID != "rg-a" {
		t.Fatalf("results = %+v, want one rg-a", results)
	}
}

// TestSearchAlbums_ClampsHugeLimit guards against a caller-supplied huge limit
// forcing a huge pre-allocation (and an over-cap API request): the limit is
// clamped to maxReleaseGroupLimit before any slice is sized from it.
func TestSearchAlbums_ClampsHugeLimit(t *testing.T) {
	var sawLimit string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		sawLimit = r.URL.Query().Get("limit")
		writeJSON(w, `{"release-groups":[{"id":"rg-a","title":"One","primary-type":"Album"}]}`)
	})
	defer cleanup()

	if _, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{Artist: "A", Title: "One"}, 10_000_000); err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if sawLimit != "100" {
		t.Errorf("limit param = %q, want 100 (clamped to maxReleaseGroupLimit)", sawLimit)
	}
}

func TestSearchAlbums_RateLimitedPartialResultsPropagateSentinel(t *testing.T) {
	// A rate limit mid-stream must ALWAYS propagate metadata.ErrRateLimited so the
	// caller can park the provider app-wide (AGENTS §8) — even when results were
	// already collected. Returning (results, nil) here is exactly the bug this
	// pins: it hid the sentinel and defeated the shared cooldown.
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "first" {
			writeJSON(w, `{"release-groups":[{"id":"rg-1","title":"One","primary-type":"Album"}]}`)
			return
		}
		// A genuine throttle (Retry-After) mid-stream must still propagate the
		// rate-limit sentinel even with partial results.
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer cleanup()

	search := func(ctx context.Context, term string) ([]ReleaseGroupResult, error) {
		return client.api.SearchReleaseGroupsQuery(ctx, term, 5)
	}
	results, err := client.collectRankedAlbums(context.Background(), "", []string{"first", "second"}, search, 5)
	if err == nil {
		t.Fatal("collectRankedAlbums error = nil, want the rate-limit sentinel to propagate")
	}
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v, want metadata.ErrRateLimited", err)
	}
	if len(results) != 1 || results[0].ProviderID != "rg-1" {
		t.Errorf("results = %+v, want the partial result collected before the rate limit", results)
	}
}

func TestSearchAlbums_NonRateLimitErrorKeepsPartialResults(t *testing.T) {
	// A non-rate-limit transient error (here HTTP 500) stays best-effort: the
	// accumulated results are returned with a nil error rather than discarded.
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "first" {
			writeJSON(w, `{"release-groups":[{"id":"rg-1","title":"One","primary-type":"Album"}]}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()

	search := func(ctx context.Context, term string) ([]ReleaseGroupResult, error) {
		return client.api.SearchReleaseGroupsQuery(ctx, term, 5)
	}
	results, err := client.collectRankedAlbums(context.Background(), "", []string{"first", "second"}, search, 5)
	if err != nil {
		t.Fatalf("collectRankedAlbums: %v (want partial results, not an error)", err)
	}
	if len(results) != 1 || results[0].ProviderID != "rg-1" {
		t.Fatalf("results = %+v, want the one result collected before the failure", results)
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
	if first.ProviderID != "rec-1" {
		t.Errorf("ProviderID = %q, want the recording MBID rec-1", first.ProviderID)
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

// ─── SearchAlbums recall regression ────────────────────────────────────

func TestSearchAlbums_StructuredRecallKeepsAllRankedCandidates(t *testing.T) {
	// Regression: a karaoke/tribute release group can outrank the real release
	// in MusicBrainz's scoring, so returning only the top hit left the album
	// unmatched. SearchAlbums must return the full ranked list — and plumb the
	// caller's limit through — so refresh.bestMatch can pick the exact match.
	var sawLimit string
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			http.NotFound(w, r)
			return
		}
		sawLimit = r.URL.Query().Get("limit")
		writeJSON(w, `{"count":2,"release-groups":[
			{"id":"rg-karaoke","title":"Unconditionally (Karaoke Version)","first-release-date":"2013","primary-type":"Single","artist-credit":[{"name":"Karaoke Star","joinphrase":""}]},
			{"id":"rg-kp","title":"Unconditionally","first-release-date":"2013-10-22","primary-type":"Single","artist-credit":[{"name":"Katy Perry","joinphrase":""}]}
		]}`)
	})
	defer cleanup()

	results, err := client.SearchAlbums(context.Background(), discovery.AlbumQuery{
		Artist: "Katy Perry",
		Title:  "Unconditionally",
	}, 20)
	if err != nil {
		t.Fatalf("SearchAlbums: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (full ranked list, not just the top hit)", len(results))
	}
	if results[0].ProviderID != "rg-karaoke" || results[1].ProviderID != "rg-kp" {
		t.Errorf("order = [%s %s], want [rg-karaoke rg-kp] (rank-preserving)", results[0].ProviderID, results[1].ProviderID)
	}
	if results[1].ArtistName != "Katy Perry" {
		t.Errorf("results[1].ArtistName = %q, want Katy Perry (release group's own credit)", results[1].ArtistName)
	}
	if sawLimit != "20" {
		t.Errorf("limit param = %q, want 20 (caller's limit plumbed through)", sawLimit)
	}

	// The exact match must be present for bestMatch to select it.
	var exact bool
	for _, r := range results {
		if r.ProviderID == "rg-kp" && r.Title == "Unconditionally" && r.ArtistName == "Katy Perry" {
			exact = true
			break
		}
	}
	if !exact {
		t.Errorf("exact Katy Perry/Unconditionally candidate missing from %+v", results)
	}
}
