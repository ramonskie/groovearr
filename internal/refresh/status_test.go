package refresh

import (
	"context"
	"testing"
	"time"
)

// TestService_Status_Defaults verifies the snapshot reports the effective
// (default-filled) options and a not-yet-running engine.
func TestService_Status_Defaults(t *testing.T) {
	svc := newTestService(newFakeStore(), &fakeLibrary{}, Options{})

	st := svc.Status()

	if st.Running {
		t.Errorf("Running = true, want false before Start")
	}
	if st.IntervalSeconds != int(DefaultOptions().Interval/time.Second) {
		t.Errorf("IntervalSeconds = %d, want %d", st.IntervalSeconds, int(DefaultOptions().Interval/time.Second))
	}
	if st.BatchSize != DefaultOptions().BatchSize {
		t.Errorf("BatchSize = %d, want %d", st.BatchSize, DefaultOptions().BatchSize)
	}
	if st.LastDrainAt != nil {
		t.Errorf("LastDrainAt = %v, want nil before any drain", st.LastDrainAt)
	}
}

// TestService_Status_LiveOptions verifies the snapshot is read live from the
// options closure (AGENTS §9 — never cached).
func TestService_Status_LiveOptions(t *testing.T) {
	svc := newTestService(newFakeStore(), &fakeLibrary{}, Options{})
	svc.SetOptionsFunc(func() Options {
		return Options{Interval: 30 * time.Second, BatchSize: 4}
	})

	st := svc.Status()

	if st.IntervalSeconds != 30 {
		t.Errorf("IntervalSeconds = %d, want 30", st.IntervalSeconds)
	}
	if st.BatchSize != 4 {
		t.Errorf("BatchSize = %d, want 4", st.BatchSize)
	}
}

// TestService_Status_RecordsDrain verifies the last-drain fields are populated
// from the engine's own clock.
func TestService_Status_RecordsDrain(t *testing.T) {
	svc := newTestService(newFakeStore(), &fakeLibrary{}, Options{})

	svc.recordDrain(3)
	st := svc.Status()

	if st.LastDrainAt == nil {
		t.Fatal("LastDrainAt = nil, want the recorded time")
	}
	if !st.LastDrainAt.Equal(fixedNow) {
		t.Errorf("LastDrainAt = %v, want %v", st.LastDrainAt, fixedNow)
	}
	if st.LastDrainCount != 3 {
		t.Errorf("LastDrainCount = %d, want 3", st.LastDrainCount)
	}
}

// TestService_Status_RunningTracksLifecycle verifies Running follows Start/Stop.
func TestService_Status_RunningTracksLifecycle(t *testing.T) {
	svc := newTestService(newFakeStore(), &fakeLibrary{}, Options{})

	svc.Start(context.Background())
	if !svc.Status().Running {
		t.Error("Running = false after Start, want true")
	}

	svc.Stop()
	if svc.Status().Running {
		t.Error("Running = true after Stop, want false")
	}
}

// TestService_Status_NilService verifies a nil receiver is safe and returns the
// zero snapshot.
func TestService_Status_NilService(t *testing.T) {
	var svc *Service
	if st := svc.Status(); st.Running || st.BatchSize != 0 {
		t.Errorf("nil service Status = %+v, want zero value", st)
	}
}
