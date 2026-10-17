package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/refresh"
)

// ─── Test doubles ────────────────────────────────────────────────────

// fakeWarmCatalog is an in-memory refresh.WarmStore. ListAlbumsDueForRefresh
// returns every known album (mirroring the production cutoff of "now", which
// lists all albums; the tier filter is what excludes complete ones).
type fakeWarmCatalog struct {
	albums   []int64
	meta     map[int64]bool
	provider map[int64]int // provider-origin materialized row count
}

func (f *fakeWarmCatalog) ListAlbumsDueForRefresh(_ context.Context, _ time.Time, limit int) ([]int64, error) {
	out := append([]int64(nil), f.albums...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeWarmCatalog) GetAlbumMetadata(_ context.Context, albumID int64) (*catalog.AlbumMetadata, error) {
	if f.meta[albumID] {
		return &catalog.AlbumMetadata{AlbumID: albumID}, nil
	}
	return nil, nil
}

func (f *fakeWarmCatalog) GetAlbumTracks(_ context.Context, albumID int64) ([]catalog.AlbumTrack, error) {
	n := f.provider[albumID]
	rows := make([]catalog.AlbumTrack, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, catalog.AlbumTrack{AlbumID: albumID, Origin: catalog.OriginProvider})
	}
	return rows, nil
}

// materialize simulates a successful provider materialization: the album now has
// a metadata row and a provider tracklist, so it drops out of the next pass when
// the local library is not short.
func (f *fakeWarmCatalog) materialize(albumID int64) {
	if f.meta == nil {
		f.meta = map[int64]bool{}
	}
	if f.provider == nil {
		f.provider = map[int64]int{}
	}
	f.meta[albumID] = true
	if f.provider[albumID] == 0 {
		f.provider[albumID] = 1
	}
}

var _ refresh.WarmStore = (*fakeWarmCatalog)(nil)

// fakeWarmRefresher records every album it is asked to materialize and, by
// default, makes that album complete in the catalog so a later pass skips it.
type fakeWarmRefresher struct {
	cat   *fakeWarmCatalog
	errs  map[int64]error
	calls []int64
}

func (f *fakeWarmRefresher) RefreshAlbum(_ context.Context, albumID int64) (refresh.AlbumOutcome, error) {
	f.calls = append(f.calls, albumID)
	if err := f.errs[albumID]; err != nil {
		return refresh.AlbumOutcome{}, err
	}
	if f.cat != nil {
		f.cat.materialize(albumID)
	}
	return refresh.AlbumOutcome{Matched: true}, nil
}

var _ WarmRefresher = (*fakeWarmRefresher)(nil)

// newWarmRunners wires the warm-library job with fakes. opts may be nil to
// exercise the default bounds.
func newWarmRunners(cat *fakeWarmCatalog, local map[int64][]domain.Track, ref *fakeWarmRefresher, opts func() refresh.WarmOptions) *Runners {
	return NewRunners(RunnerDeps{
		Log:         testLogger(),
		Store:       &organizeRunnerStore{tracks: local},
		Catalog:     cat,
		Refresh:     ref,
		WarmOptions: opts,
	})
}

// ─── Candidate selection ─────────────────────────────────────────────

func TestWarmCandidatesSkipsCompleteByDefault(t *testing.T) {
	cat := &fakeWarmCatalog{
		albums:   []int64{1, 2, 3},
		meta:     map[int64]bool{1: true, 2: true},
		provider: map[int64]int{1: 10, 2: 10},
	}
	local := map[int64][]domain.Track{
		1: make([]domain.Track, 10), // complete (10 >= 10)
		2: make([]domain.Track, 3),  // incomplete (3 < 10)
		3: make([]domain.Track, 5),  // new (no metadata, no provider rows)
	}

	got, err := refresh.WarmCandidates(context.Background(), cat, &organizeRunnerStore{tracks: local}, refresh.DefaultWarmOptions())
	if err != nil {
		t.Fatalf("WarmCandidates: %v", err)
	}
	want := []int64{3, 2} // new first, then incomplete; complete (1) excluded
	if !equalIDs(got, want) {
		t.Fatalf("WarmCandidates = %v, want %v", got, want)
	}
}

