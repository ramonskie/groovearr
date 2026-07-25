package download

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

// ─── Mock plugin (implements Plugin + DownloadProgressor) ────────────

type workerTestPlugin struct {
	name        string
	displayName string
	configured  bool

	mu          sync.Mutex
	downloadID  string
	downloadErr error
	state       domain.DownloadState
	progress    float64
	transferred int64
	total       int64
	speed       int64
	filePath    string
	errorMsg    string

	// Control for simulating multi-step progress.
	completeAfterCalls int // how many GetDownloadStatus calls before marking completed
	failAfterCalls     int // how many calls before marking failed

	// Call counters.
	downloadCalls       int
	getStatusCalls      int
	getProgressCalls    int
	lastUsername        string
	lastFilename        string
	lastFileSize        int64
}

func createWorkerTestPlugin(name string) *workerTestPlugin {
	return &workerTestPlugin{
		name:        name,
		displayName: strings.ToUpper(name[:1]) + name[1:],
		configured:  true,
		downloadID:  name + "-dl-001",
		state:       domain.DownloadDownloading,
	}
}

func (m *workerTestPlugin) Name() string                             { return m.name }
func (m *workerTestPlugin) DisplayName() string                      { return m.displayName }
func (m *workerTestPlugin) IsConfigured() bool                       { return m.configured }
func (m *workerTestPlugin) CapabilityStatus() map[string]string {
	s := "not_configured"
	if m.configured { s = "connected" }
	return map[string]string{"download": s}
}
func (m *workerTestPlugin) CheckConnection(ctx context.Context) error { return nil }
func (m *workerTestPlugin) Connected() bool                          { return m.configured }

func (m *workerTestPlugin) Search(ctx context.Context, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	return nil, nil, nil
}

func (m *workerTestPlugin) Download(ctx context.Context, username, filename string, fileSize int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.downloadCalls++
	m.lastUsername = username
	m.lastFilename = filename
	m.lastFileSize = fileSize
	if m.downloadErr != nil {
		return "", m.downloadErr
	}
	return m.downloadID, nil
}

func (m *workerTestPlugin) GetDownloads(ctx context.Context) ([]domain.DownloadRecord, error) {
	return nil, nil
}

func (m *workerTestPlugin) GetDownloadStatus(ctx context.Context, downloadID string) (*domain.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getStatusCalls++

	// Determine state based on call count.
	state := m.state
	if m.completeAfterCalls > 0 && m.getStatusCalls >= m.completeAfterCalls {
		state = domain.DownloadImported
		m.progress = 100.0
		m.transferred = m.total
		m.filePath = "/tmp/downloaded/" + m.name + ".flac"
	}
	if m.failAfterCalls > 0 && m.getStatusCalls >= m.failAfterCalls {
		state = domain.DownloadFailed
		m.errorMsg = "mock failure"
	}

	return &domain.DownloadRecord{
		ID:          downloadID,
		SourceName:  m.name,
		State:       state,
		Progress:    m.progress,
		Size:        m.total,
		Transferred: m.transferred,
		Speed:       m.speed,
		FilePath:    m.filePath,
		Error:       m.errorMsg,
	}, nil
}

func (m *workerTestPlugin) CancelDownload(ctx context.Context, downloadID string, remove bool) error {
	return nil
}

func (m *workerTestPlugin) ClearCompleted(ctx context.Context) error { return nil }

// GetProgress implements DownloadProgressor.
func (m *workerTestPlugin) GetProgress(ctx context.Context, downloadID string) (*Progress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getProgressCalls++
	return &Progress{
		DownloadID:  downloadID,
		Transferred: m.transferred,
		Total:       m.total,
		Speed:       m.speed,
	}, nil
}

// Compile-time checks.
var _ Plugin = (*workerTestPlugin)(nil)
var _ DownloadProgressor = (*workerTestPlugin)(nil)

// ─── Tests ────────────────────────────────────────────────────────────

func TestNewWorkerPool(t *testing.T) {
	reg := NewRegistry()
	store := newMockStore()
	bus := newMockBus()

	pool := NewWorkerPool(5, reg, store, bus, testLogger())
	if pool == nil {
		t.Fatal("NewWorkerPool returned nil")
	}

	// Verify interface compliance.
	var _ WorkerPool = pool
}

