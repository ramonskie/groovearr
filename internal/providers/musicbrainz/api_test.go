package musicbrainz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/metadata"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopWriter{}, nil))
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// recordingResp is a Recording as returned by MusicBrainz search.
type recordingResp struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Releases []releaseRef `json:"releases"`
}

type releaseRef struct {
	ID           string `json:"id"`
	ReleaseGroup *rgRef `json:"release-group,omitempty"`
}

type rgRef struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	PrimaryType string `json:"primary-type"`
}

// newTestClient creates an APIClient pointed at a mock server
// recordings response. The handler is called once per request — the server
// auto-closes after the test.
func newTestClient(t *testing.T, recordings []recordingResp) (*APIClient, func()) {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"recordings": recordings, "count": len(recordings)}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("mock encode error: %v", err)
		}
	})

	srv := httptest.NewServer(handler)

	// Disable rate limiting for tests.
	client := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0, // no rate limiting in tests
	}
	return client, srv.Close
}

// newTestClientWithStatus returns a mock server returning a specific HTTP status.
func newTestClientWithStatus(t *testing.T, status int) (*APIClient, func()) {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})

	srv := httptest.NewServer(handler)
	client := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0,
	}
	return client, srv.Close
}

// ─── Real-world song tests ──────────────────────────────────────────────

// TestSearchRecording_SeanPaul_Temperature simulates a track appearing on many
// compilations but only 2× on its original album "The Trinity". The aggregate
// frequency approach should pick the original.
func TestSearchRecording_SeanPaul_Temperature(t *testing.T) {
	// Simulate 30 recordings: 28 compilation-only releases + 2 "The Trinity" releases.
	recordings := []recordingResp{
		{ID: "r1", Title: "Temperature", Releases: releasesWithRG(
			[]string{"Hip Hop R&B Millenium III", "Album"},
		)},
		{ID: "r2", Title: "Temperature", Releases: releasesWithRG(
			[]string{"Summer Mix 2006", "Album"},
		)},
		{ID: "r3", Title: "Temperature", Releases: releasesWithRG(
			[]string{"Spring Break 2011", "Album"},
		)},
		{ID: "r4", Title: "Temperature", Releases: releasesWithRG(
			[]string{"10s HITS - 100 Greatest Songs", "Album"},
		)},
		{ID: "r5", Title: "Temperature", Releases: releasesWithRG(
			[]string{"New Year the Hits 2006-2007", "Album"},
			[]string{"The Trinity", "Album"}, // ← appears here
		)},
		{ID: "r6", Title: "Temperature", Releases: releasesWithRG(
			[]string{"Promo Only: Mainstream Radio", "Album"},
		)},
		{ID: "r7", Title: "Temperature", Releases: releasesWithRG(
			[]string{"100 Greatest 00s", "Album"},
		)},
		{ID: "r8", Title: "Temperature", Releases: releasesWithRG(
			[]string{"R&B Throwback", "Album"},
			[]string{"The Trinity", "Album"}, // ← appears here too
		)},
	}
	// Pad with more compilation-only recordings
	for i := 0; i < 20; i++ {
		recordings = append(recordings, recordingResp{
			ID:    "r-pad",
			Title: "Temperature",
			Releases: releasesWithRG(
				[]string{"Various Compilation " + itoa(i), "Album"},
			),
		})
	}

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Sean Paul", "Temperature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Title != "The Trinity" {
		t.Errorf("Title = %q, want %q", result.Title, "The Trinity")
	}
}

