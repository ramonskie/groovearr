package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
)

// TestStore_QueueStats covers the empty queue and the due/scheduled split.
func TestStore_QueueStats(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

	// Empty queue: all zero, and Scheduled must not go negative.
	st, err := s.QueueStats(ctx, now)
	if err != nil {
		t.Fatalf("queue stats (empty): %v", err)
	}
	if st.Queued != 0 || st.DueNow != 0 || st.Scheduled != 0 {
		t.Fatalf("empty queue stats = %+v, want all zero", st)
	}

	// One due now, two scheduled in the future.
	for _, item := range []catalog.RefreshItem{
		{EntityType: catalog.EntityAlbum, EntityID: 1, DueAt: now.Add(-time.Minute)},
		{EntityType: catalog.EntityAlbum, EntityID: 2, DueAt: now.Add(time.Hour)},
		{EntityType: catalog.EntityArtist, EntityID: 3, DueAt: now.Add(2 * time.Hour)},
	} {
		if _, err := s.EnqueueRefresh(ctx, item); err != nil {
			t.Fatalf("enqueue %d: %v", item.EntityID, err)
		}
	}

	st, err = s.QueueStats(ctx, now)
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if st.Queued != 3 {
		t.Errorf("Queued = %d, want 3", st.Queued)
	}
	if st.DueNow != 1 {
		t.Errorf("DueNow = %d, want 1", st.DueNow)
	}
	if st.Scheduled != 2 {
		t.Errorf("Scheduled = %d, want 2", st.Scheduled)
	}
}

// TestStore_AlbumStatusCounts verifies the per-status rollup and that a status
// with no rows is absent from the map.
func TestStore_AlbumStatusCounts(t *testing.T) {
	ctx := context.Background()
	s, db := newTestStore(t)

	artistID := insertArtist(t, db, "Aphex Twin")
	albums := []struct {
		title  string
		status catalog.RefreshStatus
	}{
		{"SAW 85-92", catalog.StatusFresh},
		{"SAW 85-92", catalog.StatusFresh},
		{"Drukqs", catalog.StatusStale},
		{"Richard D. James Album", catalog.StatusUnmatched},
	}
	for _, a := range albums {
		albumID := insertAlbum(t, db, artistID, a.title)
		if err := s.UpsertAlbumMetadata(ctx, catalog.AlbumMetadata{
			AlbumID:       albumID,
			RefreshStatus: a.status,
		}); err != nil {
			t.Fatalf("upsert album metadata: %v", err)
		}
	}

	counts, err := s.AlbumStatusCounts(ctx)
	if err != nil {
		t.Fatalf("album status counts: %v", err)
	}
	if counts[catalog.StatusFresh] != 2 {
		t.Errorf("fresh = %d, want 2", counts[catalog.StatusFresh])
	}
	if counts[catalog.StatusStale] != 1 {
		t.Errorf("stale = %d, want 1", counts[catalog.StatusStale])
	}
	if counts[catalog.StatusUnmatched] != 1 {
		t.Errorf("unmatched = %d, want 1", counts[catalog.StatusUnmatched])
	}
	if _, ok := counts[catalog.StatusFailed]; ok {
		t.Errorf("failed present with %d rows, want absent", counts[catalog.StatusFailed])
	}
}

// TestStore_CountArtistMetadata verifies the materialized-artist count.
func TestStore_CountArtistMetadata(t *testing.T) {
	ctx := context.Background()
	s, db := newTestStore(t)

	if n, err := s.CountArtistMetadata(ctx); err != nil || n != 0 {
		t.Fatalf("empty count = (%d, %v), want (0, nil)", n, err)
	}

	artistID := insertArtist(t, db, "Boards of Canada")
	if err := s.UpsertArtistMetadata(ctx, catalog.ArtistMetadata{
		ArtistID:      artistID,
		RefreshStatus: catalog.StatusFresh,
	}); err != nil {
		t.Fatalf("upsert artist metadata: %v", err)
	}

	n, err := s.CountArtistMetadata(ctx)
	if err != nil {
		t.Fatalf("count artist metadata: %v", err)
	}
	if n != 1 {
		t.Errorf("materialized artists = %d, want 1", n)
	}
}
