package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRearmer records the limit it was called with and returns a canned count or
// error.
type fakeRearmer struct {
	calls int
	limit int
	n     int
	err   error
}

func (f *fakeRearmer) ReArmTerminalAlbums(_ context.Context, limit int) (int, error) {
	f.calls++
	f.limit = limit
	return f.n, f.err
}

var _ UnmatchedRearmer = (*fakeRearmer)(nil)

func TestRecheckUnmatchedReportsCount(t *testing.T) {
	re := &fakeRearmer{n: 1907}
	r := NewRunners(RunnerDeps{Rearm: re})

	var last Report
	if err := r.RecheckUnmatched()(context.Background(), func(rep Report) { last = rep }); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	if re.calls != 1 {
		t.Fatalf("re-arm calls = %d, want 1", re.calls)
	}
	if re.limit != recheckUnmatchedCap {
		t.Errorf("limit = %d, want %d", re.limit, recheckUnmatchedCap)
	}
	if !strings.Contains(last.Message, "1907") {
		t.Errorf("message = %q, want it to name the 1907 re-armed albums", last.Message)
	}
}

func TestRecheckUnmatchedZeroIsReported(t *testing.T) {
	r := NewRunners(RunnerDeps{Rearm: &fakeRearmer{n: 0}})

	var last Report
	if err := r.RecheckUnmatched()(context.Background(), func(rep Report) { last = rep }); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	if !strings.Contains(last.Message, "no unmatched or failed") {
		t.Errorf("message = %q, want the no-op wording", last.Message)
	}
}

func TestRecheckUnmatchedWithoutRearmer(t *testing.T) {
	r := NewRunners(RunnerDeps{})
	if err := r.RecheckUnmatched()(context.Background(), func(Report) {}); err == nil {
		t.Fatal("want an error when no rearmer is wired")
	}
}

func TestRecheckUnmatchedPropagatesError(t *testing.T) {
	r := NewRunners(RunnerDeps{Rearm: &fakeRearmer{err: errors.New("boom")}})
	err := r.RecheckUnmatched()(context.Background(), func(Report) {})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want it to wrap boom", err)
	}
}

// TestSetUnmatchedRearmerSwaps confirms the post-construction wiring replaces a
// nil dependency.
func TestSetUnmatchedRearmerSwaps(t *testing.T) {
	r := NewRunners(RunnerDeps{})
	re := &fakeRearmer{n: 3}
	r.SetUnmatchedRearmer(re)
	if err := r.RecheckUnmatched()(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("recheck after wiring: %v", err)
	}
	if re.calls != 1 {
		t.Errorf("re-arm calls = %d, want 1", re.calls)
	}
}
