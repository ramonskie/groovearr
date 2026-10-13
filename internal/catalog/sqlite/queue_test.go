package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
)

// enqueue is a test helper that enqueues an item or fails the test.
func enqueue(t *testing.T, s *Store, item catalog.RefreshItem) int64 {
	t.Helper()
	id, err := s.EnqueueRefresh(context.Background(), item)
	if err != nil {
		t.Fatalf("enqueue %+v: %v", item, err)
	}
	return id
}

func entityIDs(items []catalog.RefreshItem) []int64 {
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.EntityID)
	}
	return ids
}

// ─── Pure backoff ────────────────────────────────────────────────────

// TestBackoffFor covers the pure, deterministic retry schedule.
func TestBackoffFor(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{"zero uses base", 0, catalog.BackoffBase},
		{"negative uses base", -1, catalog.BackoffBase},
		{"first attempt is base", 1, catalog.BackoffBase},
		{"second doubles", 2, 2 * catalog.BackoffBase},
		{"third quadruples", 3, 4 * catalog.BackoffBase},
		{"fourth doubles again", 4, 8 * catalog.BackoffBase},
		{"far future is capped", 100, catalog.BackoffCap},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalog.BackoffFor(tc.attempts); got != tc.want {
				t.Errorf("BackoffFor(%d) = %v, want %v", tc.attempts, got, tc.want)
			}
		})
	}

	// Monotonic non-decreasing and never exceeds the cap.
	prev := time.Duration(0)
	for a := 1; a <= 200; a++ {
		d := catalog.BackoffFor(a)
		if d < prev {
			t.Fatalf("BackoffFor not monotonic at %d: %v < %v", a, d, prev)
		}
		if d > catalog.BackoffCap {
			t.Fatalf("BackoffFor(%d) = %v exceeds cap %v", a, d, catalog.BackoffCap)
		}
		prev = d
	}

	if catalog.UnmatchedRetry != 90*24*time.Hour {
		t.Errorf("UnmatchedRetry = %v, want 90 days", catalog.UnmatchedRetry)
	}
}

// ─── Enqueue + claim timing ──────────────────────────────────────────

// TestEnqueueRefreshClaimDueAndTiming is table-driven over the due_at boundary.
func TestEnqueueRefreshClaimDueAndTiming(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		dueAt     time.Time
		claimAt   time.Time
		wantClaim bool
	}{
		{"due in the past is claimable", base.Add(-time.Minute), base, true},
		{"due exactly now is claimable", base, base, true},
		{"due in the future is not claimable", base.Add(time.Hour), base, false},
		{"claim exactly at due is claimable", base.Add(time.Hour), base.Add(time.Hour), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			ctx := context.Background()

			id := enqueue(t, s, catalog.RefreshItem{
				EntityType: catalog.EntityAlbum,
				EntityID:   1,
				Reason:     catalog.ReasonCold,
				Priority:   catalog.PriorityDefault,
				DueAt:      tc.dueAt,
			})
			if id <= 0 {
				t.Fatalf("id = %d, want > 0", id)
			}

			got, err := s.ClaimDueRefresh(ctx, tc.claimAt, 10)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if tc.wantClaim {
				if len(got) != 1 || got[0].ID != id {
					t.Fatalf("claim = %+v, want the queued item", got)
				}
				if got[0].Attempts != 0 || got[0].LastError != "" {
					t.Errorf("new item = %+v, want attempts 0 / no error", got[0])
				}
			} else if len(got) != 0 {
				t.Fatalf("claim = %+v, want empty", got)
			}
		})
	}
}

// TestClaimDueRefreshOrder verifies priority DESC then due_at ASC ordering.
func TestClaimDueRefreshOrder(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: 1, Priority: catalog.PriorityLow, DueAt: base.Add(-3 * time.Hour)})
	enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: 2, Priority: catalog.PriorityDefault, DueAt: base.Add(-2 * time.Hour)})
	enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: 3, Priority: catalog.PriorityHigh, DueAt: base.Add(-time.Hour)})
	// Same high priority as #3 but earlier due → claimed before #3.
	enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: 4, Priority: catalog.PriorityHigh, DueAt: base.Add(-2 * time.Hour)})

	got, err := s.ClaimDueRefresh(ctx, base, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	wantOrder := []int64{4, 3, 2, 1}
	if len(got) != len(wantOrder) {
		t.Fatalf("claim returned %d items, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].EntityID != want {
			t.Errorf("position %d = entity %d, want %d (order %v)", i, got[i].EntityID, want, entityIDs(got))
		}
	}
}

