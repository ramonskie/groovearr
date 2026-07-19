package download

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

const defaultMaxWorkers = 3
const progressPollInterval = 1 * time.Second

// DownloadJob represents a download task dispatched to a worker.
type DownloadJob struct {
	DownloadID string
	SourceName string
	Username   string
	Filename   string
	FileSize   int64
	Metadata   DownloadMeta
}

// workerPoolImpl implements WorkerPool with bounded goroutines and graceful shutdown.
type workerPoolImpl struct {
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
}

// NewWorkerPool creates a worker pool that runs up to maxWorkers concurrent
// download goroutines. maxWorkers defaults to defaultMaxWorkers (3) when zero
// or negative. Each worker picks jobs from a buffered channel and drives the
// download through its full lifecycle: downloading → importPending (or failed).
func NewWorkerPool(maxWorkers int, registry *Registry, store DownloadStore, bus events.IEventAggregator) WorkerPool {
	if maxWorkers <= 0 {
		maxWorkers = defaultMaxWorkers
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &workerPoolImpl{
		maxWorkers:     maxWorkers,
		jobQueue:       make(chan *DownloadJob, maxWorkers*2),
		pluginRegistry: registry,
		store:          store,
		bus:            bus,
		ctx:            ctx,
		cancel:         cancel,
		cancels:        make(map[string]context.CancelFunc),
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
		DownloadID: record.ID,
		SourceName: record.SourceName,
		Username:   username,
		Filename:   record.Filename,
		FileSize:   record.Size,
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
	p.cancel()
	close(p.jobQueue)
	p.wg.Wait()
}

// worker loops on the job queue until it is closed.
func (p *workerPoolImpl) worker() {
	defer p.wg.Done()
	for job := range p.jobQueue {
		p.processJob(job)
	}
}

// processJob runs the full download lifecycle for a single job.
func (p *workerPoolImpl) processJob(job *DownloadJob) {
	downloadID := job.DownloadID

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

	// Transition state: queued → downloading.
	if err := p.updateStoreState(downloadID, domain.DownloadDownloading); err != nil {
		log.Printf("worker: state update failed for %s: %v", downloadID, err)
		return
	}
	p.publishRecord(downloadID, domain.DownloadDownloading, events.TopicDownloadStateChanged)

	// Resolve the source plugin.
	plugin := p.pluginRegistry.Get(job.SourceName)
	if plugin == nil {
		p.failJob(downloadID, fmt.Sprintf("plugin %q not found", job.SourceName))
		return
	}

	// Start the plugin-specific download. The plugin returns its own internal ID
	// which we use for subsequent status and progress queries.
	pluginDownloadID, err := plugin.Download(jobCtx, job.Username, job.Filename, job.FileSize)
	if err != nil {
		p.failJob(downloadID, fmt.Sprintf("plugin download: %v", err))
		return
	}

	// Poll until the plugin reports a terminal state.
	dp, _ := plugin.(DownloadProgressor)
	if err := p.pollUntilComplete(jobCtx, downloadID, plugin, dp, pluginDownloadID); err != nil {
		p.failJob(downloadID, err.Error())
		return
	}

	// State already transitioned to DownloadImportPending inside pollUntilComplete.
	// Only publish the event — don't call updateStoreState which would blank file_path.
	p.publishRecord(downloadID, domain.DownloadImportPending, events.TopicDownloadCompleted)
}

// pollUntilComplete periodically checks the plugin for download status.
// Returns nil on success, error on failure/cancellation.
func (p *workerPoolImpl) pollUntilComplete(ctx context.Context, serviceID string, plugin Plugin, dp DownloadProgressor, pluginID string) error {
	ticker := time.NewTicker(progressPollInterval)
	defer ticker.Stop()

	var lastFilePath string

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("download cancelled")
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
				log.Printf("worker: status check for %s: %v", serviceID, err)
				continue
			}
			if status == nil {
				continue
			}

			// Remember file path from the plugin (set on completion).
			if status.FilePath != "" {
				lastFilePath = status.FilePath
			}

			// Sync progress back to our store record.
			_ = p.store.Update(p.ctx, &domain.DownloadRecord{
				ID:          serviceID,
				State:       domain.DownloadDownloading,
				Progress:    status.Progress,
				Size:        status.Size,
				Transferred: status.Transferred,
				Speed:       status.Speed,
				FilePath:    lastFilePath,
			})

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
			switch {
			case status.State == domain.DownloadImported:
				if lastFilePath != "" {
					_ = p.store.Update(p.ctx, &domain.DownloadRecord{
						ID:       serviceID,
						FilePath: lastFilePath,
						State:    domain.DownloadImportPending,
					})
				}
				return nil
			case status.State == domain.DownloadFailed:
				return fmt.Errorf("download failed: %s", status.Error)
			case status.State == domain.DownloadIgnored:
				return fmt.Errorf("download cancelled")
			}
		}
	}
}

// failJob transitions a download to the failed state and publishes events.
// If the download is already in a terminal state (e.g., cancelled), it is a no-op.
func (p *workerPoolImpl) failJob(downloadID, errMsg string) {
	record, err := p.store.Get(p.ctx, downloadID)
	if err == nil && record != nil && record.State.Terminal() {
		return // already terminal, don't overwrite
	}
	log.Printf("worker: download %s FAILED: %s", downloadID, errMsg)
	_ = p.store.Update(p.ctx, &domain.DownloadRecord{
		ID:    downloadID,
		State: domain.DownloadFailed,
		Error: errMsg,
	})
	p.publishRecord(downloadID, domain.DownloadFailed, events.TopicDownloadFailed)
}

// updateStoreState persists a state change to the store.
func (p *workerPoolImpl) updateStoreState(downloadID string, state domain.DownloadState) error {
	return p.store.Update(p.ctx, &domain.DownloadRecord{
		ID:    downloadID,
		State: state,
	})
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
