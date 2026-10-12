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
	stats   catalog.QueueStats
	counts  map[catalog.RefreshStatus]int
	artists int

	errQueue  error
	errCounts error
	errArtist error
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