// TestEnqueueRefreshDefaultsPriority verifies an omitted priority uses the
// schema/interface default rather than 0.
func TestEnqueueRefreshDefaultsPriority(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: 1, DueAt: base})

	got, err := s.ClaimDueRefresh(ctx, base, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim = (%v, %v), want 1", got, err)
	}
	if got[0].Priority != catalog.PriorityDefault {
		t.Errorf("priority = %d, want default %d", got[0].Priority, catalog.PriorityDefault)
	}
}

// ─── Debounce / uniqueness guard ─────────────────────────────────────

// TestEnqueueRefreshDebouncesPerEntity verifies the UNIQUE(entity_type,
// entity_id) upsert: one row, reason/priority/due updated, attempts preserved.
func TestEnqueueRefreshDebouncesPerEntity(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	first := enqueue(t, s, catalog.RefreshItem{
		EntityType: catalog.EntityArtist, EntityID: 7,
		Reason: catalog.ReasonEvent, Priority: catalog.PriorityLow, DueAt: base,
	})
	// Simulate a failed attempt so attempts/last_error must survive re-enqueue.
	if err := s.RetryRefresh(ctx, first, 3, base.Add(catalog.BackoffFor(3)), "boom"); err != nil {
		t.Fatalf("retry: %v", err)
	}

	// Re-enqueue with a higher priority and an earlier due; same entity.
	second := enqueue(t, s, catalog.RefreshItem{
		EntityType: catalog.EntityArtist, EntityID: 7,
		Reason: catalog.ReasonManual, Priority: catalog.PriorityHigh, DueAt: base,
	})
	if second != first {
		t.Errorf("debounce id = %d, want same row %d", second, first)
	}
	if n, err := s.CountQueuedRefresh(ctx); err != nil || n != 1 {
		t.Fatalf("count = %d (err %v), want 1", n, err)
	}

	got, err := s.ClaimDueRefresh(ctx, base, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim = (%v, %v), want one item", got, err)
	}
	if got[0].Priority != catalog.PriorityHigh {
		t.Errorf("priority = %d, want %d (raise only)", got[0].Priority, catalog.PriorityHigh)
	}
	if got[0].Reason != catalog.ReasonManual {
		t.Errorf("reason = %q, want %q", got[0].Reason, catalog.ReasonManual)
	}
	// attempts/last_error preserved across a re-enqueue (no backoff reset).
	if got[0].Attempts != 3 || got[0].LastError != "boom" {
		t.Errorf("attempts/last_error = %d/%q, want 3/boom preserved", got[0].Attempts, got[0].LastError)
	}

	// A lower-priority re-enqueue must not lower the priority.
	enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityArtist, EntityID: 7, Priority: catalog.PriorityLow, DueAt: base})
	got, _ = s.ClaimDueRefresh(ctx, base, 10)
	if got[0].Priority != catalog.PriorityHigh {
		t.Errorf("priority after lower re-enqueue = %d, want %d", got[0].Priority, catalog.PriorityHigh)
	}
}

// TestEnqueueRefreshRejectsInvalidEntity covers boundary validation.
func TestEnqueueRefreshRejectsInvalidEntity(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name string
		item catalog.RefreshItem
	}{
		{"unknown type", catalog.RefreshItem{EntityType: catalog.EntityType("genre"), EntityID: 1}},
		{"empty type", catalog.RefreshItem{EntityID: 1}},
		{"non-positive id", catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.EnqueueRefresh(ctx, tc.item); err == nil {
				t.Errorf("EnqueueRefresh(%+v) = nil error, want validation error", tc.item)
			}
		})
	}
}

// ─── Retry / backoff persistence ─────────────────────────────────────

