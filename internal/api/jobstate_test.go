package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/sse"
)

func jobStateTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestJobStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), jobStateFileName)
	s := &Server{jobStatePath: path, log: jobStateTestLogger(), runners: jobs.NewRunners(jobs.RunnerDeps{Log: jobStateTestLogger()})}
	s.saveJobState(&jobs.Job{Type: "enrich", State: jobs.StateRunning, Done: 2, Total: 100})
	got := s.loadJobState()
	if got == nil || got.Type != "enrich" || got.State != jobs.StateRunning || got.Done != 2 || got.Total != 100 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestRestoreInterruptedJobMarksRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), jobStateFileName)
	s := &Server{jobStatePath: path, log: jobStateTestLogger(), runners: jobs.NewRunners(jobs.RunnerDeps{Log: jobStateTestLogger()})}
	s.saveJobState(&jobs.Job{Type: "scan", State: jobs.StateRunning})

	s.restoreInterruptedJob()
	if s.runners.BootJob() == nil {
		t.Fatal("bootJob not set")
	}
	if s.runners.BootJob().State != jobs.StateInterrupted {
		t.Fatalf("state = %q, want interrupted", s.runners.BootJob().State)
	}
	if s.runners.BootJob().FinishedAt == nil {
		t.Error("interrupted job missing FinishedAt")
	}
	// The persisted file must reflect the interrupted state too.
	persisted := s.loadJobState()
	if persisted == nil || persisted.State != jobs.StateInterrupted {
		t.Fatalf("persisted state = %+v, want interrupted", persisted)
	}
}

func TestRestoreInterruptedJobKeepsTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), jobStateFileName)
	s := &Server{jobStatePath: path, log: jobStateTestLogger(), runners: jobs.NewRunners(jobs.RunnerDeps{Log: jobStateTestLogger()})}
	s.saveJobState(&jobs.Job{Type: "organize", State: jobs.StateCompleted, Done: 5, Total: 5})

	s.restoreInterruptedJob()
	if s.runners.BootJob() == nil || s.runners.BootJob().State != jobs.StateCompleted || s.runners.BootJob().Done != 5 {
		t.Fatalf("terminal state must be kept intact, got %+v", s.runners.BootJob())
	}
}

func TestRestoreInterruptedJobAbsentFile(t *testing.T) {
	s := &Server{jobStatePath: filepath.Join(t.TempDir(), jobStateFileName), log: jobStateTestLogger(), runners: jobs.NewRunners(jobs.RunnerDeps{Log: jobStateTestLogger()})}
	s.restoreInterruptedJob()
	if s.runners.BootJob() != nil {
		t.Fatalf("bootJob should be nil with no persisted state, got %+v", s.runners.BootJob())
	}
}