// TestSearchRecording_CrazyTown_Butterfly simulates a clean match where the
// original album appears consistently.
func TestSearchRecording_CrazyTown_Butterfly(t *testing.T) {
	recordings := []recordingResp{
		{ID: "r1", Title: "Butterfly", Releases: releasesWithRG(
			[]string{"The Gift of Game", "Album"},
		)},
		{ID: "r2", Title: "Butterfly", Releases: releasesWithRG(
			[]string{"The Gift of Game", "Album"},
		)},
		{ID: "r3", Title: "Butterfly", Releases: releasesWithRG(
			[]string{"Now That's What I Call Music! 6", "Album"},
		)},
		{ID: "r4", Title: "Butterfly", Releases: releasesWithRG(
			[]string{"The Gift of Game", "Album"},
		)},
		{ID: "r5", Title: "Butterfly", Releases: releasesWithRG(
			[]string{"Party Hits 2001", "Album"},
		)},
	}

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Crazy Town", "Butterfly")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Title != "The Gift of Game" {
		t.Errorf("Title = %q, want %q", result.Title, "The Gift of Game")
	}
}

// TestSearchRecording_DaftPunk_OneMoreTime simulates a track where the original
// album "Discovery" appears 24× deep in the results, but compilations dominate
// the top positions.
func TestSearchRecording_DaftPunk_OneMoreTime(t *testing.T) {
	recordings := make([]recordingResp, 0, 30)

	// 8 recordings with compilation release groups only
	compilations := []string{
		"Until One", "Vive La Révolution?", "Spinout 3", "Dance Top 100",
		"DJ Only: DJO 86", "Boom 2001", "Double Dancefloor", "Eden",
	}
	for _, c := range compilations {
		recordings = append(recordings, recordingResp{
			ID:    "rc-" + c,
			Title: "One More Time",
			Releases: releasesWithRG(
				[]string{c, "Album"},
			),
		})
	}

	// 22 recordings with "Discovery" release group
	for i := 0; i < 22; i++ {
		recordings = append(recordings, recordingResp{
			ID:    "rd-disc",
			Title: "One More Time",
			Releases: releasesWithRG(
				[]string{"Discovery", "Album"},
			),
		})
	}

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Daft Punk", "One More Time")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Title != "Discovery" {
		t.Errorf("Title = %q, want %q", result.Title, "Discovery")
	}
}

// TestSearchRecording_Nirvana_SmellsLikeTeenSpirit simulates a very
// compilation-heavy track where the original album "Nevermind" competes
// with live albums and bootlegs.
func TestSearchRecording_Nirvana_SmellsLikeTeenSpirit(t *testing.T) {
	recordings := []recordingResp{
		{ID: "r1", Title: "Smells Like Teen Spirit", Releases: releasesWithRG(
			[]string{"Nevermind", "Album"},
			[]string{"Nirvana", "Album"},
		)},
		{ID: "r2", Title: "Smells Like Teen Spirit", Releases: releasesWithRG(
			[]string{"Nevermind", "Album"},
		)},
		{ID: "r3", Title: "Smells Like Teen Spirit", Releases: releasesWithRG(
			[]string{"Live at Reading", "Album"},
		)},
		{ID: "r4", Title: "Smells Like Teen Spirit", Releases: releasesWithRG(
			[]string{"Nevermind", "Album"},
		)},
		{ID: "r5", Title: "Smells Like Teen Spirit", Releases: releasesWithRG(
			[]string{"From the Muddy Banks of the Wishkah", "Album"},
		)},
		{ID: "r6", Title: "Smells Like Teen Spirit", Releases: releasesWithRG(
			[]string{"Nevermind", "Album"},
		)},
		{ID: "r7", Title: "Smells Like Teen Spirit", Releases: releasesWithRG(
			[]string{"MTV Unplugged in New York", "Album"},
		)},
		{ID: "r8", Title: "Smells Like Teen Spirit", Releases: releasesWithRG(
			[]string{"1993-12-14: Salem Armory, Salem, OR, USA", "Album"},
		)},
	}

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Nirvana", "Smells Like Teen Spirit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Title != "Nevermind" {
		t.Errorf("Title = %q, want %q", result.Title, "Nevermind")
	}
}

// ─── Edge case tests ────────────────────────────────────────────────────

func TestSearchRecording_EmptyArtist(t *testing.T) {
	client, cleanup := newTestClient(t, nil)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "", "Butterfly")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for empty artist, got %+v", result)
	}
}

