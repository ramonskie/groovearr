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

// TestStore_ListUnmatchedAlbums verifies the unmatched listing joins the
// library artist/title, filters out non-unmatched statuses, orders by title,
// and honors the limit contract.
func TestStore_ListUnmatchedAlbums(t *testing.T) {
	ctx := context.Background()
	s, db := newTestStore(t)
	at := time.Date(2026, 10, 14, 17, 55, 12, 0, time.UTC)

	artistID := insertArtist(t, db, "R.E.M.")
	// Seed deliberately out of title order and with same-title rows, so the
	// test fails if the title ORDER BY is removed. The album_id tiebreak is
	// defensive (it protects against a plan change); it is not claimed to be
	// exercised by this seed.
	unmatchedZ := insertAlbum(t, db, artistID, "Zzz Later")
	unmatchedA := insertAlbum(t, db, artistID, "aaa First")
	same1 := insertAlbum(t, db, artistID, "Same Title")
	same2 := insertAlbum(t, db, artistID, "Same Title")
	fresh := insertAlbum(t, db, artistID, "Green")

	for _, row := range []struct {
		albumID int64
		status  catalog.RefreshStatus
		err     string
	}{
		{unmatchedZ, catalog.StatusUnmatched, "no provider match"},
		{unmatchedA, catalog.StatusUnmatched, "no provider match"},
		{same1, catalog.StatusUnmatched, "no provider match"},
		{same2, catalog.StatusUnmatched, "no provider match"},
		{fresh, catalog.StatusFresh, ""},
	} {
		if err := s.UpsertAlbumMetadata(ctx, catalog.AlbumMetadata{
			AlbumID:       row.albumID,
			RefreshStatus: row.status,
			RefreshError:  row.err,
			UpdatedAt:     at,
		}); err != nil {
			t.Fatalf("upsert album metadata %d: %v", row.albumID, err)
		}
	}

	got, err := s.ListUnmatchedAlbums(ctx, 10)
	if err != nil {
		t.Fatalf("list unmatched: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("rows = %d, want 4", len(got))
	}
	wantOrder := []int64{unmatchedA, same1, same2, unmatchedZ}
	for i, want := range wantOrder {
		if got[i].AlbumID != want {
			t.Errorf("row %d album_id = %d, want %d (title COLLATE NOCASE, then id)", i, got[i].AlbumID, want)
		}
	}
	if got[0].ArtistID != artistID || got[0].Artist != "R.E.M." {
		t.Errorf("artist = (%d,%q), want (%d,%q)", got[0].ArtistID, got[0].Artist, artistID, "R.E.M.")
	}
	if got[0].Title != "aaa First" || got[0].Status != catalog.StatusUnmatched ||
		got[0].Error != "no provider match" || !got[0].UpdatedAt.Equal(at) {
		t.Errorf("first row = %+v", got[0])
	}

	if limited, err := s.ListUnmatchedAlbums(ctx, 1); err != nil || len(limited) != 1 || limited[0].AlbumID != unmatchedA {
		t.Errorf("limit 1 = (%+v, %v), want only %d", limited, err, unmatchedA)
	}
	if empty, err := s.ListUnmatchedAlbums(ctx, 0); err != nil || len(empty) != 0 {
		t.Errorf("limit 0 = (%d, %v), want (0, nil)", len(empty), err)
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

// TestStore_GetUnmatchedAlbum verifies the single-row read used by manual
// review: it joins artist/title, returns nil for a resolved album, and returns
// nil for an unknown id (the fail-closed path behind a stale review request).
func TestStore_GetUnmatchedAlbum(t *testing.T) {
	ctx := context.Background()
	s, db := newTestStore(t)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	artistID := insertArtist(t, db, "Mad Mark")
	unmatched := insertAlbum(t, db, artistID, "WOW")
	fresh := insertAlbum(t, db, artistID, "Green")

	for _, row := range []struct {
		albumID int64
		status  catalog.RefreshStatus
		err     string
	}{
		{unmatched, catalog.StatusUnmatched, "no provider match"},
		{fresh, catalog.StatusFresh, ""},
	} {
		if err := s.UpsertAlbumMetadata(ctx, catalog.AlbumMetadata{
			AlbumID:       row.albumID,
			RefreshStatus: row.status,
			RefreshError:  row.err,
			UpdatedAt:     at,
		}); err != nil {
			t.Fatalf("upsert album metadata %d: %v", row.albumID, err)
		}
	}

	got, err := s.GetUnmatchedAlbum(ctx, unmatched)
	if err != nil {
		t.Fatalf("get unmatched: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the unmatched row")
	}
	if got.AlbumID != unmatched || got.ArtistID != artistID || got.Artist != "Mad Mark" ||
		got.Title != "WOW" || got.Status != catalog.StatusUnmatched ||
		got.Error != "no provider match" || !got.UpdatedAt.Equal(at) {
		t.Fatalf("row = %+v", got)
	}

	if resolved, err := s.GetUnmatchedAlbum(ctx, fresh); err != nil || resolved != nil {
		t.Fatalf("resolved = (%+v, %v), want (nil, nil)", resolved, err)
	}
	if unknown, err := s.GetUnmatchedAlbum(ctx, 999999); err != nil || unknown != nil {
		t.Fatalf("unknown = (%+v, %v), want (nil, nil)", unknown, err)
	}
}
