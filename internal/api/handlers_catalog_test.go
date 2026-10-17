package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/refresh"
)

// fakeCatalogStats is a minimal catalog.StatsStore that returns canned values,
// or a per-method canned error.
type fakeCatalogStats struct {
	stats     catalog.QueueStats
	counts    map[catalog.RefreshStatus]int
	artists   int
	unmatched []catalog.UnmatchedAlbum
	limit     int

	errQueue     error
	errCounts    error
	errArtist    error
	errUnmatched error
}

func (f *fakeCatalogStats) QueueStats(context.Context, time.Time) (catalog.QueueStats, error) {
	if f.errQueue != nil {
		return catalog.QueueStats{}, f.errQueue
	}
	return f.stats, nil
}

func (f *fakeCatalogStats) AlbumStatusCounts(context.Context) (map[catalog.RefreshStatus]int, error) {
	if f.errCounts != nil {
		return nil, f.errCounts
	}
	return f.counts, nil
}

func (f *fakeCatalogStats) CountArtistMetadata(context.Context) (int, error) {
	if f.errArtist != nil {
		return 0, f.errArtist
	}
	return f.artists, nil
}

func (f *fakeCatalogStats) ListUnmatchedAlbums(_ context.Context, limit int) ([]catalog.UnmatchedAlbum, error) {
	f.limit = limit
	if f.errUnmatched != nil {
		return nil, f.errUnmatched
	}
	return f.unmatched, nil
}

// fakeRefreshStatus is a fixed refreshStatusReader.
type fakeRefreshStatus struct{ st refresh.Status }

func (f fakeRefreshStatus) Status() refresh.Status { return f.st }

