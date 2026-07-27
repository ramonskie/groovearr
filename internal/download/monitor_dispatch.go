package download

import (
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

// ---------------------------------------------------------------------------
// Start queued downloads
// ---------------------------------------------------------------------------

// startQueuedDownloads scans the store for queued records, resolves pending
// sources, respects per-plugin concurrency limits, and starts downloads via
// the provider's StartDownload method.
func (m *MonitoringService) startQueuedDownloads() {
	queued, err := m.store.ListByState(m.ctx, domain.DownloadQueued)
	if err != nil {
		m.log.Error("startQueuedDownloads: list queued failed", "error", err, "component", "monitor")
		return
	}
	if len(queued) == 0 {
		return
	}

	for _, rec := range queued {
		// Skip records already being tracked (shouldn't happen, but safe).
		m.activeMu.Lock()
		_, tracking := m.active[rec.ID]
		m.activeMu.Unlock()
		if tracking {
			continue
		}

		// Skip pending-source records — resolved by resolvePendingSources on
		// startup or via the periodic retry loop when they become failedPending.
		if rec.IsPendingSource() {
			continue
		}

		// Skip records mid-retry-resolution. Retry() clears source fields
		// before launching a background goroutine that searches for alternative
		// sources. Picking up a record with no Filename would cause a spurious
		// download failure.
		if rec.Filename == "" {
			continue
		}

		// Respect retry backoff.
		if rec.RetryAfter != "" {
			if t, err := time.Parse(time.RFC3339, rec.RetryAfter); err != nil {
				m.log.Warn("startQueuedDownloads: invalid RetryAfter, proceeding",
					"download_id", rec.ID, "retry_after", rec.RetryAfter, "error", err, "component", "monitor")
			} else if time.Now().UTC().Before(t) {
				continue
			}
		}

		// Re-read from store to confirm state hasn't changed.
		fresh, err := m.store.Get(m.ctx, rec.ID)
		if err != nil || fresh == nil {
			continue
		}
		if fresh.State != domain.DownloadQueued {
			continue
		}

		m.startSingleDownload(fresh)
	}
}

// startSingleDownload initiates a download for a single queued record. It
// looks up the plugin, checks concurrency limits, transitions the store
// state to downloading, calls StartDownload, and registers the mapping.
func (m *MonitoringService) startSingleDownload(rec *domain.DownloadRecord) {
	downloadID := rec.ID
	sourceName := rec.SourceName

	// Look up the plugin.
	plugin := m.registry.Get(sourceName)
	if plugin == nil {
		m.failRecord(downloadID, domain.DownloadQueued,
			fmt.Sprintf("plugin %q not found", sourceName))
		return
	}

	mp, ok := plugin.(MonitoredProvider)
	if !ok {
		m.failRecord(downloadID, domain.DownloadQueued,
			fmt.Sprintf("plugin %q does not support monitored downloads", sourceName))
		return
	}

	// Acquire per-plugin concurrency slot.
	maxConc := mp.MaxConcurrent()
	semAcquired := false
	if maxConc > 0 {
		sem := m.getSemaphore(plugin.Name(), maxConc)
		select {
		case sem <- struct{}{}:
			semAcquired = true
		default:
			// Pool full, try again next tick.
			return
		}
	}
	defer func() {
		if semAcquired {
			m.releaseSemaphore(plugin.Name())
		}
	}()

	// Transition state atomically: queued → downloading.
	if ok, err := m.store.TransitionState(m.ctx, downloadID, domain.DownloadQueued, domain.DownloadDownloading); err != nil {
		m.log.Error("startSingleDownload: transition failed",
			"download_id", downloadID, "error", err, "component", "monitor")
		return
	} else if !ok {
		m.log.Warn("startSingleDownload: state changed before start, skipping",
			"download_id", downloadID, "component", "monitor")
		return
	}

	// Publish state-changed event.
	m.publishRecord(downloadID, domain.DownloadDownloading, events.TopicDownloadStateChanged)

	// Build metadata for the provider.
	displayName := rec.DisplayName
	if displayName == "" {
		if rec.Artist != "" && rec.Title != "" {
			displayName = rec.Artist + " - " + rec.Title
		} else if rec.Title != "" {
			displayName = rec.Title
		} else {
			displayName = rec.Filename
		}
	}
	meta := DownloadMeta{
		Artist:      rec.Artist,
		Album:       rec.Album,
		Title:       rec.Title,
		TrackNumber: rec.TrackNumber,
		DiscNumber:  rec.DiscNumber,
		Year:        rec.Year,
		TrackID:     rec.TrackID,
		ISRC:        rec.ISRC,
		CoverURL:    rec.CoverURL,
		PlaylistID:  rec.PlaylistID,
		Bitrate:     rec.Bitrate,
		Format:      rec.Format,
		Username:    rec.Username,
		Filename:    rec.Filename,
		Size:        rec.Size,
	}

	// Apply per-provider timeout for tracking (enforced in pollSingle via
	// md.deadline). Do NOT use a timeout context for StartDownload — providers
	// like Deezer derive their background goroutine context from the passed-in
	// context, and a WithTimeout that expires before the goroutine runs will
	// cancel the download before it begins.
	deadline := time.Now().Add(mp.DownloadTimeout())

	// Start the download on the provider side. Use the monitor's long-lived
	// context so derived provider contexts (e.g., Deezer's download goroutine)
	// are not prematurely cancelled.
	providerID, err := mp.StartDownload(m.ctx, meta)
	if err != nil {
		m.failRecord(downloadID, domain.DownloadDownloading,
			fmt.Sprintf("start download for %s: %v", displayName, err))
		return
	}

	// Track the mapping.
	m.addMapping(downloadID, providerID, plugin.Name(), time.Now(), deadline)

	// Slot held until download completes/fails in pollActiveDownloads.
	semAcquired = false

	m.log.Info("download started",
		"download_id", downloadID,
		"provider_id", providerID,
		"plugin", plugin.Name(),
		"display_name", displayName,
		"component", "monitor",
	)
}

// ---------------------------------------------------------------------------
// Poll active downloads
// ---------------------------------------------------------------------------

// pollActiveDownloads iterates over all tracked downloads, queries the
// provider for status and progress, updates the store, and handles terminal
// states (completed, failed, cancelled).
func (m *MonitoringService) pollActiveDownloads() {
	m.activeMu.Lock()
	// Snapshot active downloads to avoid holding the lock during I/O.
	snapshot := make([]*monitoredDownload, 0, len(m.active))
	for _, md := range m.active {
		cp := *md
		snapshot = append(snapshot, &cp)
	}
	m.activeMu.Unlock()

	for _, md := range snapshot {
		m.pollSingle(md)
	}
}

// pollSingle polls one active download's status from its provider and handles
// state transitions. If the download record has been externally modified
// (cancelled/ignored), it stops polling.
func (m *MonitoringService) pollSingle(md *monitoredDownload) {
	// Confirm this download is still tracked (may have been removed by
	// checkCancellations or another pollSingle completing it concurrently).
	m.activeMu.Lock()
	_, tracking := m.active[md.recordID]
	m.activeMu.Unlock()
	if !tracking {
		return
	}

	// Check for per-download timeout.
	if !md.deadline.IsZero() && time.Now().After(md.deadline) {
		elapsed := time.Since(md.startedAt).Round(time.Second)
		reason := fmt.Sprintf("download timed out after %v", elapsed)
		m.failRecord(md.recordID, domain.DownloadDownloading, reason)
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)
		return
	}

	// Look up the provider via the registry.
	plugin := m.registry.Get(md.pluginName)
	if plugin == nil {
		m.log.Warn("pollSingle: plugin not found in registry, failing download",
			"plugin", md.pluginName, "download_id", md.recordID, "component", "monitor")
		m.failRecord(md.recordID, domain.DownloadDownloading,
			fmt.Sprintf("plugin %q unavailable", md.pluginName))
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)
		return
	}
	mp, ok := plugin.(MonitoredProvider)
	if !ok {
		m.log.Warn("pollSingle: plugin no longer supports monitored downloads, failing download",
			"plugin", md.pluginName, "download_id", md.recordID, "component", "monitor")
		m.failRecord(md.recordID, domain.DownloadDownloading,
			fmt.Sprintf("plugin %q type changed", md.pluginName))
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)
		return
	}

	// Query provider status.
	status, err := mp.GetStatus(m.ctx, md.providerID)
	if err != nil {
		m.log.Warn("pollSingle: get status failed",
			"download_id", md.recordID, "provider_id", md.providerID, "error", err, "component", "monitor")
		return
	}
	if status == nil {
		return
	}

	// Query provider progress for live byte-level updates.
	prog, _ := mp.GetProgress(m.ctx, md.providerID)

	bestProgress := status.Progress
	bestTransferred := status.Transferred
	bestTotal := status.Size
	bestSpeed := status.Speed

	if prog != nil {
		bestTransferred = prog.Transferred
		bestTotal = prog.Total
		bestSpeed = prog.Speed
		if prog.Total > 0 {
			bestProgress = float64(prog.Transferred) / float64(prog.Total) * 100
		}
	}

	// Sync progress to store.
	_ = m.store.UpdateProgress(m.ctx, md.recordID, domain.DownloadDownloading,
		bestProgress, bestTotal, bestTransferred, bestSpeed,
		status.FilePath, status.CoverURL)

	// Fire progress event.
	m.fireProgress(md.recordID, bestProgress, bestTransferred, bestTotal, bestSpeed)

	// Handle terminal states from provider.
	m.handleProviderState(md, status)
}

