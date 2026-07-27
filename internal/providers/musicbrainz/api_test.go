package musicbrainz

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// newTestClient creates an apiClient pointed at a mock server serving the given
// recordings response. The handler is called once per request — the server
// auto-closes after the test.
func newTestClient(t *testing.T, recordings []recordingResp) (*apiClient, func()) {
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
	client := &apiClient{
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
func newTestClientWithStatus(t *testing.T, status int) (*apiClient, func()) {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})

	srv := httptest.NewServer(handler)
	client := &apiClient{
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

func TestSearchRecording_HTTP503(t *testing.T) {
	client, cleanup := newTestClientWithStatus(t, http.StatusServiceUnavailable)
	defer cleanup()

	_, err := client.SearchRecording(context.Background(), "Artist", "Title")
	if err == nil {
		t.Fatal("expected error for 503, got nil")
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

	client := &apiClient{
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

	client := &apiClient{
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

func itoa(n int) string {
	return string(rune('0'+n%10)) + string(rune('0'+n/10%10))
}