func TestWarmCandidatesIncludeComplete(t *testing.T) {
	cat := &fakeWarmCatalog{
		albums:   []int64{1, 2, 3},
		meta:     map[int64]bool{1: true, 2: true},
		provider: map[int64]int{1: 10, 2: 10},
	}
	local := map[int64][]domain.Track{
		1: make([]domain.Track, 10),
		2: make([]domain.Track, 3),
		3: make([]domain.Track, 5),
	}

	opts := refresh.DefaultWarmOptions()
	opts.IncludeComplete = true
	got, err := refresh.WarmCandidates(context.Background(), cat, &organizeRunnerStore{tracks: local}, opts)
	if err != nil {
		t.Fatalf("WarmCandidates: %v", err)
	}
	want := []int64{3, 2, 1}
	if !equalIDs(got, want) {
		t.Fatalf("WarmCandidates(include complete) = %v, want %v", got, want)
	}
}

func TestWarmCandidatesPriorityOrder(t *testing.T) {
	cat := &fakeWarmCatalog{
		albums:   []int64{10, 11, 12, 13},
		meta:     map[int64]bool{11: true, 12: true, 13: true},
		provider: map[int64]int{12: 5, 13: 5},
	}
	local := map[int64][]domain.Track{
		10: make([]domain.Track, 2), // new
		11: make([]domain.Track, 0), // never-materialized
		12: make([]domain.Track, 2), // incomplete (2 < 5)
		13: make([]domain.Track, 5), // complete
	}

	got, err := refresh.WarmCandidates(context.Background(), cat, &organizeRunnerStore{tracks: local}, refresh.DefaultWarmOptions())
	if err != nil {
		t.Fatalf("WarmCandidates: %v", err)
	}
	want := []int64{10, 11, 12}
	if !equalIDs(got, want) {
		t.Fatalf("WarmCandidates priority = %v, want %v (new > never-materialized > incomplete)", got, want)
	}
}