// handleProviderState inspects the provider's reported download state and
// transitions the groovearr record accordingly.
func (m *MonitoringService) handleProviderState(md *monitoredDownload, status *domain.DownloadRecord) {
	switch status.State {
	case domain.DownloadImported:
		// Provider says file is on disk → transition to importPending.
		if ok, err := m.store.TransitionState(m.ctx, md.recordID, domain.DownloadDownloading, domain.DownloadImportPending); err != nil {
			m.log.Error("handleProviderState: transition to importPending failed",
				"download_id", md.recordID, "error", err, "component", "monitor")
			return
		} else if !ok {
			m.log.Warn("handleProviderState: state changed before importPending, skipping",
				"download_id", md.recordID, "component", "monitor")
			m.removeTracking(md.recordID, md.providerID)
			m.releaseSemaphore(md.pluginName)
			return
		}

		if status.FilePath != "" {
			_ = m.store.UpdateProgress(m.ctx, md.recordID, domain.DownloadImportPending,
				100, status.Size, status.Size, 0, status.FilePath, status.CoverURL)
		}

		// Re-read the record for the event so subscribers see the latest data.
		fresh, err := m.store.Get(m.ctx, md.recordID)
		if err == nil && fresh != nil {
			m.bus.Publish(m.ctx, events.TopicDownloadCompleted, fresh)
		} else {
			m.publishRecord(md.recordID, domain.DownloadImportPending, events.TopicDownloadCompleted)
		}

		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)

	case domain.DownloadFailed:
		reason := status.Error
		if reason == "" {
			reason = "provider reported download failure"
		}
		m.failRecord(md.recordID, domain.DownloadDownloading, reason)
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)

	case domain.DownloadIgnored:
		// Provider-side cancellation.
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)

	default:
		// Still in progress — will poll again next tick.
	}
}

