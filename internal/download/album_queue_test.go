package download

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

// fakeAlbumSearcher records queries and returns canned releases/err.
type fakeAlbumSearcher struct {
	releases []domain.AlbumRelease
	err      error
	calls    int
}

func (f *fakeAlbumSearcher) SearchAlbums(ctx context.Context, query string) ([]domain.AlbumRelease, error) {
	f.calls++
	return f.releases, f.err
}

// errInsertStore fails Insert for records whose Title matches failTitle.
type errInsertStore struct {
	*mockStore
	failTitle string
}

func (e *errInsertStore) Insert(ctx context.Context, r *Record) error {
	if e.failTitle != "" && r.Title == e.failTitle {
		return fmt.Errorf("insert boom")
	}
	return e.mockStore.Insert(ctx, r)
}

func sampleTracks() []TrackQueue {
	return []TrackQueue{
		{Artist: "A", Album: "B", Title: "T1", TrackNumber: 1, DiscNumber: 1, ISRC: "ISRC1"},
		{Artist: "A", Album: "B", Title: "T2", TrackNumber: 2, DiscNumber: 1, ISRC: "ISRC2"},
		{Artist: "A", Album: "B", Title: "T3", TrackNumber: 3, DiscNumber: 1, ISRC: "ISRC3"},
	}
}

func oneAlbumRelease() []domain.AlbumRelease {
	return []domain.AlbumRelease{{SourceName: "prowlarr", Artist: "A", Album: "B", MagnetURI: "magnet:x"}}
}

func TestQueueAlbumWithFallback(t *testing.T) {
	searcher := func(releases []domain.AlbumRelease, err error) *fakeAlbumSearcher {
		return &fakeAlbumSearcher{releases: releases, err: err}
	}

	tests := []struct {
		name            string
		client          string
		sources         []string
		tracks          []TrackQueue
		searcher        *fakeAlbumSearcher
		wantMode        QueueMode
		wantQueued      int
		wantTotal       int
		wantErrors      int
		wantDownloadID  bool
		wantSearchCalls int
	}{
		{
			name:            "album mode when sources and client set",
			client:          "qbit",
			sources:         []string{"prowlarr"},
			tracks:          sampleTracks(),
			searcher:        searcher(oneAlbumRelease(), nil),
			wantMode:        QueueModeAlbum,
			wantQueued:      1,
			wantTotal:       0,
			wantDownloadID:  true,
			wantSearchCalls: 1,
		},
		{
			name:            "per-track when client empty",
			client:          "",
			sources:         []string{"prowlarr"},
			tracks:          sampleTracks(),
			searcher:        searcher(oneAlbumRelease(), nil),
			wantMode:        QueueModeTracks,
			wantQueued:      3,
			wantTotal:       3,
			wantSearchCalls: 0,
		},
		{
			name:            "per-track when sources empty",
			client:          "qbit",
			sources:         nil,
			tracks:          sampleTracks(),
			searcher:        searcher(oneAlbumRelease(), nil),
			wantMode:        QueueModeTracks,
			wantQueued:      3,
			wantTotal:       3,
			wantSearchCalls: 0,
		},
		{
			name:            "per-track when search returns no releases",
			client:          "qbit",
			sources:         []string{"prowlarr"},
			tracks:          sampleTracks(),
			searcher:        searcher(nil, nil),
			wantMode:        QueueModeTracks,
			wantQueued:      3,
			wantTotal:       3,
			wantSearchCalls: 1,
		},
		{
			name:            "per-track when search errors without returning it",
			client:          "qbit",
			sources:         []string{"prowlarr"},
			tracks:          sampleTracks(),
			searcher:        searcher(nil, fmt.Errorf("search boom")),
			wantMode:        QueueModeTracks,
			wantQueued:      3,
			wantTotal:       3,
			wantSearchCalls: 1,
		},
		{
			name:            "per-track when searcher not wired",
			client:          "qbit",
			sources:         []string{"prowlarr"},
			tracks:          sampleTracks(),
			searcher:        nil,
			wantMode:        QueueModeTracks,
			wantQueued:      3,
			wantTotal:       3,
			wantSearchCalls: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(newMockStore(), newMockBus(), testLogger())
			if tc.searcher != nil {
				svc.SetAlbumSearcher(tc.searcher)
			}

			res, err := svc.QueueAlbumWithFallback(context.Background(), 0, "", "A", "B", tc.tracks, tc.client, tc.sources)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", res.Mode, tc.wantMode)
			}
			if res.Queued != tc.wantQueued {
				t.Errorf("Queued = %d, want %d", res.Queued, tc.wantQueued)
			}
			if res.Total != tc.wantTotal {
				t.Errorf("Total = %d, want %d", res.Total, tc.wantTotal)
			}
			if len(res.Errors) != tc.wantErrors {
				t.Errorf("Errors = %v, want %d entries", res.Errors, tc.wantErrors)
			}
			if (res.DownloadID != "") != tc.wantDownloadID {
				t.Errorf("DownloadID = %q, want set=%v", res.DownloadID, tc.wantDownloadID)
			}
			if tc.searcher != nil && tc.searcher.calls != tc.wantSearchCalls {
				t.Errorf("SearchAlbums calls = %d, want %d", tc.searcher.calls, tc.wantSearchCalls)
			}
		})
	}
}