func TestHandleCatalogStatus_OK(t *testing.T) {
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s := &Server{
		catalogStats: &fakeCatalogStats{
			stats:   catalog.QueueStats{Queued: 5, DueNow: 2, Scheduled: 3},
			counts:  map[catalog.RefreshStatus]int{catalog.StatusFresh: 4, catalog.StatusStale: 1},
			artists: 7,
		},
		refreshStatus: fakeRefreshStatus{st: refresh.Status{
			Running:         true,
			IntervalSeconds: 60,
			BatchSize:       8,
			LastDrainAt:     &at,
			LastDrainCount:  2,
		}},
		catalogEnabled: func() bool { return true },
	}

	rec := httptest.NewRecorder()
	s.handleCatalogStatus(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got catalogStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got.Queue.Queued != 5 || got.Queue.DueNow != 2 || got.Queue.Scheduled != 3 {
		t.Errorf("queue = %+v, want {5 2 3}", got.Queue)
	}
	// Every known status must be present (zero-filled) so the card is stable.
	for status, want := range map[string]int{
		string(catalog.StatusPending):   0,
		string(catalog.StatusFresh):     4,
		string(catalog.StatusStale):     1,
		string(catalog.StatusUnmatched): 0,
		string(catalog.StatusFailed):    0,
	} {
		if got.Albums[status] != want {
			t.Errorf("albums[%q] = %d, want %d", status, got.Albums[status], want)
		}
	}
	if got.Artists.Materialized != 7 {
		t.Errorf("artists.materialized = %d, want 7", got.Artists.Materialized)
	}
	if !got.Engine.Running || got.Engine.IntervalSeconds != 60 || got.Engine.BatchSize != 8 {
		t.Errorf("engine = %+v, want running/60/8", got.Engine)
	}
	if got.Engine.LastDrainAt == nil || !got.Engine.LastDrainAt.Equal(at) {
		t.Errorf("engine.last_drain_at = %v, want %v", got.Engine.LastDrainAt, at)
	}
	if got.Engine.LastDrainCount != 2 {
		t.Errorf("engine.last_drain_count = %d, want 2", got.Engine.LastDrainCount)
	}
}

// TestHandleCatalogStatus_NoStatsWired verifies the defensive 503 when the
// catalog store was never injected.
func TestHandleCatalogStatus_NoStatsWired(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.handleCatalogStatus(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/status", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestHandleCatalogStatus_StoreError covers a failure from each store read: all
// three must become a 500, and the body must not leak the wrapped error text.
func TestHandleCatalogStatus_StoreError(t *testing.T) {
	cases := []struct {
		name  string
		stats *fakeCatalogStats
	}{
		{"queue", &fakeCatalogStats{errQueue: errors.New("catalog sqlite: boom")}},
		{"albums", &fakeCatalogStats{errCounts: errors.New("catalog sqlite: boom")}},
		{"artists", &fakeCatalogStats{errArtist: errors.New("catalog sqlite: boom")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{catalogStats: tc.stats}

			rec := httptest.NewRecorder()
			s.handleCatalogStatus(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/status", nil))

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			if body := rec.Body.String(); strings.Contains(body, "boom") {
				t.Errorf("body leaked wrapped error text: %s", body)
			}
		})
	}
}

// TestHandleCatalogStatus_Disabled verifies the live catalog.enabled gate is
// reflected in the response.
func TestHandleCatalogStatus_Disabled(t *testing.T) {
	s := &Server{
		catalogStats:   &fakeCatalogStats{counts: map[catalog.RefreshStatus]int{}},
		catalogEnabled: func() bool { return false },
	}

	rec := httptest.NewRecorder()
	s.handleCatalogStatus(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got catalogStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Enabled {
		t.Error("enabled = true, want false from the live gate")
	}
}

// TestHandleCatalogStatus_NoEngineWired verifies the engine block degrades to
// its zero value when no engine is injected, without failing the request.
func TestHandleCatalogStatus_NoEngineWired(t *testing.T) {
	s := &Server{catalogStats: &fakeCatalogStats{counts: map[catalog.RefreshStatus]int{}}}

	rec := httptest.NewRecorder()
	s.handleCatalogStatus(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got catalogStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Engine.Running || got.Engine.IntervalSeconds != 0 {
		t.Errorf("engine = %+v, want zero value", got.Engine)
	}
	if len(got.Albums) != 5 {
		t.Errorf("albums has %d keys, want 5 zero-filled", len(got.Albums))
	}
}

func TestHandleCatalogUnmatched_OK(t *testing.T) {
	at := time.Date(2026, 10, 14, 17, 55, 12, 0, time.UTC)
	f := &fakeCatalogStats{unmatched: []catalog.UnmatchedAlbum{
		{AlbumID: 4332, ArtistID: 682, Artist: "R.E.M.", Title: "1992 Fanclub Single",
			Status: catalog.StatusUnmatched, Error: "no provider match", UpdatedAt: at},
		{AlbumID: 5024, ArtistID: 824, Artist: "The Wesley Willis Fiasco",
			Title: "All You Need / Get on the Bus", Status: catalog.StatusUnmatched, UpdatedAt: at},
	}}
	s := &Server{catalogStats: f}

	rec := httptest.NewRecorder()
	s.handleCatalogUnmatched(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/unmatched", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if f.limit != catalogUnmatchedLimit+1 {
		t.Errorf("store limit = %d, want %d (one extra for truncation)", f.limit, catalogUnmatchedLimit+1)
	}
	var got []catalogUnmatchedAlbum
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[0].AlbumID != 4332 || got[0].ArtistID != 682 || got[0].Artist != "R.E.M." ||
		got[0].Title != "1992 Fanclub Single" || got[0].Status != catalog.StatusUnmatched ||
		got[0].Error != "no provider match" {
		t.Errorf("first row = %+v", got[0])
	}
	if !got[0].UpdatedAt.Equal(at) {
		t.Errorf("updated_at = %v, want %v", got[0].UpdatedAt, at)
	}
	if got[1].Error != "" {
		t.Errorf("empty error = %q, want empty", got[1].Error)
	}

	// Assert the wire contract directly: snake_case keys and an omitted empty
	// error, not just the Go struct round-trip.
	var wire []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode wire response: %v", err)
	}
	for _, key := range []string{"album_id", "artist_id", "artist", "title", "status", "updated_at"} {
		if _, ok := wire[0][key]; !ok {
			t.Errorf("first row missing %q", key)
		}
	}
	if _, ok := wire[1]["error"]; ok {
		t.Errorf("empty error should be omitted from the wire body: %v", wire[1])
	}
}

func TestHandleCatalogUnmatched_Truncated(t *testing.T) {
	rows := make([]catalog.UnmatchedAlbum, catalogUnmatchedLimit+1)
	for i := range rows {
		rows[i] = catalog.UnmatchedAlbum{AlbumID: int64(i + 1), Status: catalog.StatusUnmatched}
	}
	s := &Server{catalogStats: &fakeCatalogStats{unmatched: rows}}

	rec := httptest.NewRecorder()
	s.handleCatalogUnmatched(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/unmatched", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Result-Truncated"); got != "true" {
		t.Errorf("X-Result-Truncated = %q, want true", got)
	}
	var body []catalogUnmatchedAlbum
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != catalogUnmatchedLimit {
		t.Fatalf("rows = %d, want capped %d", len(body), catalogUnmatchedLimit)
	}
	if body[0].AlbumID != 1 || body[catalogUnmatchedLimit-1].AlbumID != catalogUnmatchedLimit {
		t.Errorf("truncated slice = [%d..%d], want [1..%d]",
			body[0].AlbumID, body[catalogUnmatchedLimit-1].AlbumID, catalogUnmatchedLimit)
	}
}

func TestHandleCatalogUnmatched_EmptyIsArray(t *testing.T) {
	s := &Server{catalogStats: &fakeCatalogStats{}}

	rec := httptest.NewRecorder()
	s.handleCatalogUnmatched(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/unmatched", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %s, want []", body)
	}
}

func TestHandleCatalogUnmatched_NoStatsWired(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.handleCatalogUnmatched(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/unmatched", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"catalog stats not available"}` {
		t.Errorf("body = %s, want catalog stats not available", body)
	}
}

func TestHandleCatalogUnmatched_StoreError(t *testing.T) {
	s := &Server{catalogStats: &fakeCatalogStats{errUnmatched: errors.New("catalog sqlite: boom")}}

	rec := httptest.NewRecorder()
	s.handleCatalogUnmatched(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/unmatched", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "boom") {
		t.Errorf("body leaked wrapped error text: %s", body)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"catalog unmatched unavailable"}` {
		t.Errorf("body = %s, want catalog unmatched unavailable", body)
	}
}