// TestRetryRefreshBackoff verifies a failed attempt reschedules by exactly
// BackoffFor and persists attempts/last_error.
func TestRetryRefreshBackoff(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	id := enqueue(t, s, catalog.RefreshItem{
		EntityType: catalog.EntityAlbum, EntityID: 1, Priority: catalog.PriorityDefault, DueAt: base,
	})
	if due, _ := s.ClaimDueRefresh(ctx, base, 10); len(due) != 1 {
		t.Fatalf("initial claim = %v, want 1", due)
	}

	attempts := 1
	next := base.Add(catalog.BackoffFor(attempts))
	if err := s.RetryRefresh(ctx, id, attempts, next, "rate limited"); err != nil {
		t.Fatalf("retry: %v", err)
	}

	// Not due before the backoff elapses.
	got, _ := s.ClaimDueRefresh(ctx, base.Add(catalog.BackoffFor(attempts)-time.Second), 10)
	if len(got) != 0 {
		t.Fatalf("claimed before backoff = %v, want empty", got)
	}
	// Due again exactly at nextAttempt, with attempts and last_error persisted.
	got, err := s.ClaimDueRefresh(ctx, next, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim at next = (%v, %v), want 1", got, err)
	}
	if got[0].Attempts != 1 || got[0].LastError != "rate limited" {
		t.Errorf("item = %+v, want attempts 1 / last_error %q", got[0], "rate limited")
	}

	// The schedule grows: the second retry is due later than the first.
	if catalog.BackoffFor(2) <= catalog.BackoffFor(1) {
		t.Fatalf("BackoffFor not growing: %v then %v", catalog.BackoffFor(1), catalog.BackoffFor(2))
	}
}

// ─── Finish / delete / bump / count ──────────────────────────────────

func TestFinishRefreshRemovesAndCount(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	a := enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: 1, DueAt: base})
	enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: 2, DueAt: base})

	if n, _ := s.CountQueuedRefresh(ctx); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
	if err := s.FinishRefresh(ctx, a, catalog.StatusFresh); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if n, _ := s.CountQueuedRefresh(ctx); n != 1 {
		t.Fatalf("count after finish = %d, want 1", n)
	}
	got, _ := s.ClaimDueRefresh(ctx, base, 10)
	if len(got) != 1 || got[0].EntityID != 2 {
		t.Errorf("claim after finish = %v, want only entity 2", got)
	}
	// Finishing an already-removed row is a no-op, not an error.
	if err := s.FinishRefresh(ctx, a, catalog.StatusFresh); err != nil {
		t.Errorf("finish again: %v", err)
	}
}

func TestDeleteAndBumpRefreshPriority(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	id := enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityArtist, EntityID: 5, Priority: catalog.PriorityLow, DueAt: base})
	enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityArtist, EntityID: 6, Priority: catalog.PriorityDefault, DueAt: base})

	// Bump entity 5 above entity 6.
	if err := s.BumpRefreshPriority(ctx, catalog.EntityArtist, 5, catalog.PriorityHigh); err != nil {
		t.Fatalf("bump: %v", err)
	}
	got, _ := s.ClaimDueRefresh(ctx, base, 10)
	if len(got) != 2 || got[0].EntityID != 5 {
		t.Fatalf("after bump order = %v, want entity 5 first", entityIDs(got))
	}

	// Bumping a non-queued entity is a no-op, not an error.
	if err := s.BumpRefreshPriority(ctx, catalog.EntityArtist, 999, catalog.PriorityHigh); err != nil {
		t.Errorf("bump absent: %v", err)
	}

	if err := s.DeleteRefresh(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := s.CountQueuedRefresh(ctx); n != 1 {
		t.Errorf("count after delete = %d, want 1", n)
	}
	// Deleting an absent row is a no-op, not an error.
	if err := s.DeleteRefresh(ctx, id); err != nil {
		t.Errorf("delete absent: %v", err)
	}
}

