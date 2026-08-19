// Package jobs provides a background job runner with SSE progress updates.
// Used for long-running library tasks (scan, enrichment) that should not
// block HTTP requests.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/sse"
)

// State is the lifecycle state of a background job.
type State string

const (
	StateIdle      State = "idle"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// ErrBusy is returned when trying to start a job while one is already running.
var ErrBusy = errors.New("a job is already running")

// minProgressInterval paces job_progress SSE broadcasts so per-file reports on
// large jobs don't flood the hub's per-client channels.
const minProgressInterval = 150 * time.Millisecond

// Job is a public snapshot of a background job.
type Job struct {
	Type       string     `json:"type"`
	State      State      `json:"state"`
	Progress   float64    `json:"progress"` // 0-100
	Message    string     `json:"message,omitempty"`
	Done       int        `json:"done"`
	Total      int        `json:"total"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// Report carries a progress update from a running job.
type Report struct {
	Done    int
	Total   int
	Message string
}

// Runner is the body of a background job. It should call report periodically
// to publish progress. Returning context.Canceled marks the job cancelled.
type Runner func(ctx context.Context, report func(Report)) error

// Manager runs one background job at a time and broadcasts progress over SSE.
// Job contexts derive from baseCtx so server shutdown cancels running jobs.
type Manager struct {
	mu           sync.Mutex
	baseCtx      context.Context
	current      *Job
	cancel       context.CancelFunc
	shuttingDown bool
	wg           sync.WaitGroup
	hub          *sse.SSEHub
	log          *slog.Logger
}

// NewManager creates a job manager bound to the given SSE hub. Job contexts
// derive from baseCtx (typically the server lifecycle context) so cancelling
// it stops any running job.
func NewManager(hub *sse.SSEHub, baseCtx context.Context, logger *slog.Logger) *Manager {
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{hub: hub, baseCtx: baseCtx, log: logger}
}

// Start begins running runner as a job of the given type. If a job is already
// running it returns ErrBusy. The returned Job is a snapshot.
func (m *Manager) Start(jobType string, runner Runner) (*Job, error) {
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	if m.current != nil && m.current.State == StateRunning {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	now := time.Now().UTC()
	job := &Job{Type: jobType, State: StateRunning, StartedAt: &now}
	m.current = job
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.cancel = cancel
	// Register the goroutine while holding the lock so Shutdown's Wait can
	// never run after this point without the counter already incremented —
	// an Add after Wait has started is a data race.
	m.wg.Add(1)
	m.mu.Unlock()

	m.broadcast(job, "job_started")
	m.log.Info("job started", "type", jobType, "component", "jobs")

	lastProgress := time.Now()
	go func() {
		defer m.wg.Done()
		err := runner(ctx, func(r Report) {
			m.mu.Lock()
			job.Done = r.Done
			job.Total = r.Total
			job.Message = r.Message
			if r.Total > 0 {
				job.Progress = float64(r.Done) / float64(r.Total) * 100
				if job.Progress > 100 {
					job.Progress = 100
				}
			}
			snap := *job
			// Throttle SSE broadcasts — per-file reports on a large scan would
			// otherwise flood the hub's small per-client channels and crowd out
			// download events (and the terminal job event). The job state is
			// always kept current for /api/jobs pollers; only the push is paced.
			now := time.Now()
			emit := now.Sub(lastProgress) >= minProgressInterval
			if emit {
				lastProgress = now
			}
			m.mu.Unlock()
			if emit {
				m.broadcast(&snap, "job_progress")
			}
		})
		m.finish(job, err)
	}()

	return m.Current(), nil
}

func (m *Manager) finish(job *Job, err error) {
	m.mu.Lock()
	now := time.Now().UTC()
	job.FinishedAt = &now
	switch {
	case err == nil:
		job.State = StateCompleted
		job.Progress = 100
		job.Done = job.Total
	case errors.Is(err, context.Canceled):
		job.State = StateCancelled
	default:
		job.State = StateFailed
		job.Error = err.Error()
	}
	snap := *job
	m.cancel = nil
	m.mu.Unlock()

	var eventType string
	switch snap.State {
	case StateCompleted:
		eventType = "job_completed"
	case StateFailed:
		eventType = "job_failed"
	case StateCancelled:
		eventType = "job_cancelled"
	}
	m.broadcast(&snap, eventType)
	m.log.Info("job finished", "type", snap.Type, "state", snap.State, "error", snap.Error, "component", "jobs")
}

// Cancel requests cancellation of the running job. The runner observes it via
// the context and stops.
func (m *Manager) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

// Shutdown cancels the running job and blocks until its goroutine returns.
// Safe to call multiple times and when no job is running. The shuttingDown
// flag is set under the mutex before Wait so a concurrent Start (e.g. from an
// in-flight HTTP request during server shutdown) can never Add to the
// WaitGroup while Wait is waiting. The wait is bounded so a runner that
// ignores cancellation can't hang shutdown forever.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.shuttingDown = true
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		m.log.Warn("job shutdown timed out, leaving runner to exit", "component", "jobs")
	}
}

// Current returns a snapshot of the current (or last) job, or nil when the
// manager has never run a job.
func (m *Manager) Current() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return nil
	}
	snap := *m.current
	return &snap
}

// broadcast marshals a job snapshot and publishes it to all SSE clients.
func (m *Manager) broadcast(job *Job, eventType string) {
	data, err := json.Marshal(job)
	if err != nil {
		m.log.Error("marshal job failed", "type", job.Type, "error", err, "component", "jobs")
		return
	}
	m.hub.Broadcast(sse.SSEEvent{Type: eventType, Data: data, Timestamp: time.Now()})
}