func TestSearchRecording_EmptyTitle(t *testing.T) {
	client, cleanup := newTestClient(t, nil)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Crazy Town", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for empty title, got %+v", result)
	}
}

func TestSearchRecording_EmptyResults(t *testing.T) {
	recordings := []recordingResp{} // empty

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Nonexistent", "Track")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for empty response, got %+v", result)
	}
}

func TestSearchRecording_HTTP503BusyIsTransient(t *testing.T) {
	// A 503 with NO Retry-After is MusicBrainz's transient "web server is
	// currently busy" blip, not a rate-limit: it must surface as
	// metadata.ErrProviderBusy so the shared cooldown is not parked (AGENTS §8).
	client, cleanup := newTestClientWithStatus(t, http.StatusServiceUnavailable)
	defer cleanup()

	_, err := client.SearchRecording(context.Background(), "Artist", "Title")
	if err == nil {
		t.Fatal("expected error for 503, got nil")
	}
	if !errors.Is(err, metadata.ErrProviderBusy) {
		t.Errorf("error = %v, want metadata.ErrProviderBusy", err)
	}
	if errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("error = %v must NOT be ErrRateLimited (would park the shared cooldown)", err)
	}
}

func TestSearchRecording_NoReleaseGroups(t *testing.T) {
	// Recordings exist but have no release-group data attached.
	recordings := []recordingResp{
		{ID: "r1", Title: "Track", Releases: []releaseRef{
			{ID: "rel1"}, // no ReleaseGroup
		}},
		{ID: "r2", Title: "Track", Releases: []releaseRef{
			{ID: "rel2"}, // no ReleaseGroup
		}},
	}

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Artist", "Title")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when no release groups, got %+v", result)
	}
}

func TestSearchRecording_FallbackToAnyType(t *testing.T) {
	// No Album-type release groups — should fall back to Single or EP.
	recordings := []recordingResp{
		{ID: "r1", Title: "Track", Releases: releasesWithRG(
			[]string{"The Single", "Single"},
		)},
		{ID: "r2", Title: "Track", Releases: releasesWithRG(
			[]string{"The Single", "Single"},
		)},
		{ID: "r3", Title: "Track", Releases: releasesWithRG(
			[]string{"The EP", "EP"},
		)},
	}

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Artist", "Title")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected fallback result, got nil")
	}
	// "The Single" appears 2×, "The EP" appears 1× — should pick Single.
	if result.Title != "The Single" {
		t.Errorf("Title = %q, want %q", result.Title, "The Single")
	}
}

func TestSearchRecording_DeduplicatesPerRecording(t *testing.T) {
	// Same release group listed twice for the same recording (different editions).
	// Should count as 1 per recording, not 2.
	recordings := []recordingResp{
		{ID: "r1", Title: "Track", Releases: []releaseRef{
			{ID: "rel1a", ReleaseGroup: &rgRef{ID: "rg1", Title: "Correct Album", PrimaryType: "Album"}},
			{ID: "rel1b", ReleaseGroup: &rgRef{ID: "rg1", Title: "Correct Album", PrimaryType: "Album"}}, // duplicate RG
		}},
		{ID: "r2", Title: "Track", Releases: releasesWithRG(
			[]string{"Wrong Album", "Album"},
		)},
	}

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Artist", "Title")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	// "Correct Album" counted 1× (deduplicated), "Wrong Album" counted 1×.
	// Both tied at 1 → alphabetical tiebreaker picks "Correct Album".
	if result.Title != "Correct Album" {
		t.Errorf("Title = %q, want %q", result.Title, "Correct Album")
	}
}

func TestSearchRecording_SingleRecordingSingleRelease(t *testing.T) {
	recordings := []recordingResp{
		{ID: "r1", Title: "The Song", Releases: releasesWithRG(
			[]string{"The Album", "Album"},
		)},
	}

	client, cleanup := newTestClient(t, recordings)
	defer cleanup()

	result, err := client.SearchRecording(context.Background(), "Artist", "The Song")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Title != "The Album" {
		t.Errorf("Title = %q, want %q", result.Title, "The Album")
	}
}