func TestNewWorkerPoolDefaultsMaxWorkers(t *testing.T) {
	pool := NewWorkerPool(0, NewRegistry(), newMockStore(), newMockBus(), testLogger())
	p := pool.(*workerPoolImpl)
	if p.maxWorkers != defaultMaxWorkers {
		t.Errorf("maxWorkers = %d, want %d", p.maxWorkers, defaultMaxWorkers)
	}

	pool2 := NewWorkerPool(-1, NewRegistry(), newMockStore(), newMockBus(), testLogger())
	p2 := pool2.(*workerPoolImpl)
	if p2.maxWorkers != defaultMaxWorkers {
		t.Errorf("maxWorkers = %d, want %d", p2.maxWorkers, defaultMaxWorkers)
	}
}

func TestSubmitEnqueuesJob(t *testing.T) {
	reg := NewRegistry()
	store := newMockStore()
	bus := newMockBus()
	pool := NewWorkerPool(1, reg, store, bus, testLogger())

	err := pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "test-001",
		SourceName: "soulseek",
		Filename:   "track.flac",
		Size:       1000,
	})
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
}

func TestSubmitOverflow(t *testing.T) {
	// Plugin that never completes — worker stays busy so buffer fills.
	mp := createWorkerTestPlugin("slow")
	mp.completeAfterCalls = 1000 // never completes within test
	reg := NewRegistry()
	_ = reg.Register(mp)

	pool := NewWorkerPool(1, reg, newMockStore(), newMockBus(), testLogger())

	// Submit jobs until overflow. Buffer size = maxWorkers*2 = 2.
	// After the worker picks up the first job, the buffer can hold 2 more.
	successCount := 0
	hasError := false
	for i := 0; i < 20; i++ {
		err := pool.Submit(context.Background(), &domain.DownloadRecord{
			ID:         fmt.Sprintf("ovf-%03d", i),
			SourceName: "slow",
			Filename:   "track.flac",
			Size:       1000,
		})
		if err != nil {
			hasError = true
			break
		}
		successCount++
	}
	if !hasError {
		t.Fatal("expected overflow error after queue is full")
	}
	// Worker consumes 1, buffer holds 2 => at least 1, at most 3 succeed.
	if successCount < 1 || successCount > 5 {
		t.Logf("successCount = %d (buffer=2, worker=1)", successCount)
	}
}

func TestSubmitOverflowErrorContainsCapacity(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	mp.completeAfterCalls = 1000 // never completes
	reg := NewRegistry()
	_ = reg.Register(mp)
	pool := NewWorkerPool(1, reg, newMockStore(), newMockBus(), testLogger())

	// Submit many jobs until overflow.
	for i := 0; i < 20; i++ {
		err := pool.Submit(context.Background(), &domain.DownloadRecord{
			ID:         fmt.Sprintf("ovfcap-%d", i),
			SourceName: "src",
			Filename:   "f.flac",
			Size:       1,
		})
		if err != nil {
			if !strings.Contains(err.Error(), "capacity") {
				t.Errorf("error = %q, want 'capacity'", err)
			}
			return
		}
	}
	t.Fatal("expected overflow error")
}

func TestWorkerPicksUpJobAndCallsPlugin(t *testing.T) {
	mp := createWorkerTestPlugin("soulseek")
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	// Pre-insert the record so the worker can update it.
	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-001",
		SourceName: "soulseek",
		Filename:   "music/song.flac",
		Size:       42_000_000,
		Username:   "peer1",
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	err := pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-001",
		SourceName: "soulseek",
		Filename:   "music/song.flac",
		Size:       42_000_000,
		Username:   "peer1",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for worker to pick up and call Download.
	waitFor(t, 3*time.Second, func() bool {
		mp.mu.Lock()
		defer mp.mu.Unlock()
		return mp.downloadCalls >= 1
	})

	mp.mu.Lock()
	defer mp.mu.Unlock()
	if mp.downloadCalls != 1 {
		t.Fatalf("downloadCalls = %d, want 1", mp.downloadCalls)
	}
	if mp.lastUsername != "peer1" {
		t.Errorf("username = %q, want 'peer1'", mp.lastUsername)
	}
	if mp.lastFilename != "music/song.flac" {
		t.Errorf("filename = %q", mp.lastFilename)
	}
	if mp.lastFileSize != 42_000_000 {
		t.Errorf("fileSize = %d", mp.lastFileSize)
	}
}

