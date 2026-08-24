package playlist

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	dlsqlite "github.com/ramonskie/groovearr/internal/download/sqlite"
	"github.com/ramonskie/groovearr/internal/events"
	libsqlite "github.com/ramonskie/groovearr/internal/library/sqlite"
	"github.com/ramonskie/groovearr/internal/matching"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestDownloadMissingIdempotent verifies a track already added to the download
// pipeline (in any state) is not queued again on a second call — the periodic
// playlist sync can never double-queue.
func TestDownloadMissingIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"

	libStore, err := libsqlite.New(dbPath, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer libStore.Close()

	// The download store shares the library store's connection (same DB file,
	// WAL mode, single write connection) — mirroring how app.go wires it.
	dlStore := dlsqlite.New(libStore.DB(), discardLogger())

	bus := events.NewInMemoryEventBus(discardLogger())
	downloadSvc := download.NewService(dlStore, bus, discardLogger())

	// Create a playlist with one track, unlinked.
	pid, err := libStore.UpsertPlaylist(ctx, &domain.Playlist{Name: "Test", SyncMode: domain.SyncModeMirror})
	if err != nil {
		t.Fatal(err)
	}
	pt := domain.PlaylistTrack{
		PlaylistID: pid, Position: 1,
		SourceTrackID: "src-1", Title: "Song", Artist: "Artist", ISRC: "US1234567890",
	}
	if err := libStore.UpsertPlaylistTrack(ctx, &pt); err != nil {
		t.Fatal(err)
	}

	svc := NewService(
		NewRegistry(),
		libStore,
		download.NewRegistry(),
		downloadSvc,
		func() config.Config { return config.Config{} },
		nil,
		nil,
		discardLogger(),
	)
	svc.matcher = matching.New()

	// First call queues the track.
	n, err := svc.DownloadMissing(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("first DownloadMissing queued %d, want 1", n)
	}

	existing, err := dlStore.ListByPlaylist(ctx, "1")
	if err != nil || len(existing) != 1 {
		t.Fatalf("pipeline records = %v, err = %v; want exactly 1", existing, err)
	}

	// Second call (periodic re-sync) while the record is mid-flight (queued,
	// downloading, ...) must queue nothing.
	n2, err := svc.DownloadMissing(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second DownloadMissing queued %d, want 0 (already added)", n2)
	}
	recs, _ := dlStore.ListByPlaylist(ctx, "1")
	if len(recs) != 1 {
		t.Fatalf("pipeline has %d records after re-sync, want 1 (no duplicate)", len(recs))
	}
}