func TestSearchRecording_ContextCancelled(t *testing.T) {
	// Create a server that delays, then cancel the context.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := client.SearchRecording(ctx, "Artist", "Title")
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}

func TestSearchRecording_NonOKStatus(t *testing.T) {
	client, cleanup := newTestClientWithStatus(t, http.StatusInternalServerError)
	defer cleanup()

	_, err := client.SearchRecording(context.Background(), "Artist", "Title")
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestSearchRecording_InvalidJSON(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json {{{"))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0,
	}

	_, err := client.SearchRecording(context.Background(), "Artist", "Title")
	if err == nil {
		t.Fatal("expected unmarshal error, got nil")
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────

func releasesWithRG(pairs ...[]string) []releaseRef {
	var refs []releaseRef
	for _, p := range pairs {
		refs = append(refs, releaseRef{
			ID: "rel-" + p[0],
			ReleaseGroup: &rgRef{
				ID:          "rg-" + p[0],
				Title:       p[0],
				PrimaryType: p[1],
			},
		})
	}
	return refs
}

// ─── Artist canonical-name search ───────────────────────────────────────

type artistSearchResp struct {
	Count   int          `json:"count"`
	Artists []artistResp `json:"artists"`
}

type artistResp struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Score int    `json:"score"`
}

func newArtistTestClient(t *testing.T, artists []artistResp) (*APIClient, func()) {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artist/" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		resp := artistSearchResp{Count: len(artists), Artists: artists}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("mock encode error: %v", err)
		}
	})
	srv := httptest.NewServer(handler)
	client := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0,
	}
	return client, srv.Close
}

func TestSearchArtist_CanonicalSpelling(t *testing.T) {
	// MusicBrainz returns the canonical spelling; the case-mangled local name
	// must resolve to it.
	client, cleanup := newArtistTestClient(t, []artistResp{
		{ID: "mbid-1", Name: "Acda en de Munnik", Score: 100},
		{ID: "mbid-2", Name: "Acda en De Munnik", Score: 95},
	})
	defer cleanup()

	res, err := client.SearchArtist(context.Background(), "Acda en De Munnik")
	if err != nil {
		t.Fatalf("SearchArtist error: %v", err)
	}
	if res == nil {
		t.Fatal("expected a match, got nil")
	}
	if res.Name != "Acda en de Munnik" {
		t.Errorf("got %+v, want canonical 'Acda en de Munnik'", res)
	}
}

func TestSearchArtist_AccentedCanonical(t *testing.T) {
	// The local library may hold a de-accented variant ("Tiesto") while the
	// authoritative MusicBrainz spelling keeps the accent ("Tiësto"). The
	// match key (strutil.NormalizeName) folds both to "tiesto", so the query
	// must resolve to the accented canonical spelling — the whole point of the
	// unicode-variant duplicate grouping.
	client, cleanup := newArtistTestClient(t, []artistResp{
		{ID: "mbid-1", Name: "Tiësto", Score: 100},
		{ID: "mbid-2", Name: "Tiesto", Score: 90},
	})
	defer cleanup()

	res, err := client.SearchArtist(context.Background(), "Tiesto")
	if err != nil {
		t.Fatalf("SearchArtist error: %v", err)
	}
	if res == nil {
		t.Fatal("expected a match, got nil")
	}
	if res.Name != "Tiësto" {
		t.Errorf("got %+v, want canonical 'Tiësto' (accented)", res)
	}

	// The reverse direction must also work: query the accented variant, resolve
	// to the same canonical.
	res, err = client.SearchArtist(context.Background(), "Tiësto")
	if err != nil {
		t.Fatalf("SearchArtist error: %v", err)
	}
	if res == nil || res.Name != "Tiësto" {
		t.Errorf("accented query: got %+v, want 'Tiësto'", res)
	}
}