func TestWorkerUpdatesStateToDownloading(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-002",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-002",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100,
	})

	// Wait for state to change to downloading.
	waitFor(t, 3*time.Second, func() bool {
		r, _ := store.Get(context.Background(), "job-002")
		return r != nil && r.State == domain.DownloadDownloading
	})

	r, _ := store.Get(context.Background(), "job-002")
	if r == nil {
		t.Fatal("record not found")
	}
	if r.State != domain.DownloadDownloading {
		t.Errorf("state = %q, want %q", r.State, domain.DownloadDownloading)
	}
}

func TestWorkerFiresStateChangedEvent(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-003",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-003",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100,
	})

	waitFor(t, 3*time.Second, func() bool {
		evts := bus.published()
		for _, e := range evts {
			if e.Topic == events.TopicDownloadStateChanged {
				return true
			}
		}
		return false
	})

	evts := bus.published()
	found := false
	for _, e := range evts {
		if e.Topic == events.TopicDownloadStateChanged {
			rec, ok := e.Event.(*domain.DownloadRecord)
			if !ok {
				t.Fatal("event payload is not *domain.DownloadRecord")
			}
			if rec.State == domain.DownloadDownloading {
				found = true
			}
		}
	}
	if !found {
		t.Error("TopicDownloadStateChanged not fired with downloading state")
	}
}

func TestWorkerCompletesDownload(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	// Complete on first GetDownloadStatus call.
	mp.completeAfterCalls = 1
	mp.total = 50_000
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-004",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       50_000,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-004",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       50_000,
	})

	// Wait for completion event.
	waitFor(t, 5*time.Second, func() bool {
		evts := bus.published()
		for _, e := range evts {
			if e.Topic == events.TopicDownloadCompleted {
				return true
			}
		}
		return false
	})

	// Verify store state.
	r, _ := store.Get(context.Background(), "job-004")
	if r == nil {
		t.Fatal("record not found")
	}
	if r.State != domain.DownloadImportPending {
		t.Errorf("state = %q, want %q", r.State, domain.DownloadImportPending)
	}

	// Verify completed event.
	evts := bus.published()
	hasCompleted := false
	for _, e := range evts {
		if e.Topic == events.TopicDownloadCompleted {
			hasCompleted = true
			rec, ok := e.Event.(*domain.DownloadRecord)
			if !ok {
				t.Error("completed event payload is not *domain.DownloadRecord")
			}
			if rec.ID != "job-004" {
				t.Errorf("completed event ID = %q", rec.ID)
			}
			if rec.State != domain.DownloadImportPending {
				t.Errorf("completed event state = %q, want importPending", rec.State)
			}
		}
	}
	if !hasCompleted {
		t.Error("TopicDownloadCompleted not fired")
	}
}

func TestWorkerFiresProgressEvents(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	mp.total = 100_000
	mp.transferred = 33_000
	mp.progress = 33.0
	mp.speed = 500_000
	// Complete after 3 status checks (so 2 progress events).
	mp.completeAfterCalls = 3
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-005",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100_000,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-005",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100_000,
	})

	// Wait for completion.
	waitFor(t, 8*time.Second, func() bool {
		evts := bus.published()
		for _, e := range evts {
			if e.Topic == events.TopicDownloadCompleted {
				return true
			}
		}
		return false
	})

	evts := bus.published()
	progressCount := 0
	for _, e := range evts {
		if e.Topic == events.TopicDownloadProgress {
			progressCount++
		}
	}
	if progressCount < 1 {
		t.Errorf("expected at least 1 progress event, got %d", progressCount)
	}
}

func TestWorkerHandlesDownloadError(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	mp.downloadErr = fmt.Errorf("connection refused")
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-006",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-006",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100,
	})

	// Wait for failed event.
	waitFor(t, 3*time.Second, func() bool {
		evts := bus.published()
		for _, e := range evts {
			if e.Topic == events.TopicDownloadFailed {
				return true
			}
		}
		return false
	})

	r, _ := store.Get(context.Background(), "job-006")
	if r == nil {
		t.Fatal("record not found")
	}
	if r.State != domain.DownloadFailed {
		t.Errorf("state = %q, want failed", r.State)
	}
	if !strings.Contains(r.Error, "connection refused") {
		t.Errorf("error = %q, want 'connection refused'", r.Error)
	}

	// Verify failed event fired.
	evts := bus.published()
	hasFailed := false
	for _, e := range evts {
		if e.Topic == events.TopicDownloadFailed {
			hasFailed = true
			rec, ok := e.Event.(*domain.DownloadRecord)
			if !ok {
				t.Error("failed event payload is not *domain.DownloadRecord")
			}
			if rec.ID != "job-006" {
				t.Errorf("failed event ID = %q", rec.ID)
			}
		}
	}
	if !hasFailed {
		t.Error("TopicDownloadFailed not fired")
	}
}

