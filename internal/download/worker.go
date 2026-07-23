package download

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

const defaultMaxWorkers = 3
const progressPollInterval = 1 * time.Second

// DownloadJob represents a download task dispatched to a worker.
type DownloadJob struct {
	DownloadID  string
	SourceName  string
	Username    string
	Filename    string
	DisplayName string
	FileSize    int64
	Metadata    DownloadMeta
}

// workerPoolImpl implements WorkerPool with bounded goroutines and graceful shutdown.
type workerPoolImpl struct {
	log            *slog.Logger
	maxWorkers     int
	jobQueue       chan *DownloadJob
	pluginRegistry *Registry
	store          DownloadStore
	bus            events.IEventAggregator
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup

	cancelMu  sync.Mutex
	cancels   map[string]context.CancelFunc // per-download cancellation

	pluginSemMu    sync.Mutex
	pluginSemaphores map[string]chan struct{} // per-plugin concurrency gates
}

// NewWorkerPool creates a worker pool that runs up to maxWorkers concurrent
// download goroutines. maxWorkers defaults to defaultMaxWorkers (3) when zero
// or negative. Each worker picks jobs from a buffered channel and drives the
// download through its full lifecycle: downloading → importPending (or failed).
func NewWorkerPool(maxWorkers int, registry *Registry, store DownloadStore, bus events.IEventAggregator, logger *slog.Logger) WorkerPool {
	if maxWorkers <= 0 {
		maxWorkers = defaultMaxWorkers
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &workerPoolImpl{
		log:              logger,
		maxWorkers:       maxWorkers,
		jobQueue:         make(chan *DownloadJob, maxWorkers*2),
		pluginRegistry:   registry,
		store:            store,
		bus:              bus,
		ctx:              ctx,
		cancel:           cancel,
		cancels:          make(map[string]context.CancelFunc),
		pluginSemaphores: make(map[string]chan struct{}),
	}
	for i := 0; i < maxWorkers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

// Submit enqueues a download record for processing. Returns an error if the
// pool's job queue is at capacity (non-blocking with overflow handling).
func (p *workerPoolImpl) Submit(ctx context.Context, record *domain.DownloadRecord) error {
	username := record.Username
	if username == "" {
		username = record.SourceName
	}

	job := &DownloadJob{
		DownloadID:  record.ID,
		SourceName:  record.SourceName,
		Username:    username,
		Filename:    record.Filename,
		DisplayName: record.DisplayName,
		FileSize:    record.Size,
		Metadata: DownloadMeta{
			Artist:      record.Artist,
			Album:       record.Album,
			Title:       record.Title,
			TrackNumber: record.TrackNumber,
			DiscNumber:  record.DiscNumber,
			Year:        record.Year,
			TrackID:     record.TrackID,
			CoverURL:    record.CoverURL,
			PlaylistID:  record.PlaylistID,
		},
	}

	select {
	case p.jobQueue <- job:
		return nil
	default:
		return fmt.Errorf("worker pool at capacity (%d workers, %d queued)",
			p.maxWorkers, cap(p.jobQueue))
	}
}

// Cancel stops an in-progress download by cancelling its per-job context.
func (p *workerPoolImpl) Cancel(downloadID string) {
	p.cancelMu.Lock()
	cancel, ok := p.cancels[downloadID]
	if ok {
		delete(p.cancels, downloadID)
	}
	p.cancelMu.Unlock()

	if ok {
		cancel()
	}
}

// Shutdown gracefully stops the pool: cancels in-flight downloads (via the
// pool-level context), drains remaining queued jobs by discarding them, and
// waits for all workers to exit.
func (p *workerPoolImpl) Shutdown() {
	p.log.Info("worker pool shutdown started", "component", "worker")
	p.cancel()
	close(p.jobQueue)
	p.wg.Wait()
}

// worker loops on the job queue until it is closed.
func (p *workerPoolImpl) worker() {
	p.log.Info("worker started", "component", "worker")
	defer p.wg.Done()
	for job := range p.jobQueue {
		p.processJob(job)
	}
	p.log.Info("worker stopped", "component", "worker")
}

// processJob runs the full download lifecycle for a single job.
func (p *workerPoolImpl) processJob(job *DownloadJob) {
	downloadID := job.DownloadID
	displayName := job.DisplayName
	if displayName == "" {
		if job.Metadata.Artist != "" && job.Metadata.Title != "" {
			displayName = job.Metadata.Artist + " - " + job.Metadata.Title
		} else if job.Metadata.Title != "" {
			displayName = job.Metadata.Title
		} else {
			displayName = job.Filename
		}
	}
	shortFilename := filepath.Base(job.Filename)

	// Create a per-job cancellable context so Cancel() can stop this download.
	jobCtx, jobCancel := context.WithCancel(p.ctx)
	p.cancelMu.Lock()
	p.cancels[downloadID] = jobCancel
	p.cancelMu.Unlock()
	defer func() {
		jobCancel()
		p.cancelMu.Lock()
		delete(p.cancels, downloadID)
		p.cancelMu.Unlock()
	}()

	// Resolve the source plugin.
	plugin := p.pluginRegistry.Get(job.SourceName)
	if plugin == nil {
		p.failJob(downloadID, fmt.Sprintf("plugin %q not found", job.SourceName))
		return
	}

	// Acquire per-plugin concurrency slot if the plugin has a limit.
	if cl, ok := plugin.(ConcurrencyLimited); ok && cl.MaxConcurrentDownloads() > 0 {
		sem := p.getPluginSemaphore(plugin.Name(), cl.MaxConcurrentDownloads())
		select {
		case sem <- struct{}{}:
		case <-jobCtx.Done():
			p.failJob(downloadID, "download cancelled while queued")
			return
		}
		defer func() { <-sem }()
	}

	// Transition state: queued → downloading atomically.
	if ok, err := p.store.TransitionState(jobCtx, downloadID, domain.DownloadQueued, domain.DownloadDownloading); err != nil {
		p.log.Error("state update failed", "download_id", downloadID, "error", err, "component", "worker")
		return
	} else if !ok {
		p.log.Warn("state changed before start, skipping", "download_id", downloadID, "component", "worker")
		return
	}
	p.publishRecord(downloadID, domain.DownloadDownloading, events.TopicDownloadStateChanged)

	// Start the plugin-specific download. The plugin returns its own internal ID
	// which we use for subsequent status and progress queries.
	pluginDownloadID, err := plugin.Download(jobCtx, job.Username, job.Filename, job.FileSize)
	if err != nil {
		p.failJob(downloadID, fmt.Sprintf("plugin download for %s: %v", displayName, err))
		return
	}

	// Poll until the plugin reports a terminal state.
	dp, _ := plugin.(DownloadProgressor)
	if err := p.pollUntilComplete(jobCtx, downloadID, plugin, dp, pluginDownloadID, displayName, shortFilename); err != nil {
		p.failJob(downloadID, err.Error())
		return
	}

	// State already transitioned to DownloadImportPending inside pollUntilComplete.
	p.publishRecord(downloadID, domain.DownloadImportPending, events.TopicDownloadCompleted)
}

// pollUntilComplete periodically checks the plugin for download status.
// Returns nil on success, error on failure/cancellation/timeout.
func (p *workerPoolImpl) pollUntilComplete(ctx context.Context, serviceID string, plugin Plugin, dp DownloadProgressor, pluginID string, displayName string, filename string) error {
	ticker := time.NewTicker(progressPollInterval)
	defer ticker.Stop()

	const downloadTimeout = 10 * time.Minute
	deadline := time.After(downloadTimeout)

	var lastFilePath string
	var errCount int

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("download cancelled: %s", displayName)
		case <-deadline:
			return fmt.Errorf("download timed out after %v: %s (%s)", downloadTimeout, displayName, filename)
		case <-ticker.C:
			// If the plugin supports DownloadProgressor, get high-resolution progress.
			if dp != nil {
				prog, err := dp.GetProgress(ctx, pluginID)
				if err == nil && prog != nil {
					p.fireProgress(serviceID, 0, prog.Transferred, prog.Total, prog.Speed)
				}
			}

			// Check terminal state via the plugin's status API.
			status, err := plugin.GetDownloadStatus(ctx, pluginID)
			if err != nil {
				errCount++
				if errCount >= 30 {
					return fmt.Errorf("status check failed for %s after %d attempts: %w", displayName, errCount, err)
				}
				p.log.Warn("status check failed", "service_id", serviceID, "attempt", errCount, "error", err, "component", "worker")
				continue
			}
			errCount = 0
			if status == nil {
				continue
			}

			// Remember file path from the plugin (set on completion).
			if status.FilePath != "" {
				lastFilePath = status.FilePath
			}

			// Sync progress back to our store record without overwriting metadata.
			// Uses job-level ctx so the update is skipped when the download is cancelled.
			_ = p.store.UpdateProgress(ctx, serviceID, domain.DownloadDownloading,
				status.Progress, status.Size, status.Transferred, status.Speed,
				lastFilePath, "")

			// Fire progress event with status data (may be overwritten by dp above).
			if dp == nil {
				p.fireProgress(serviceID, status.Progress, status.Transferred,
					status.Size, status.Speed)
			}

			// Check terminal state.
			// NOTE: Plugins use DownloadImported to mean "file on disk, ready for
			// import" — this is the PLUGIN's internal state, not the pipeline's
			// final state. After we see this, we transition to DownloadImportPending
			// and CompletedDownloadService takes over.
			p.log.Debug("download progress", "service_id", serviceID, "state", status.State, "progress_pct", status.Progress, "component", "worker")
			switch {
			case status.State == domain.DownloadImported:
				if lastFilePath != "" {
					_ = p.store.UpdateProgress(ctx, serviceID, domain.DownloadImportPending,
						100, status.Size, status.Size, 0, lastFilePath, status.CoverURL)
				}
				return nil
			case status.State == domain.DownloadFailed:
				reason := status.Error
				if reason == "" {
					reason = "peer offline or file unavailable"
				}
				return fmt.Errorf("download failed: %s", reason)
			case status.State == domain.DownloadIgnored:
				return fmt.Errorf("download cancelled")
			}
		}
	}
}

