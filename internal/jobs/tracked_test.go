package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/tracking"
)

// fakeTrackedRefresher is an in-memory TrackedRefresher: it returns a fixed
// artist list and records every RefreshArtist / SearchMissing call, with
// per-artist and per-method errors.
type fakeTrackedRefresher struct {
	artists      []domain.TrackedArtist
	listErr      error
	getErr       error
	refreshErr   map[int64]error
	searchErr    error
	searchResult *tracking.SearchResult
	// searchBlocksUntilCtx makes SearchMissing behave like a wedged provider:
	// it blocks until the caller's context is done and returns that error, so
	// tests can exercise the per-run timeout path.
	searchBlocksUntilCtx bool
	refreshed            []int64
	searched             []int64
}

func (f *fakeTrackedRefresher) ListTrackedArtists(context.Context) ([]domain.TrackedArtist, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.artists, nil
}

func (f *fakeTrackedRefresher) GetTrackedArtist(_ context.Context, id int64) (*domain.TrackedArtist, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.artists {
		if f.artists[i].ID == id {
			cp := f.artists[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeTrackedRefresher) RefreshArtist(_ context.Context, id int64) (*tracking.RefreshResult, error) {
	f.refreshed = append(f.refreshed, id)
	if err := f.refreshErr[id]; err != nil {
		return nil, err
	}
	return &tracking.RefreshResult{}, nil
}

func (f *fakeTrackedRefresher) SearchMissing(ctx context.Context, id int64) (*tracking.SearchResult, error) {
	f.searched = append(f.searched, id)
	if f.searchBlocksUntilCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	if f.searchResult != nil {
		return f.searchResult, nil
	}
	return &tracking.SearchResult{}, nil
}

func trackedArtist(id int64, name, provider string, auto bool) domain.TrackedArtist {
	return domain.TrackedArtist{ID: id, Name: name, ProviderName: provider, AutoRefresh: auto}
}

func TestRefreshTracked(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	boom := errors.New("provider exploded")

	tests := []struct {
		name            string
		nilTracking     bool
		artists         []domain.TrackedArtist
		listErr         error
		cooling         map[string]bool
		refreshErr      map[int64]error
		ctx             context.Context
		wantErr         error
		wantAnyErr      bool
		wantRefreshed   []int64
		wantMinReports  int
		wantMsgContains string
	}{
		{
			name:        "nil tracking returns a clear error",
			nilTracking: true,
			wantAnyErr:  true,
		},
		{
			name:            "no artists reports and returns nil",
			wantMinReports:  1,
			wantMsgContains: "no auto-refresh",
		},
		{
			name: "only auto-refresh artists are refreshed",
			artists: []domain.TrackedArtist{
				trackedArtist(1, "Alpha", "deezer", true),
				trackedArtist(2, "ManualArtist", "spotify", false),
				trackedArtist(3, "Beta", "deezer", true),
			},
			wantRefreshed:   []int64{1, 3},
			wantMinReports:  3,
			wantMsgContains: "Alpha",
		},
		{
			name: "cooling-down artist is skipped without a refresh call",
			artists: []domain.TrackedArtist{
				trackedArtist(1, "Hot", "deezer", true),
				trackedArtist(2, "Cool", "spotify", true),
			},
			cooling:         map[string]bool{"deezer": true},
			wantRefreshed:   []int64{2},
			wantMinReports:  2,
			wantMsgContains: "cooling down",
		},
		{
			name: "single artist failure continues and is counted",
			artists: []domain.TrackedArtist{
				trackedArtist(1, "Breaks", "deezer", true),
				trackedArtist(2, "Works", "spotify", true),
			},
			refreshErr:     map[int64]error{1: boom},
			wantAnyErr:     true,
			wantRefreshed:  []int64{1, 2},
			wantMinReports: 2,
		},
		{
			name: "context cancellation stops before refreshing",
			artists: []domain.TrackedArtist{
				trackedArtist(1, "Never", "deezer", true),
			},
			ctx:           cancelledCtx,
			wantErr:       context.Canceled,
			wantRefreshed: nil,
		},
		{
			name:       "list error propagates",
			listErr:    errors.New("store down"),
			wantAnyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var refresher *fakeTrackedRefresher
			var trackingDep TrackedRefresher
			if !tt.nilTracking {
				refresher = &fakeTrackedRefresher{
					artists:    tt.artists,
					listErr:    tt.listErr,
					refreshErr: tt.refreshErr,
				}
				trackingDep = refresher
			}
			deps := RunnerDeps{Tracking: trackingDep, Log: testLogger()}
			if tt.cooling != nil {
				deps.RateLimit = &stubCooldown{cooling: tt.cooling}
			}
			r := NewRunners(deps)

			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			var reports []Report
			err := r.RefreshTracked()(ctx, func(rep Report) { reports = append(reports, rep) })

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantAnyErr:
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			var got []int64
			if refresher != nil {
				got = refresher.refreshed
			}
			if !equalInt64s(got, tt.wantRefreshed) {
				t.Errorf("refreshed = %v, want %v", got, tt.wantRefreshed)
			}

			if len(reports) < tt.wantMinReports {
				t.Errorf("reports = %d, want at least %d", len(reports), tt.wantMinReports)
			}
			if tt.wantMsgContains != "" {
				found := false
				for _, rep := range reports {
					if strings.Contains(strings.ToLower(rep.Message), strings.ToLower(tt.wantMsgContains)) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no report message contained %q; reports = %v", tt.wantMsgContains, reports)
				}
			}
		})
	}
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reportsContain reports whether any report message contains sub
// (case-insensitively).
func reportsContain(reports []Report, sub string) bool {
	for _, rep := range reports {
		if strings.Contains(strings.ToLower(rep.Message), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

func TestRefreshTrackedArtist(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	boom := errors.New("provider exploded")
	artist := trackedArtist(7, "Tool", "deezer", true)

	tests := []struct {
		name            string
		nilTracking     bool
		artists         []domain.TrackedArtist
		getErr          error
		cooling         map[string]bool
		refreshErr      map[int64]error
		ctx             context.Context
		wantErr         error
		wantAnyErr      bool
		wantRefreshed   []int64
		wantMinReports  int
		wantMsgContains string
	}{
		{
			name:        "nil tracking returns a clear error",
			nilTracking: true,
			wantAnyErr:  true,
		},
		{
			name:            "success refreshes the artist and reports progress",
			artists:         []domain.TrackedArtist{artist},
			wantRefreshed:   []int64{7},
			wantMinReports:  1,
			wantMsgContains: "Tool",
		},
		{
			name:            "cooling-down provider is skipped without a refresh",
			artists:         []domain.TrackedArtist{artist},
			cooling:         map[string]bool{"deezer": true},
			wantRefreshed:   nil,
			wantMinReports:  1,
			wantMsgContains: "cooling down",
		},
		{
			name:          "refresh failure is returned and logged",
			artists:       []domain.TrackedArtist{artist},
			refreshErr:    map[int64]error{7: boom},
			wantAnyErr:    true,
			wantRefreshed: []int64{7},
		},
		{
			name:          "context cancellation stops before refreshing",
			artists:       []domain.TrackedArtist{artist},
			ctx:           cancelledCtx,
			wantErr:       context.Canceled,
			wantRefreshed: nil,
		},
		{
			name:       "unknown artist is an error",
			wantAnyErr: true,
		},
		{
			name:       "get error propagates",
			getErr:     errors.New("store down"),
			wantAnyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var refresher *fakeTrackedRefresher
			var trackingDep TrackedRefresher
			if !tt.nilTracking {
				refresher = &fakeTrackedRefresher{
					artists:    tt.artists,
					getErr:     tt.getErr,
					refreshErr: tt.refreshErr,
				}
				trackingDep = refresher
			}
			deps := RunnerDeps{Tracking: trackingDep, Log: testLogger()}
			if tt.cooling != nil {
				deps.RateLimit = &stubCooldown{cooling: tt.cooling}
			}
			r := NewRunners(deps)

			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			var reports []Report
			err := r.RefreshTrackedArtist(7)(ctx, func(rep Report) { reports = append(reports, rep) })

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantAnyErr:
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			var got []int64
			if refresher != nil {
				got = refresher.refreshed
			}
			if !equalInt64s(got, tt.wantRefreshed) {
				t.Errorf("refreshed = %v, want %v", got, tt.wantRefreshed)
			}
			if len(reports) < tt.wantMinReports {
				t.Errorf("reports = %d, want at least %d", len(reports), tt.wantMinReports)
			}
			if tt.wantMsgContains != "" && !reportsContain(reports, tt.wantMsgContains) {
				t.Errorf("no report message contained %q; reports = %v", tt.wantMsgContains, reports)
			}
		})
	}
}

func TestSearchMissingArtist(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	boom := errors.New("search exploded")

	tests := []struct {
		name            string
		nilTracking     bool
		searchErr       error
		searchResult    *tracking.SearchResult
		blockSearch     bool
		timeout         time.Duration
		ctx             context.Context
		wantErr         error
		wantAnyErr      bool
		wantSearched    []int64
		wantMinReports  int
		wantMsgContains string
	}{
		{
			name:        "nil tracking returns a clear error",
			nilTracking: true,
			wantAnyErr:  true,
		},
		{
			name:            "success reports the queued/skipped summary",
			searchResult:    &tracking.SearchResult{Queued: 2, Skipped: 1},
			wantSearched:    []int64{7},
			wantMinReports:  1,
			wantMsgContains: "queued 2",
		},
		{
			name:            "recorded search errors fail the job and are reported",
			searchResult:    &tracking.SearchResult{Queued: 0, Errors: 1},
			wantAnyErr:      true,
			wantSearched:    []int64{7},
			wantMinReports:  1,
			wantMsgContains: "errors 1",
		},
		{
			name:         "service error propagates",
			searchErr:    boom,
			wantAnyErr:   true,
			wantSearched: []int64{7},
		},
		{
			name:         "context cancellation stops before searching",
			ctx:          cancelledCtx,
			wantErr:      context.Canceled,
			wantSearched: nil,
		},
		{
			name:         "per-run timeout fails the job instead of pinning the slot",
			blockSearch:  true,
			timeout:      20 * time.Millisecond,
			wantErr:      context.DeadlineExceeded,
			wantSearched: []int64{7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.timeout > 0 {
				// Shorten the package budget so the timeout path runs fast;
				// restore it so sibling subtests keep production behavior.
				prev := trackedArtistRefreshTimeout
				trackedArtistRefreshTimeout = tt.timeout
				defer func() { trackedArtistRefreshTimeout = prev }()
			}

			var refresher *fakeTrackedRefresher
			var trackingDep TrackedRefresher
			if !tt.nilTracking {
				refresher = &fakeTrackedRefresher{
					searchErr:            tt.searchErr,
					searchResult:         tt.searchResult,
					searchBlocksUntilCtx: tt.blockSearch,
				}
				trackingDep = refresher
			}
			r := NewRunners(RunnerDeps{Tracking: trackingDep, Log: testLogger()})

			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			var reports []Report
			err := r.SearchMissingArtist(7)(ctx, func(rep Report) { reports = append(reports, rep) })

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantAnyErr:
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			var got []int64
			if refresher != nil {
				got = refresher.searched
			}
			if !equalInt64s(got, tt.wantSearched) {
				t.Errorf("searched = %v, want %v", got, tt.wantSearched)
			}
			if len(reports) < tt.wantMinReports {
				t.Errorf("reports = %d, want at least %d", len(reports), tt.wantMinReports)
			}
			if tt.wantMsgContains != "" && !reportsContain(reports, tt.wantMsgContains) {
				t.Errorf("no report message contained %q; reports = %v", tt.wantMsgContains, reports)
			}
		})
	}
}