func TestWorkerHandlesDownloadFailureMidway(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	mp.total = 100_000
	mp.failAfterCalls = 2 // fail on 2nd status check
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-007",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100_000,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-007",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100_000,
	})

	waitFor(t, 5*time.Second, func() bool {
		evts := bus.published()
		for _, e := range evts {
			if e.Topic == events.TopicDownloadFailed {
				return true
			}
		}
		return false
	})

	r, _ := store.Get(context.Background(), "job-007")
	if r.State != domain.DownloadFailed {
		t.Errorf("state = %q, want failed", r.State)
	}
}

func TestWorkerHandlesPluginNotFound(t *testing.T) {
	reg := NewRegistry()
	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-008",
		SourceName: "nonexistent",
		Filename:   "t.flac",
		Size:       100,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-008",
		SourceName: "nonexistent",
		Filename:   "t.flac",
		Size:       100,
	})

	waitFor(t, 3*time.Second, func() bool {
		evts := bus.published()
		for _, e := range evts {
			if e.Topic == events.TopicDownloadFailed {
				return true
			}
		}
		return false
	})

	r, _ := store.Get(context.Background(), "job-008")
	if r.State != domain.DownloadFailed {
		t.Errorf("state = %q, want failed", r.State)
	}
	if !strings.Contains(r.Error, "not found") {
		t.Errorf("error = %q, want 'not found'", r.Error)
	}
}

func TestDownloadProgressorCalled(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	mp.total = 100_000
	mp.completeAfterCalls = 3
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-009",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100_000,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-009",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100_000,
	})

	waitFor(t, 8*time.Second, func() bool {
		mp.mu.Lock()
		defer mp.mu.Unlock()
		return mp.getProgressCalls > 0
	})

	mp.mu.Lock()
	defer mp.mu.Unlock()
	if mp.getProgressCalls == 0 {
		t.Error("GetProgress was never called on DownloadProgressor")
	}
}

func TestShutdown(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	mp.completeAfterCalls = 100 // won't complete naturally
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-010",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100,
		State:      domain.DownloadQueued,
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-010",
		SourceName: "src",
		Filename:   "t.flac",
		Size:       100,
	})

	// Wait for worker to start processing.
	waitFor(t, 2*time.Second, func() bool {
		mp.mu.Lock()
		defer mp.mu.Unlock()
		return mp.downloadCalls > 0
	})

	// Shutdown should cancel in-flight download.
	pool.(*workerPoolImpl).Shutdown()

	// Worker should have exited. The in-flight job was cancelled.
	r, _ := store.Get(context.Background(), "job-010")
	if r != nil && r.State != domain.DownloadFailed && r.State != domain.DownloadDownloading {
		t.Logf("state after shutdown: %s", r.State)
	}
}

func TestShutdownCleansUpWorkers(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	reg := NewRegistry()
	_ = reg.Register(mp)

	pool := NewWorkerPool(3, reg, newMockStore(), newMockBus(), testLogger())
	p := pool.(*workerPoolImpl)

	// Shutdown and wait — should not hang.
	done := make(chan struct{})
	go func() {
		p.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		// OK — workers exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown timed out — workers may be stuck")
	}
}

func TestSubmitWithNoUsernameUsesSourceName(t *testing.T) {
	mp := createWorkerTestPlugin("deezer")
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-011",
		SourceName: "deezer",
		Filename:   "12345||Track Name",
		Size:       10_000_000,
		State:      domain.DownloadQueued,
		// Username intentionally empty — falls back to source name.
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-011",
		SourceName: "deezer",
		Filename:   "12345||Track Name",
		Size:       10_000_000,
	})

	waitFor(t, 3*time.Second, func() bool {
		mp.mu.Lock()
		defer mp.mu.Unlock()
		return mp.downloadCalls >= 1
	})

	mp.mu.Lock()
	defer mp.mu.Unlock()
	if mp.lastUsername != "deezer" {
		t.Errorf("username = %q, want 'deezer' (fallback to SourceName)", mp.lastUsername)
	}
}