// failJob transitions a download to the failed state and publishes events.
// If the download is already in a terminal state (e.g., cancelled), it is a no-op.
// Uses TransitionState to atomically guard against concurrent Cancel() setting
// "ignored" between the Get check and the state write.
func (p *workerPoolImpl) failJob(downloadID, errMsg string) {
	record, err := p.store.Get(p.ctx, downloadID)
	if err == nil && record != nil && record.State.Terminal() {
		return // already terminal, don't overwrite
	}

	oldState := domain.DownloadQueued // safe default if record is nil
	if record != nil {
		oldState = record.State
	}

	p.log.Error("download failed", "download_id", downloadID, "error", errMsg, "component", "worker")

	// Atomic transition — returns false if a concurrent Cancel() already
	// changed the state to ignored (or any other terminal state).
	if ok, _ := p.store.TransitionState(p.ctx, downloadID, oldState, domain.DownloadFailed); !ok {
		return // another caller won the race
	}

	// Persist the error message after the successful state transition.
	_ = p.store.Update(p.ctx, &domain.DownloadRecord{
		ID:    downloadID,
		State: domain.DownloadFailed,
		Error: errMsg,
	})

	p.publishRecord(downloadID, domain.DownloadFailed, events.TopicDownloadFailed)
}

// getPluginSemaphore returns or lazily creates a bounded channel for the given
// plugin. Used to limit concurrent downloads per plugin (e.g., Deezer limits to 2
// to avoid CDN throttling).
func (p *workerPoolImpl) getPluginSemaphore(name string, max int) chan struct{} {
	p.pluginSemMu.Lock()
	defer p.pluginSemMu.Unlock()
	if sem, ok := p.pluginSemaphores[name]; ok {
		return sem
	}
	sem := make(chan struct{}, max)
	p.pluginSemaphores[name] = sem
	return sem
}

// publishRecord fires a lifecycle event with a minimal download record.
func (p *workerPoolImpl) publishRecord(downloadID string, state domain.DownloadState, topic string) {
	p.bus.Publish(p.ctx, topic, &domain.DownloadRecord{
		ID:    downloadID,
		State: state,
	})
}

// fireProgress publishes a TopicDownloadProgress event using a minimal
// domain.DownloadRecord so SSE notifier type-assertion works uniformly.
func (p *workerPoolImpl) fireProgress(downloadID string, progress float64, transferred, total, speed int64) {
	p.bus.Publish(p.ctx, events.TopicDownloadProgress, &domain.DownloadRecord{
		ID:          downloadID,
		State:       domain.DownloadDownloading,
		Progress:    progress,
		Transferred: transferred,
		Size:        total,
		Speed:       speed,
	})
}
