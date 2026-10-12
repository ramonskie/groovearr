package refresh

import (
	"context"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/domain"
)

// fakeWarmStore is an in-memory refresh.WarmStore for warm-candidate tests.
type fakeWarmStore struct {
	ids    []int64
	meta   map[int64]*catalog.AlbumMetadata
	tracks map[int64][]catalog.AlbumTrack
}

func (f *fakeWarmStore) ListAlbumsDueForRefresh(context.Context, time.Time, int) ([]int64, error) {
	return f.ids, nil
}

func (f *fakeWarmStore) GetAlbumMetadata(_ context.Context, albumID int64) (*catalog.AlbumMetadata, error) {
	return f.meta[albumID], nil
}

func (f *fakeWarmStore) GetAlbumTracks(_ context.Context, albumID int64) ([]catalog.AlbumTrack, error) {
	return f.tracks[albumID], nil
}

// fakeWarmLibrary is an in-memory refresh.WarmLibrary.
type fakeWarmLibrary struct {
	tracks map[int64][]domain.Track
}

func (f *fakeWarmLibrary) GetTracksByAlbum(_ context.Context, albumID int64) ([]domain.Track, error) {
	return f.tracks[albumID], nil
}

var (
	_ WarmStore   = (*fakeWarmStore)(nil)
	_ WarmLibrary = (*fakeWarmLibrary)(nil)
)

// warmTestOpts drives the pure selection with the shared deterministic clock.
func warmTestOpts() WarmOptions {
	return WarmOptions{
		Limit:     50,
		ScanLimit: 100,
		now:       func() time.Time { return fixedNow },
	}
}

// TestWarmCandidates_ExcludesFreshTerminalAlbum proves a warm pass does not
// re-arm an unmatched/failed album whose long retry has not elapsed.
func TestWarmCandidates_ExcludesFreshTerminalAlbum(t *testing.T) {
	for _, status := range []catalog.RefreshStatus{catalog.StatusUnmatched, catalog.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			// Arrange
			store := &fakeWarmStore{
				ids: []int64{1},
				meta: map[int64]*catalog.AlbumMetadata{
					1: {AlbumID: 1, RefreshStatus: status, UpdatedAt: fixedNow.Add(-time.Hour)},
				},
			}

			// Act
			got, err := WarmCandidates(context.Background(), store, &fakeWarmLibrary{}, warmTestOpts())

			// Assert
			if err != nil {
				t.Fatalf("WarmCandidates: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("candidates = %v, want none for a fresh %s album", got, status)
			}
		})
	}
}

// TestWarmCandidates_IncludesTerminalAlbumAfterRetryElapsed proves the exclusion
// is a window: once the retry has elapsed the album is warmed again.
func TestWarmCandidates_IncludesTerminalAlbumAfterRetryElapsed(t *testing.T) {
	// Arrange
	store := &fakeWarmStore{
		ids: []int64{1},
		meta: map[int64]*catalog.AlbumMetadata{
			1: {
				AlbumID:       1,
				RefreshStatus: catalog.StatusUnmatched,
				UpdatedAt:     fixedNow.Add(-catalog.UnmatchedRetry - time.Hour),
			},
		},
	}

	// Act
	got, err := WarmCandidates(context.Background(), store, &fakeWarmLibrary{}, warmTestOpts())

	// Assert
	if err != nil {
		t.Fatalf("WarmCandidates: %v", err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("candidates = %v, want [1] after the retry elapsed", got)
	}
}

// TestWarmCandidates_IncludesNonTerminalNeverMaterialized proves a normal
// never-materialized album (pending/fresh metadata, no provider tracks) is still
// selected.
func TestWarmCandidates_IncludesNonTerminalNeverMaterialized(t *testing.T) {
	// Arrange
	store := &fakeWarmStore{
		ids: []int64{1},
		meta: map[int64]*catalog.AlbumMetadata{
			1: {AlbumID: 1, RefreshStatus: catalog.StatusPending, UpdatedAt: fixedNow},
		},
	}

	// Act
	got, err := WarmCandidates(context.Background(), store, &fakeWarmLibrary{}, warmTestOpts())

	// Assert
	if err != nil {
		t.Fatalf("WarmCandidates: %v", err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("candidates = %v, want [1] for a pending album", got)
	}
}

// TestWarmCandidate_Excluded is table-driven over the pure exclusion predicate.
func TestWarmCandidate_Excluded(t *testing.T) {
	tests := []struct {
		name string
		c    WarmCandidate
		want bool
	}{
		{name: "no metadata", c: WarmCandidate{}, want: false},
		{name: "fresh status", c: WarmCandidate{RefreshStatus: catalog.StatusFresh, StatusUpdatedAt: fixedNow.Add(-time.Hour)}, want: false},
		{name: "unmatched within retry", c: WarmCandidate{RefreshStatus: catalog.StatusUnmatched, StatusUpdatedAt: fixedNow.Add(-time.Hour)}, want: true},
		{name: "failed within retry", c: WarmCandidate{RefreshStatus: catalog.StatusFailed, StatusUpdatedAt: fixedNow.Add(-time.Hour)}, want: true},
		{name: "unmatched retry elapsed", c: WarmCandidate{RefreshStatus: catalog.StatusUnmatched, StatusUpdatedAt: fixedNow.Add(-catalog.UnmatchedRetry)}, want: false},
		{name: "terminal zero timestamp", c: WarmCandidate{RefreshStatus: catalog.StatusUnmatched}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Excluded(fixedNow); got != tc.want {
				t.Fatalf("Excluded = %v, want %v", got, tc.want)
			}
		})
	}
}