// TestDownloadMissingRequeuesExhaustedRetries verifies a download the monitor
// has given up on (StateFailed with RetryCount >= MaxRetries) is re-queued by
// the next periodic sync, while a failure still within the retry budget stays
// "already added" (the monitor retries it itself).
func TestDownloadMissingRequeuesExhaustedRetries(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"

	libStore, err := libsqlite.New(dbPath, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer libStore.Close()

	// The download store shares the library store's connection (same DB file,
	// WAL mode, single write connection) — mirroring how app.go wires it.
	dlStore := dlsqlite.New(libStore.DB(), discardLogger())

	bus := events.NewInMemoryEventBus(discardLogger())
	downloadSvc := download.NewService(dlStore, bus, discardLogger())

	pid, err := libStore.UpsertPlaylist(ctx, &domain.Playlist{Name: "Test", SyncMode: domain.SyncModeMirror})
	if err != nil {
		t.Fatal(err)
	}
	pt := domain.PlaylistTrack{
		PlaylistID: pid, Position: 1,
		SourceTrackID: "src-1", Title: "Song", Artist: "Artist", ISRC: "US1234567890",
	}
	if err := libStore.UpsertPlaylistTrack(ctx, &pt); err != nil {
		t.Fatal(err)
	}

	svc := NewService(
		NewRegistry(),
		libStore,
		download.NewRegistry(),
		downloadSvc,
		func() config.Config { return config.Config{} },
		nil,
		nil,
		discardLogger(),
	)
	svc.matcher = matching.New()

	// Shrink the cooldown so the test can walk the full lifecycle quickly, but
	// keep it larger than the write+read round-trip (WAL checkpoint, query
	// overhead) so the within-cooldown assertion isn't spuriously past the
	// window by the time it runs.
	origCooldown := requeueCooldown
	requeueCooldown = 500 * time.Millisecond
	defer func() { requeueCooldown = origCooldown }()

	// First call queues the track, then the pipeline marks it failed WITH
	// retries still remaining — the monitor owns the retry, sync must skip.
	n, err := svc.DownloadMissing(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("first DownloadMissing queued %d, want 1", n)
	}
	recs, _ := dlStore.ListByPlaylist(ctx, "1")
	rec := recs[0]
	rec.State = download.StateFailed
	rec.RetryCount = 1 // within budget (MaxRetries=5)
	if err := dlStore.Update(ctx, &rec); err != nil {
		t.Fatal(err)
	}

	n2, err := svc.DownloadMissing(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("failed-within-budget DownloadMissing queued %d, want 0 (monitor retries it)", n2)
	}

	// Exhaust the retry budget but keep the record young (within cooldown) —
	// the sync must still skip: the record is re-armed only after cooldown.
	recs, _ = dlStore.ListByPlaylist(ctx, "1")
	rec = recs[0]
	rec.State = download.StateFailed
	rec.RetryCount = download.MaxRetries
	if err := dlStore.Update(ctx, &rec); err != nil {
		t.Fatal(err)
	}

	n3, err := svc.DownloadMissing(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if n3 != 0 {
		t.Fatalf("exhausted-within-cooldown DownloadMissing queued %d, want 0 (cooldown not elapsed)", n3)
	}
	recs, _ = dlStore.ListByPlaylist(ctx, "1")
	if len(recs) != 1 {
		t.Fatalf("pipeline has %d records, want 1 (no re-queue inside cooldown)", len(recs))
	}

	// Let the cooldown elapse, then re-arm in place: the SAME record is
	// re-queued (no duplicate), state reset to queued with a fresh retry budget.
	time.Sleep(600 * time.Millisecond)

	n4, err := svc.DownloadMissing(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if n4 != 1 {
		t.Fatalf("exhausted-past-cooldown DownloadMissing queued %d, want 1 (re-armed)", n4)
	}
	recs, _ = dlStore.ListByPlaylist(ctx, "1")
	if len(recs) != 1 {
		t.Fatalf("pipeline has %d records after re-arm, want 1 (record reused, no duplicate)", len(recs))
	}
	if recs[0].State != download.StateQueued {
		t.Errorf("re-armed record state = %q, want %q", recs[0].State, download.StateQueued)
	}
	if recs[0].RetryCount != 0 {
		t.Errorf("re-armed record RetryCount = %d, want 0 (fresh budget)", recs[0].RetryCount)
	}
}

// TestDownloadMissingDistinctISRCSameTitle verifies two playlist entries that
// share artist+title but have DIFFERENT ISRCs (different releases/masterings)
// are both queued. The artist|title fallback must only apply to tracks that
// carry no ISRC — an ISRC-bearing track dedups by its own ISRC, not by a
// same-titled sibling of a different release.
func TestDownloadMissingDistinctISRCSameTitle(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"

	libStore, err := libsqlite.New(dbPath, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer libStore.Close()

	dlStore := dlsqlite.New(libStore.DB(), discardLogger())
	bus := events.NewInMemoryEventBus(discardLogger())
	downloadSvc := download.NewService(dlStore, bus, discardLogger())

	pid, err := libStore.UpsertPlaylist(ctx, &domain.Playlist{Name: "Test", SyncMode: domain.SyncModeMirror})
	if err != nil {
		t.Fatal(err)
	}

	// First release: ISRC A.
	ptA := domain.PlaylistTrack{
		PlaylistID: pid, Position: 1,
		SourceTrackID: "src-a", Title: "Same Song", Artist: "Artist", ISRC: "USAAA0000001",
	}
	if err := libStore.UpsertPlaylistTrack(ctx, &ptA); err != nil {
		t.Fatal(err)
	}

	svc := NewService(
		NewRegistry(),
		libStore,
		download.NewRegistry(),
		downloadSvc,
		func() config.Config { return config.Config{} },
		nil,
		nil,
		discardLogger(),
	)
	svc.matcher = matching.New()

	if n, err := svc.DownloadMissing(ctx, pid); err != nil || n != 1 {
		t.Fatalf("first sync queued %d (want 1), err=%v", n, err)
	}

	// Second release of the SAME song (same artist+title), different ISRC —
	// added to the playlist after the first sync. It is a distinct track and
	// must be queued, not swallowed by the artist|title dedup of ISRC-A.
	ptB := domain.PlaylistTrack{
		PlaylistID: pid, Position: 2,
		SourceTrackID: "src-b", Title: "Same Song", Artist: "Artist", ISRC: "USBBB0000002",
	}
	if err := libStore.UpsertPlaylistTrack(ctx, &ptB); err != nil {
		t.Fatal(err)
	}

	n, err := svc.DownloadMissing(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("second sync queued %d (want 1 for the distinct ISRC release), err=%v", n, err)
	}

	// A third sync must not re-queue either release.
	n, err = svc.DownloadMissing(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("third sync queued %d, want 0 (both releases already queued)", n)
	}
}