func TestClaimDueRefreshLimit(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	for i := int64(1); i <= 3; i++ {
		enqueue(t, s, catalog.RefreshItem{EntityType: catalog.EntityAlbum, EntityID: i, Priority: catalog.PriorityDefault, DueAt: base})
	}

	got, err := s.ClaimDueRefresh(ctx, base, 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("limit 2 = (%v, %v), want 2", got, err)
	}

	// Non-positive limit: empty non-nil slice, no error.
	got, err = s.ClaimDueRefresh(ctx, base, 0)
	if err != nil {
		t.Fatalf("limit 0 err: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("limit 0 = %v, want empty non-nil", got)
	}
}

// TestStore_ReArmTerminalAlbums covers the explicit override of the terminal
// parks: parked unmatched rows are moved to due-now, a failed album whose queue
// row was deleted by the terminal transition gets a fresh one, a fresh album is
// untouched, and limit caps the rows touched.
func TestStore_ReArmTerminalAlbums(t *testing.T) {
	ctx := context.Background()
	s, db := newTestStore(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	parked := now.Add(catalog.UnmatchedRetry)

	artistID := insertArtist(t, db, "Aphex Twin")

	albumWithStatus := func(title string, status catalog.RefreshStatus) int64 {
		albumID := insertAlbum(t, db, artistID, title)
		if err := s.UpsertAlbumMetadata(ctx, catalog.AlbumMetadata{
			AlbumID:       albumID,
			RefreshStatus: status,
		}); err != nil {
			t.Fatalf("upsert metadata %q: %v", title, err)
		}
		return albumID
	}
	park := func(albumID int64) {
		enqueue(t, s, catalog.RefreshItem{
			EntityType: catalog.EntityAlbum,
			EntityID:   albumID,
			Priority:   catalog.PriorityDefault,
			DueAt:      parked,
		})
	}

	unmatched1 := albumWithStatus("Drukqs", catalog.StatusUnmatched)
	unmatched2 := albumWithStatus("Richard D. James Album", catalog.StatusUnmatched)
	// A failed album has NO queue row: the terminal transition deletes it
	// (retryOrFail → FinishRefresh). It must still be recoverable.
	failed := albumWithStatus("Selected Ambient Works", catalog.StatusFailed)
	fresh := albumWithStatus("Fresh Album", catalog.StatusFresh)

	park(unmatched1)
	park(unmatched2)
	park(fresh)

	n, err := s.ReArmTerminalAlbums(ctx, now, 0)
	if err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	if n != 3 {
		t.Fatalf("re-armed = %d, want 3 (2 unmatched rows + 1 failed insert)", n)
	}

	// The failed album now has a row; the fresh album stays scheduled.
	st, err := s.QueueStats(ctx, now)
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if st.DueNow != 3 {
		t.Errorf("DueNow = %d, want 3", st.DueNow)
	}
	if st.Scheduled != 1 {
		t.Errorf("Scheduled = %d, want 1 (the fresh album)", st.Scheduled)
	}

	due, err := s.ClaimDueRefresh(ctx, now, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	duePriority := map[int64]int{}
	for _, it := range due {
		duePriority[it.EntityID] = it.Priority
	}
	for _, id := range []int64{unmatched1, unmatched2, failed} {
		p, ok := duePriority[id]
		if !ok {
			t.Errorf("terminal album %d not due after re-arm", id)
			continue
		}
		if p != catalog.PriorityHigh {
			t.Errorf("album %d priority = %d, want %d", id, p, catalog.PriorityHigh)
		}
	}
	if _, ok := duePriority[fresh]; ok {
		t.Errorf("fresh album %d must not be re-armed", fresh)
	}

	// limit must cap the rows: re-park all three terminal albums, then re-arm one
	// and confirm exactly one row moved to due-now.
	for _, id := range []int64{unmatched1, unmatched2, failed} {
		park(id)
	}
	before, err := s.QueueStats(ctx, now)
	if err != nil {
		t.Fatalf("queue stats before limit: %v", err)
	}
	n, err = s.ReArmTerminalAlbums(ctx, now, 1)
	if err != nil {
		t.Fatalf("re-arm limit: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-armed with limit 1 = %d, want 1", n)
	}
	after, err := s.QueueStats(ctx, now)
	if err != nil {
		t.Fatalf("queue stats after limit: %v", err)
	}
	if got := after.DueNow - before.DueNow; got != 1 {
		t.Errorf("due-now delta = %d, want 1 (LIMIT must cap the rows)", got)
	}
}