// ---------------------------------------------------------------------------
// Cancellation detection
// ---------------------------------------------------------------------------

// checkCancellations re-reads each tracked download from the store. If the
// state has been changed externally to ignored or failed (e.g., by the API
// Cancel endpoint), the monitoring service cancels the provider-level download
// and stops tracking.
func (m *MonitoringService) checkCancellations() {
	m.activeMu.Lock()
	snapshot := make([]*monitoredDownload, 0, len(m.active))
	for _, md := range m.active {
		cp := *md
		snapshot = append(snapshot, &cp)
	}
	m.activeMu.Unlock()

	for _, md := range snapshot {
		// Confirm still tracked.
		m.activeMu.Lock()
		_, tracking := m.active[md.recordID]
		m.activeMu.Unlock()
		if !tracking {
			continue
		}

		fresh, err := m.store.Get(m.ctx, md.recordID)
		if err != nil || fresh == nil {
			// Record disappeared — remove tracking.
			m.removeTracking(md.recordID, md.providerID)
			m.releaseSemaphore(md.pluginName)
			continue
		}

		if fresh.State != domain.DownloadDownloading {
			m.log.Info("checkCancellations: state changed externally, cancelling provider download",
				"download_id", md.recordID, "state", fresh.State, "component", "monitor")

			// Cancel the provider-level download before removing tracking.
			if plugin := m.registry.Get(md.pluginName); plugin != nil {
				if mp, ok := plugin.(MonitoredProvider); ok {
					if err := mp.Cancel(m.ctx, md.providerID, false); err != nil {
						m.log.Warn("checkCancellations: provider cancel failed",
							"download_id", md.recordID, "provider_id", md.providerID,
							"error", err, "component", "monitor")
					}
				}
			}

			m.removeTracking(md.recordID, md.providerID)
			m.releaseSemaphore(md.pluginName)
		}
	}
}

// ---------------------------------------------------------------------------
// Fail record
// ---------------------------------------------------------------------------

// failRecord atomically transitions a download to the failed state and
// publishes a TopicDownloadFailed event. If the download is already in a
// terminal state, this is a no-op.
func (m *MonitoringService) failRecord(downloadID string, oldState domain.DownloadState, errMsg string) {
	record, err := m.store.Get(m.ctx, downloadID)
	if err == nil && record != nil && record.State.Terminal() {
		return // already terminal — don't overwrite
	}

	m.log.Error("download failed", "download_id", downloadID, "error", errMsg, "component", "monitor")

	// Atomic transition — returns false if a concurrent call already changed
	// the state (e.g., external Cancel).
	attemptOld := oldState
	if record != nil {
		attemptOld = record.State
	}
	if ok, _ := m.store.TransitionState(m.ctx, downloadID, attemptOld, domain.DownloadFailed); !ok {
		return // another caller won the race
	}

	// Re-read to get the freshest record for the event.
	if fresh, err := m.store.Get(m.ctx, downloadID); err == nil && fresh != nil {
		record = fresh
	}
	if record == nil {
		record = &domain.DownloadRecord{ID: downloadID}
	}
	record.State = domain.DownloadFailed
	record.Error = errMsg
	_ = m.store.Update(m.ctx, record)

	m.bus.Publish(m.ctx, events.TopicDownloadFailed, record)
}