// TestQueueAlbumWithFallbackPerTrackPersists verifies the per-track path
// actually writes records carrying the track metadata.
func TestQueueAlbumWithFallbackPerTrackPersists(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, newMockBus(), testLogger())

	res, err := svc.QueueAlbumWithFallback(context.Background(), 0, "", "A", "B", sampleTracks(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Queued != 3 {
		t.Fatalf("Queued = %d, want 3", res.Queued)
	}

	records, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("stored %d records, want 3", len(records))
	}
	for _, r := range records {
		if !r.IsPendingSource() {
			t.Errorf("record %q: source = %q, want pending", r.ID, r.SourceName)
		}
	}
}

// TestQueueAlbumWithFallbackPerTrackWhenMetadataEmpty guards the
// non-empty artist/album eligibility rule: the album leg is skipped and the
// searcher is never consulted.
func TestQueueAlbumWithFallbackPerTrackWhenMetadataEmpty(t *testing.T) {
	searcher := &fakeAlbumSearcher{releases: oneAlbumRelease()}
	svc := NewService(newMockStore(), newMockBus(), testLogger())
	svc.SetAlbumSearcher(searcher)

	res, err := svc.QueueAlbumWithFallback(context.Background(), 0, "", "", "", sampleTracks(), "qbit", []string{"prowlarr"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != QueueModeTracks {
		t.Errorf("Mode = %q, want %q", res.Mode, QueueModeTracks)
	}
	if res.Total != 3 || res.Queued != 3 {
		t.Errorf("Queued/Total = %d/%d, want 3/3", res.Queued, res.Total)
	}
	if searcher.calls != 0 {
		t.Errorf("searcher called %d times, want 0", searcher.calls)
	}
}

// TestQueueAlbumWithFallbackQueueAlbumErrorPropagates verifies a real
// QueueAlbum failure is returned wrapped with Mode still QueueModeAlbum.
func TestQueueAlbumWithFallbackQueueAlbumErrorPropagates(t *testing.T) {
	store := &errInsertStore{mockStore: newMockStore(), failTitle: "B"}
	svc := NewService(store, newMockBus(), testLogger())
	svc.SetAlbumSearcher(&fakeAlbumSearcher{releases: oneAlbumRelease()})

	res, err := svc.QueueAlbumWithFallback(context.Background(), 0, "", "A", "B", sampleTracks(), "qbit", []string{"prowlarr"})
	if err == nil {
		t.Fatal("expected error from failed QueueAlbum")
	}
	if !strings.Contains(err.Error(), "queue album") {
		t.Errorf("error = %q, want wrapped 'queue album'", err)
	}
	if res.Mode != QueueModeAlbum {
		t.Errorf("Mode = %q, want %q", res.Mode, QueueModeAlbum)
	}
	if res.Queued != 0 {
		t.Errorf("Queued = %d, want 0", res.Queued)
	}
	if res.DownloadID != "" {
		t.Errorf("DownloadID = %q, want empty", res.DownloadID)
	}
}

// TestQueueAlbumWithFallbackPerTrackSkipsAndCollectsErrors verifies empty
// artist/title tracks are skipped and per-track insert failures are collected
// without aborting the loop.
func TestQueueAlbumWithFallbackPerTrackSkipsAndCollectsErrors(t *testing.T) {
	store := &errInsertStore{mockStore: newMockStore(), failTitle: "Bad"}
	svc := NewService(store, newMockBus(), testLogger())

	tracks := []TrackQueue{
		{Artist: "A", Title: "Good"},
		{Artist: "A", Title: "Bad"},     // insert fails
		{Artist: "", Title: "NoArtist"}, // skipped
		{Artist: "A", Title: ""},        // skipped
	}

	res, err := svc.QueueAlbumWithFallback(context.Background(), 0, "", "A", "B", tracks, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Mode != QueueModeTracks {
		t.Errorf("Mode = %q, want %q", res.Mode, QueueModeTracks)
	}
	if res.Total != 4 {
		t.Errorf("Total = %d, want 4", res.Total)
	}
	if res.Queued != 1 {
		t.Errorf("Queued = %d, want 1", res.Queued)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Errors = %v, want 1 entry", res.Errors)
	}
	if !strings.Contains(res.Errors[0], "Bad") {
		t.Errorf("error entry = %q, want it to reference the failing track", res.Errors[0])
	}
}