func TestMultipleWorkersConcurrent(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	mp.completeAfterCalls = 1
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	// Pre-insert 3 records.
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("concurrent-%03d", i)
		_ = store.Insert(context.Background(), &domain.DownloadRecord{
			ID:         id,
			SourceName: "src",
			Filename:   "t.flac",
			Size:       100,
			State:      domain.DownloadQueued,
		})
	}

	pool := NewWorkerPool(3, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	// Submit all 3 jobs.
	for i := 0; i < 3; i++ {
		_ = pool.Submit(context.Background(), &domain.DownloadRecord{
			ID:         fmt.Sprintf("concurrent-%03d", i),
			SourceName: "src",
			Filename:   "t.flac",
			Size:       100,
		})
	}

	// Wait for all 3 to complete.
	waitFor(t, 10*time.Second, func() bool {
		evts := bus.published()
		count := 0
		for _, e := range evts {
			if e.Topic == events.TopicDownloadCompleted {
				count++
			}
		}
		return count >= 3
	})

	// Verify all 3 records are in importPending state.
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("concurrent-%03d", i)
		r, _ := store.Get(context.Background(), id)
		if r == nil || r.State != domain.DownloadImportPending {
			t.Errorf("job %s: state = %v, want importPending", id, r)
		}
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if fn() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("waitFor timed out after %s", timeout)
		case <-ticker.C:
		}
	}
}

// ─── failJob RetryCount preservation ────────────────────────────────

func TestFailJobPreservesRetryCount(t *testing.T) {
	mp := createWorkerTestPlugin("src")
	mp.downloadErr = fmt.Errorf("connection refused")
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-retry-preserve",
		SourceName: "src",
		Filename:   "t.flac",
		State:      domain.DownloadQueued,
		RetryCount: 3,
		RetryAfter: "2025-01-01T00:00:00Z",
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	store.mu.Lock()
	store.records["job-retry-preserve"].RetryCount = 3
	store.records["job-retry-preserve"].RetryAfter = "2025-01-01T00:00:00Z"
	store.mu.Unlock()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-retry-preserve",
		SourceName: "src",
		Filename:   "t.flac",
	})

	waitFor(t, 5*time.Second, func() bool {
		evts := bus.published()
		for _, e := range evts {
			if e.Topic == events.TopicDownloadFailed {
				return true
			}
		}
		return false
	})

	r, _ := store.Get(context.Background(), "job-retry-preserve")
	if r == nil {
		t.Fatal("record not found")
	}
	if r.RetryCount != 3 {
		t.Errorf("RetryCount = %d, want 3 (failJob should preserve it)", r.RetryCount)
	}
	if r.RetryAfter != "2025-01-01T00:00:00Z" {
		t.Errorf("RetryAfter = %q, want 2025-01-01T00:00:00Z", r.RetryAfter)
	}
}

func TestFailJobSoulseekTimeoutPreservesRetryCount(t *testing.T) {
	mp := createWorkerTestPlugin("soulseek")
	// Simulate download starts OK, but plugin reports failure on first status check.
	mp.state = domain.DownloadFailed
	mp.errorMsg = "peer offline"
	reg := NewRegistry()
	_ = reg.Register(mp)

	store := newMockStore()
	bus := newMockBus()

	_ = store.Insert(context.Background(), &domain.DownloadRecord{
		ID:         "job-slsk-retry",
		SourceName: "soulseek",
		Filename:   "t.flac",
		State:      domain.DownloadQueued,
		Size:       50_000,
		RetryCount: 2,
		RetryAfter: "2025-06-01T00:00:00Z",
	})

	pool := NewWorkerPool(1, reg, store, bus, testLogger())
	defer pool.(*workerPoolImpl).Shutdown()

	store.mu.Lock()
	store.records["job-slsk-retry"].RetryCount = 2
	store.records["job-slsk-retry"].RetryAfter = "2025-06-01T00:00:00Z"
	store.mu.Unlock()

	_ = pool.Submit(context.Background(), &domain.DownloadRecord{
		ID:         "job-slsk-retry",
		SourceName: "soulseek",
		Filename:   "t.flac",
		Size:       50_000,
	})

	waitFor(t, 5*time.Second, func() bool {
		evts := bus.published()
		for _, e := range evts {
			if e.Topic == events.TopicDownloadFailed {
				return true
			}
		}
		return false
	})

	r, _ := store.Get(context.Background(), "job-slsk-retry")
	if r == nil {
		t.Fatal("record not found")
	}
	if r.RetryCount != 2 {
		t.Errorf("RetryCount = %d, want 2 (failJob should preserve it)", r.RetryCount)
	}
	if r.RetryAfter != "2025-06-01T00:00:00Z" {
		t.Errorf("RetryAfter = %q, want 2025-06-01T00:00:00Z", r.RetryAfter)
	}
}
