package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/sse"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitState polls the manager until the current job reaches a non-running state.
func waitState(t *testing.T, m *Manager) *Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cur := m.Current(); cur != nil && cur.State != StateRunning {
			return cur
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not leave running state in time")
	return nil
}

func TestManagerCompletes(t *testing.T) {
	m := NewManager(sse.NewSSEHub(testLogger()), context.Background(), testLogger())
	started := make(chan struct{})
	job, err := m.Start("scan", func(ctx context.Context, report func(Report)) error {
		report(Report{Done: 2, Total: 4, Message: "two"})
		close(started)
		report(Report{Done: 4, Total: 4, Message: "four"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	cur := waitState(t, m)
	if cur.State != StateCompleted {
		t.Fatalf("state = %s, want completed", cur.State)
	}
	if cur.Progress != 100 {
		t.Errorf("progress = %v, want 100", cur.Progress)
	}
	if cur.Done != 4 || cur.Total != 4 {
		t.Errorf("done/total = %d/%d, want 4/4", cur.Done, cur.Total)
	}
	if cur.Type != "scan" {
		t.Errorf("type = %q, want scan", cur.Type)
	}
	_ = job
}

func TestManagerBusy(t *testing.T) {
	m := NewManager(sse.NewSSEHub(testLogger()), context.Background(), testLogger())
	block := make(chan struct{})
	defer close(block)
	if _, err := m.Start("scan", func(ctx context.Context, report func(Report)) error {
		<-block
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Start("enrich", func(ctx context.Context, report func(Report)) error {
		return nil
	}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second start err = %v, want ErrBusy", err)
	}
}

func TestManagerCancel(t *testing.T) {
	m := NewManager(sse.NewSSEHub(testLogger()), context.Background(), testLogger())
	if _, err := m.Start("scan", func(ctx context.Context, report func(Report)) error {
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}

	m.Cancel()
	cur := waitState(t, m)
	if cur.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled", cur.State)
	}
}

func TestManagerShutdownCancelsAndBlocks(t *testing.T) {
	m := NewManager(sse.NewSSEHub(testLogger()), context.Background(), testLogger())
	if _, err := m.Start("scan", func(ctx context.Context, report func(Report)) error {
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}

	m.Shutdown()
	cur := m.Current()
	if cur == nil || cur.State != StateCancelled {
		t.Fatalf("state = %v, want cancelled after shutdown", cur)
	}

	// After shutdown no new job may start.
	if _, err := m.Start("enrich", func(ctx context.Context, report func(Report)) error {
		return nil
	}); !errors.Is(err, ErrBusy) {
		t.Fatalf("start after shutdown err = %v, want ErrBusy", err)
	}
}

func TestManagerShutdownIdle(t *testing.T) {
	m := NewManager(sse.NewSSEHub(testLogger()), context.Background(), testLogger())
	m.Shutdown() // must not hang or panic when nothing ever ran
}