func TestCanonicalArtistName_Accented(t *testing.T) {
	// End-to-end through the provider wrapper: the duplicates job calls
	// CanonicalArtistName with the group representative (possibly de-accented)
	// and must receive the authoritative accented spelling back.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artist/" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		resp := artistSearchResp{Count: 1, Artists: []artistResp{
			{ID: "mbid-1", Name: "René Froger", Score: 100},
		}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("mock encode error: %v", err)
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := &Client{
		cfg: MusicBrainzConfig{},
		api: &APIClient{
			cfg:         MusicBrainzConfig{},
			httpClient:  srv.Client(),
			userAgent:   "groovearr-test",
			baseURL:     srv.URL,
			log:         testLogger(),
			minInterval: 0,
		},
		log: testLogger(),
	}

	got, err := client.CanonicalArtistName(context.Background(), "Rene Froger")
	if err != nil {
		t.Fatalf("CanonicalArtistName error: %v", err)
	}
	if got != "René Froger" {
		t.Errorf("CanonicalArtistName(\"Rene Froger\") = %q, want \"René Froger\" (accented canonical)", got)
	}
}

func TestSearchArtist_NoMatch(t *testing.T) {
	client, cleanup := newArtistTestClient(t, []artistResp{
		{ID: "mbid-x", Name: "Some Other Artist", Score: 100},
	})
	defer cleanup()

	res, err := client.SearchArtist(context.Background(), "Totally Different Name")
	if err != nil {
		t.Fatalf("SearchArtist error: %v", err)
	}
	if res != nil {
		t.Errorf("expected nil for non-matching result, got %+v", res)
	}
}

func TestSearchArtist_EmptyName(t *testing.T) {
	client, cleanup := newArtistTestClient(t, nil)
	defer cleanup()
	res, err := client.SearchArtist(context.Background(), "")
	if err != nil {
		t.Fatalf("SearchArtist error: %v", err)
	}
	if res != nil {
		t.Errorf("expected nil for empty name, got %+v", res)
	}
}

// TestNewAPIClientDefaultIntervalHasMargin locks the MB pacing margin: the
// client must pace strictly slower than MusicBrainz's hard 1 request/second
// per-IP limit (an all-or-nothing 503 above it, regardless of User-Agent), so
// the default interval stays at or above 1100ms. A regression back to exactly
// 1s would ride the boundary and can trip the limit under clock or scheduling
// skew. Tests that need real pacing set minInterval explicitly via
// SetMinInterval; the shared provider cooldown (AGENTS §8) is unchanged.
//
// It builds the client through NewClient (the provider constructor) rather
// than naming the API client constructor directly: the module-wide guard
// TestExactlyOneMusicBrainzAPIClientConstruction counts every textual
// construction reference, so a test must not add a second one.
func TestNewAPIClientDefaultIntervalHasMargin(t *testing.T) {
	c := NewClient(MusicBrainzConfig{}, testLogger())
	if c.api.minInterval <= time.Second {
		t.Fatalf("default minInterval = %v, want > 1s (must not ride MB's 1 req/sec per-IP boundary)", c.api.minInterval)
	}
	if c.api.minInterval < 1100*time.Millisecond {
		t.Fatalf("default minInterval = %v, want >= 1100ms", c.api.minInterval)
	}
}

func TestAPIClientRateLimitsConcurrentRequests(t *testing.T) {
	// Record when requests arrive; concurrent callers must be paced one
	// minInterval apart (token bucket), not burst through together.
	var mu sync.Mutex
	var arrivals []time.Time
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"artists": []any{}})
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 50 * time.Millisecond,
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.SearchArtist(context.Background(), "Test Artist")
		}()
	}
	wg.Wait()

	mu.Lock()
	times := append([]time.Time(nil), arrivals...)
	mu.Unlock()
	if len(times) != 5 {
		t.Fatalf("expected 5 arrivals, got %d", len(times))
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	// Allow 10% jitter — under -race the scheduler skews timing slightly.
	// A burst (the regression this guards against) is microseconds, far below.
	minGap := time.Duration(float64(client.minInterval) * 0.9)
	for i := 1; i < len(times); i++ {
		if d := times[i].Sub(times[i-1]); d < minGap {
			t.Errorf("requests %d and %d arrived %v apart, want >= %v", i-1, i, d, minGap)
		}
	}
}