func TestWarmCandidatesBoundedByLimit(t *testing.T) {
	cat := &fakeWarmCatalog{albums: []int64{1, 2, 3, 4, 5}}
	opts := refresh.DefaultWarmOptions()
	opts.Limit = 2

	got, err := refresh.WarmCandidates(context.Background(), cat, &organizeRunnerStore{}, opts)
	if err != nil {
		t.Fatalf("WarmCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("WarmCandidates returned %d ids, want 2 (limit)", len(got))
	}
}

// ─── Runner behavior ─────────────────────────────────────────────────

func TestWarmLibrarySkipsCompleteAlbums(t *testing.T) {
	cat := &fakeWarmCatalog{
		albums:   []int64{1, 2},
		meta:     map[int64]bool{1: true, 2: true},
		provider: map[int64]int{1: 10, 2: 10},
	}
	local := map[int64][]domain.Track{
		1: make([]domain.Track, 10),
		2: make([]domain.Track, 10),
	}
	ref := &fakeWarmRefresher{cat: cat}
	r := newWarmRunners(cat, local, ref, nil)

	var last Report
	if err := r.WarmLibrary()(context.Background(), func(rep Report) { last = rep }); err != nil {
		t.Fatalf("WarmLibrary: %v", err)
	}
	if len(ref.calls) != 0 {
		t.Fatalf("warmed %v, want none (all albums complete)", ref.calls)
	}
	if last.Message != "library already warm" {
		t.Fatalf("message = %q, want %q", last.Message, "library already warm")
	}
}

func TestWarmLibraryBoundedBatch(t *testing.T) {
	cat := &fakeWarmCatalog{albums: []int64{1, 2, 3, 4, 5}}
	local := map[int64][]domain.Track{1: {}} // one local track per album
	ref := &fakeWarmRefresher{cat: cat}
	r := newWarmRunners(cat, local, ref, func() refresh.WarmOptions {
		return refresh.WarmOptions{Limit: 2}
	})

	var reports []Report
	if err := r.WarmLibrary()(context.Background(), func(rep Report) { reports = append(reports, rep) }); err != nil {
		t.Fatalf("WarmLibrary: %v", err)
	}
	if len(ref.calls) != 2 {
		t.Fatalf("warmed %v, want exactly 2 (bounded batch)", ref.calls)
	}
	if len(reports) == 0 {
		t.Fatal("expected progress reports")
	}
	// The batch keeps only the first Limit candidates.
	if got := reports[0].Total; got != 2 {
		t.Fatalf("report Total = %d, want 2", got)
	}
	assertHasETA(t, reports)
}

func TestWarmLibraryTransientErrorsAreSkippedNotFailed(t *testing.T) {
	cat := &fakeWarmCatalog{albums: []int64{1}}
	local := map[int64][]domain.Track{1: {}}
	ref := &fakeWarmRefresher{cat: cat, errs: map[int64]error{1: metadata.ErrProviderBusy}}
	r := newWarmRunners(cat, local, ref, nil)

	var last Report
	if err := r.WarmLibrary()(context.Background(), func(rep Report) { last = rep }); err != nil {
		t.Fatalf("WarmLibrary: %v", err)
	}
	if !strings.Contains(last.Message, "0 failed") || !strings.Contains(last.Message, "1 skipped") {
		t.Fatalf("message = %q, want 0 failed / 1 skipped", last.Message)
	}
}

func TestWarmLibraryHardErrorsAreFailed(t *testing.T) {
	cat := &fakeWarmCatalog{albums: []int64{1}}
	local := map[int64][]domain.Track{1: {}}
	ref := &fakeWarmRefresher{cat: cat, errs: map[int64]error{1: errors.New("boom")}}
	r := newWarmRunners(cat, local, ref, nil)

	var last Report
	if err := r.WarmLibrary()(context.Background(), func(rep Report) { last = rep }); err != nil {
		t.Fatalf("WarmLibrary: %v", err)
	}
	if !strings.Contains(last.Message, "1 failed") || !strings.Contains(last.Message, "0 skipped") {
		t.Fatalf("message = %q, want 1 failed / 0 skipped", last.Message)
	}
}

func TestWarmLibraryResumable(t *testing.T) {
	cat := &fakeWarmCatalog{albums: []int64{1, 2, 3, 4}}
	local := map[int64][]domain.Track{1: {}, 2: {}, 3: {}, 4: {}}
	ref := &fakeWarmRefresher{cat: cat}
	r := newWarmRunners(cat, local, ref, func() refresh.WarmOptions {
		return refresh.WarmOptions{Limit: 2}
	})
	ctx := context.Background()

	if err := r.WarmLibrary()(ctx, func(Report) {}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := append([]int64(nil), ref.calls...)
	if !equalIDs(first, []int64{1, 2}) {
		t.Fatalf("first run warmed %v, want [1 2]", first)
	}

	if err := r.WarmLibrary()(ctx, func(Report) {}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	second := ref.calls[len(first):]
	if !equalIDs(second, []int64{3, 4}) {
		t.Fatalf("second run warmed %v, want [3 4] (resumes with remaining)", second)
	}
}

func TestWarmLibraryCancellationBetweenAlbums(t *testing.T) {
	cat := &fakeWarmCatalog{albums: []int64{1, 2, 3}}
	local := map[int64][]domain.Track{1: {}, 2: {}, 3: {}}
	ref := &fakeWarmRefresher{cat: cat}
	r := newWarmRunners(cat, local, ref, func() refresh.WarmOptions {
		return refresh.WarmOptions{Limit: 50}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.WarmLibrary()(ctx, func(Report) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WarmLibrary(cancelled) = %v, want context.Canceled", err)
	}
	if len(ref.calls) != 0 {
		t.Fatalf("warmed %v after cancellation, want none", ref.calls)
	}
}

func TestWarmLibraryNilDeps(t *testing.T) {
	local := &organizeRunnerStore{}
	cat := &fakeWarmCatalog{}
	ref := &fakeWarmRefresher{cat: cat}

	cases := []struct {
		name string
		deps RunnerDeps
	}{
		{"nil refresh", RunnerDeps{Log: testLogger(), Store: local, Catalog: cat}},
		{"nil catalog", RunnerDeps{Log: testLogger(), Store: local, Refresh: ref}},
		{"nil store", RunnerDeps{Log: testLogger(), Catalog: cat, Refresh: ref}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewRunners(c.deps)
			if err := r.WarmLibrary()(context.Background(), func(Report) {}); err == nil {
				t.Fatal("expected a clear error for missing dependency")
			}
		})
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func equalIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func assertHasETA(t *testing.T, reports []Report) {
	t.Helper()
	for _, rep := range reports {
		if rep.Total > 0 && rep.Done > 0 && rep.Done < rep.Total {
			if !strings.Contains(rep.Message, "eta") {
				t.Fatalf("progress message %q missing ETA", rep.Message)
			}
			return
		}
	}
	t.Fatal("no in-progress report carried an ETA")
}