func TestHandleGetJobFallsBackToBootJob(t *testing.T) {
	logger := jobStateTestLogger()
	s := &Server{
		jobs:    jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
		log:     logger,
		runners: jobs.NewRunners(jobs.RunnerDeps{Log: logger}),
	}
	s.runners.SetBootJob(&jobs.Job{Type: "enrich", State: jobs.StateInterrupted, Done: 3, Total: 100})

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	rec := httptest.NewRecorder()
	s.handleGetJob(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"enrich"`) || !strings.Contains(body, `"state":"interrupted"`) {
		t.Fatalf("expected boot job in response, got %s", body)
	}
}

func TestStartJobPersistsLifecycle(t *testing.T) {
	logger := jobStateTestLogger()
	s := &Server{
		jobs:         jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
		log:          logger,
		jobStatePath: filepath.Join(t.TempDir(), jobStateFileName),
	}

	release := make(chan struct{})
	rec := httptest.NewRecorder()
	s.startJob(rec, "testjob", func(ctx context.Context, report func(jobs.Report)) error {
		<-release
		report(jobs.Report{Done: 7, Total: 10, Message: "halfway"})
		return nil
	})

	// While running, the persisted snapshot must say "running".
	if !waitPersistedState(t, s, jobs.StateRunning) {
		t.Fatal("running state not persisted after start")
	}

	close(release)
	waitJobState(t, s, jobs.StateCompleted)

	persisted := s.loadJobState()
	if persisted == nil || persisted.State != jobs.StateCompleted {
		t.Fatalf("persisted state = %+v, want completed", persisted)
	}
	if persisted.Done != 7 || persisted.Total != 10 || persisted.Message != "halfway" {
		t.Fatalf("persisted progress mismatch: %+v", persisted)
	}
	if persisted.StartedAt == nil {
		t.Error("terminal snapshot missing StartedAt (should carry from running snapshot)")
	}
	if persisted.FinishedAt == nil {
		t.Error("terminal snapshot missing FinishedAt")
	}
}

func TestStartJobPersistsCancelledState(t *testing.T) {
	logger := jobStateTestLogger()
	s := &Server{
		jobs:         jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
		log:          logger,
		jobStatePath: filepath.Join(t.TempDir(), jobStateFileName),
	}

	rec := httptest.NewRecorder()
	s.startJob(rec, "testjob", func(ctx context.Context, report func(jobs.Report)) error {
		<-ctx.Done()
		return ctx.Err()
	})
	waitJobState(t, s, jobs.StateRunning)
	s.jobs.Cancel()
	waitJobState(t, s, jobs.StateCancelled)

	persisted := s.loadJobState()
	if persisted == nil || persisted.State != jobs.StateCancelled {
		t.Fatalf("persisted state = %+v, want cancelled", persisted)
	}
	if persisted.Error != "" {
		t.Fatalf("cancelled job should not carry an error string, got %q", persisted.Error)
	}
}

func waitPersistedState(t *testing.T, s *Server, want jobs.State) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		j := s.loadJobState()
		if j != nil && j.State == want {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOrganizeDivergencePossible(t *testing.T) {
	cases := []struct {
		name string
		job  *jobs.Job
		want bool
	}{
		{"no boot snapshot", nil, false},
		{"killed organize (restarted)", &jobs.Job{Type: "organize", State: jobs.StateInterrupted}, true},
		{"completed organize", &jobs.Job{Type: "organize", State: jobs.StateCompleted}, false},
		{"cancelled organize", &jobs.Job{Type: "organize", State: jobs.StateCancelled}, false},
		{"failed organize", &jobs.Job{Type: "organize", State: jobs.StateFailed}, false},
		// "running" never appears in bootJob — restoreInterruptedJob rewrites it
		// to interrupted at boot.
		{"organize running", &jobs.Job{Type: "organize", State: jobs.StateRunning}, false},
		{"killed enrich", &jobs.Job{Type: "enrich", State: jobs.StateInterrupted}, false},
		{"killed scan", &jobs.Job{Type: "scan", State: jobs.StateInterrupted}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{log: jobStateTestLogger(), runners: jobs.NewRunners(jobs.RunnerDeps{Log: jobStateTestLogger()})}
			s.runners.SetBootJob(c.job)
			if got := s.runners.OrganizeDivergencePossible(); got != c.want {
				t.Fatalf("organizeDivergencePossible() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestOrganizeDivergencePossibleSurvivesJobStart locks the ordering that the
// scan-time reconcile depends on: after boot restores a killed organize into
// bootJob, starting a scan must not clear the trigger. (job.json is
// overwritten by the scan's own running write before scanRunner runs, so the
// gate must read the in-memory boot snapshot, not the file.)
func TestOrganizeDivergencePossibleSurvivesJobStart(t *testing.T) {
	logger := jobStateTestLogger()
	s := &Server{
		jobs:         jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
		log:          logger,
		jobStatePath: filepath.Join(t.TempDir(), jobStateFileName),
		runners:      jobs.NewRunners(jobs.RunnerDeps{Log: logger}),
	}

	// Simulate boot with a killed organize on disk.
	s.saveJobState(&jobs.Job{Type: "organize", State: jobs.StateRunning})
	s.restoreInterruptedJob()
	if !s.runners.OrganizeDivergencePossible() {
		t.Fatal("organizeDivergencePossible() = false right after boot restore")
	}

	// Starting a scan must not clear the trigger.
	rec := httptest.NewRecorder()
	s.startJob(rec, "scan", func(ctx context.Context, report func(jobs.Report)) error {
		return nil
	})
	waitJobState(t, s, jobs.StateCompleted)
	if !s.runners.OrganizeDivergencePossible() {
		t.Fatal("organizeDivergencePossible() = false after a scan ran (trigger lost)")
	}
}

// TestStartJobPersistsTerminalForFastRunner guards the running-vs-terminal
// write ordering: a runner that returns immediately must leave the persisted
// state terminal, not a stale "running" (the running write must land first).
func TestStartJobPersistsTerminalForFastRunner(t *testing.T) {
	logger := jobStateTestLogger()
	s := &Server{
		jobs:         jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
		log:          logger,
		jobStatePath: filepath.Join(t.TempDir(), jobStateFileName),
	}

	rec := httptest.NewRecorder()
	s.startJob(rec, "testjob", func(ctx context.Context, report func(jobs.Report)) error {
		report(jobs.Report{Done: 1, Total: 1, Message: "done fast"})
		return nil
	})
	waitJobState(t, s, jobs.StateCompleted)

	persisted := s.loadJobState()
	if persisted == nil || persisted.State != jobs.StateCompleted {
		t.Fatalf("persisted state = %+v, want completed (running write clobbered terminal)", persisted)
	}
	if persisted.Message != "done fast" {
		t.Fatalf("persisted message = %q, want %q", persisted.Message, "done fast")
	}
}

func TestOrganizeDivergenceDisarmedAfterRepair(t *testing.T) {
	s := &Server{log: jobStateTestLogger(), runners: jobs.NewRunners(jobs.RunnerDeps{Log: jobStateTestLogger()})}
	s.runners.SetBootJob(&jobs.Job{Type: "organize", State: jobs.StateInterrupted})
	if !s.runners.OrganizeDivergencePossible() {
		t.Fatal("trigger should be armed after a killed-organize boot")
	}
	s.runners.MarkDivergenceRepairDone()
	if s.runners.OrganizeDivergencePossible() {
		t.Fatal("trigger should be disarmed after one completed repair pass")
	}
}

// TestStartJobMultiGoroutineReports exercises the terminal-snapshot read under
// concurrent report() calls (like enrichRunner's worker pool). Without the
// fix — reading the manager snapshot instead of capturing in the closure —
// this races under -race.
func TestStartJobMultiGoroutineReports(t *testing.T) {
	logger := jobStateTestLogger()
	s := &Server{
		jobs:         jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
		log:          logger,
		jobStatePath: filepath.Join(t.TempDir(), jobStateFileName),
	}

	rec := httptest.NewRecorder()
	s.startJob(rec, "testjob", func(ctx context.Context, report func(jobs.Report)) error {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				report(jobs.Report{Done: i, Total: 100, Message: "worker"})
			}(i)
		}
		wg.Wait()
		report(jobs.Report{Done: 100, Total: 100, Message: "final"})
		return nil
	})
	waitJobState(t, s, jobs.StateCompleted)

	persisted := s.loadJobState()
	if persisted == nil || persisted.State != jobs.StateCompleted || persisted.Done != 100 || persisted.Message != "final" {
		t.Fatalf("persisted terminal = %+v", persisted)
	}
}