func itoa(n int) string {
	return string(rune('0'+n%10)) + string(rune('0'+n/10%10))
}

// TestLogOpErrorLevel asserts a rate-limit error is downgraded to WARN while a
// genuine fault stays ERROR — the level split that keeps the error log free of
// routine, self-healing 503 noise.
func TestLogOpErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := &APIClient{log: logger}

	c.logOpError("rate limited op", "error", metadata.NewRateLimitError("musicbrainz", 0, "HTTP 503"))
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Errorf("rate-limit log = %s, want WARN", buf.String())
	}

	buf.Reset()
	c.logOpError("real fault", "error", errors.New("boom"))
	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Errorf("fault log = %s, want ERROR", buf.String())
	}
}

// ─── Release-group search ──────────────────────────────────────────────

type releaseGroupSearchResp struct {
	Count         int               `json:"count"`
	ReleaseGroups []releaseGroupHit `json:"release-groups"`
}

type releaseGroupHit struct {
	ID               string              `json:"id"`
	Title            string              `json:"title"`
	FirstReleaseDate string              `json:"first-release-date"`
	PrimaryType      string              `json:"primary-type"`
	ArtistCredit     []artistCreditEntry `json:"artist-credit"`
}

func newReleaseGroupTestClient(t *testing.T, rgs []releaseGroupHit) (*APIClient, func()) {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group/" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		resp := releaseGroupSearchResp{Count: len(rgs), ReleaseGroups: rgs}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("mock encode error: %v", err)
		}
	})
	srv := httptest.NewServer(handler)
	client := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0,
	}
	return client, srv.Close
}

func TestSearchReleaseGroups_ParsesAllInOrder(t *testing.T) {
	// A karaoke hit is ranked first; the real release group sits at rank 2. Both
	// must come back, in order, each with its own artist credit.
	client, cleanup := newReleaseGroupTestClient(t, []releaseGroupHit{
		{
			ID:               "rg-karaoke",
			Title:            "Unconditionally (Karaoke Version)",
			FirstReleaseDate: "2013",
			PrimaryType:      "Single",
			ArtistCredit:     []artistCreditEntry{{Name: "Karaoke Star"}},
		},
		{
			ID:               "rg-kp",
			Title:            "Unconditionally",
			FirstReleaseDate: "2013-10-22",
			PrimaryType:      "Single",
			ArtistCredit:     []artistCreditEntry{{Name: "Katy Perry"}},
		},
	})
	defer cleanup()

	rgs, err := client.SearchReleaseGroups(context.Background(), "Katy Perry", "Unconditionally", 5)
	if err != nil {
		t.Fatalf("SearchReleaseGroups: %v", err)
	}
	if len(rgs) != 2 {
		t.Fatalf("results = %d, want 2", len(rgs))
	}
	if rgs[0].MBID != "rg-karaoke" || rgs[1].MBID != "rg-kp" {
		t.Errorf("order = [%s %s], want [rg-karaoke rg-kp] (ranked order preserved)", rgs[0].MBID, rgs[1].MBID)
	}
	if rgs[0].ArtistName != "Karaoke Star" {
		t.Errorf("rgs[0].ArtistName = %q, want Karaoke Star (own credit, not the query)", rgs[0].ArtistName)
	}
	if rgs[1].Title != "Unconditionally" {
		t.Errorf("rgs[1].Title = %q, want Unconditionally", rgs[1].Title)
	}
	if rgs[1].ArtistName != "Katy Perry" {
		t.Errorf("rgs[1].ArtistName = %q, want Katy Perry (parsed from artist-credit)", rgs[1].ArtistName)
	}
	if rgs[1].FirstReleaseDate != "2013-10-22" {
		t.Errorf("rgs[1].FirstReleaseDate = %q, want 2013-10-22", rgs[1].FirstReleaseDate)
	}
	if rgs[1].PrimaryType != "Single" {
		t.Errorf("rgs[1].PrimaryType = %q, want Single", rgs[1].PrimaryType)
	}
}

func TestSearchReleaseGroup_ReturnsFirst(t *testing.T) {
	// The legacy single-result helper must keep returning the top hit for its
	// existing callers (cover art, track resolution, connection check).
	client, cleanup := newReleaseGroupTestClient(t, []releaseGroupHit{
		{ID: "rg-1", Title: "First", PrimaryType: "Album", ArtistCredit: []artistCreditEntry{{Name: "Artist One"}}},
		{ID: "rg-2", Title: "Second", PrimaryType: "Album"},
	})
	defer cleanup()

	rg, err := client.SearchReleaseGroup(context.Background(), "Artist One", "First")
	if err != nil {
		t.Fatalf("SearchReleaseGroup: %v", err)
	}
	if rg == nil {
		t.Fatal("expected a result, got nil")
	}
	if rg.MBID != "rg-1" || rg.Title != "First" {
		t.Errorf("got %+v, want rg-1/First (first ranked result)", rg)
	}
	if rg.ArtistName != "Artist One" {
		t.Errorf("ArtistName = %q, want Artist One", rg.ArtistName)
	}
}

func TestSearchReleaseGroup_EmptyReturnsNil(t *testing.T) {
	client, cleanup := newReleaseGroupTestClient(t, nil)
	defer cleanup()

	rg, err := client.SearchReleaseGroup(context.Background(), "Nobody", "Nothing")
	if err != nil {
		t.Fatalf("SearchReleaseGroup: %v", err)
	}
	if rg != nil {
		t.Errorf("got %+v, want nil", rg)
	}
}

func TestSearchReleaseGroups_EmptyReturnsNil(t *testing.T) {
	client, cleanup := newReleaseGroupTestClient(t, nil)
	defer cleanup()

	rgs, err := client.SearchReleaseGroups(context.Background(), "Nobody", "Nothing", 5)
	if err != nil {
		t.Fatalf("SearchReleaseGroups: %v", err)
	}
	if rgs != nil {
		t.Errorf("results = %+v, want nil", rgs)
	}
}

func TestSearchReleaseGroups_EmptyInputSkipsServer(t *testing.T) {
	client, cleanup := newReleaseGroupTestClient(t, nil)
	defer cleanup()

	if rgs, err := client.SearchReleaseGroups(context.Background(), "", "Nothing", 5); err != nil || rgs != nil {
		t.Errorf("empty artist: got (%+v, %v), want (nil, nil)", rgs, err)
	}
	if rgs, err := client.SearchReleaseGroups(context.Background(), "Nobody", "", 5); err != nil || rgs != nil {
		t.Errorf("empty album: got (%+v, %v), want (nil, nil)", rgs, err)
	}
}

func TestSearchReleaseGroups_ClampsAndDefaultsLimit(t *testing.T) {
	var sawLimit string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releaseGroupSearchResp{})
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0,
	}

	cases := []struct {
		name  string
		limit int
		want  string
	}{
		{"default when zero", 0, "5"},
		{"default when negative", -3, "5"},
		{"passthrough", 20, "20"},
		{"clamped to MB max", 5000, "100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.SearchReleaseGroups(context.Background(), "a", "b", tc.limit); err != nil {
				t.Fatalf("SearchReleaseGroups: %v", err)
			}
			if sawLimit != tc.want {
				t.Errorf("limit param = %q, want %q", sawLimit, tc.want)
			}
		})
	}
}
