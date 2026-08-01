package download

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/sanitize"
)

// ---------------------------------------------------------------------------
// Start queued downloads
// ---------------------------------------------------------------------------

// startQueuedDownloads scans the store for queued records, resolves pending
// sources, respects per-plugin concurrency limits, and starts downloads via
// the provider's StartDownload method.
func (m *MonitoringService) startQueuedDownloads() {
	queued, err := m.store.ListByState(m.ctx, StateQueued)
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
		// download failure. Album records use MagnetURI instead of Filename.
		if !rec.IsAlbum() && rec.Filename == "" {
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
		if fresh.State != StateQueued {
			continue
		}

		// Route album downloads through the DownloadClient,
		// track downloads through the existing MonitoredProvider path.
		if fresh.IsAlbum() {
			m.log.Info("routing to album download client",
				"download_id", fresh.ID,
				"client", fresh.DownloadClient,
				"album", fresh.Album,
				"artist", fresh.Artist,
				"component", "monitor",
			)
			m.startAlbumDownload(fresh)
		} else {
			m.log.Info("routing to track download plugin",
				"download_id", fresh.ID,
				"plugin", fresh.SourceName,
				"artist", fresh.Artist,
				"title", fresh.Title,
				"component", "monitor",
			)
			m.startSingleDownload(fresh)
		}
	}
}

// startSingleDownload initiates a download for a single queued record. It
// looks up the plugin, checks concurrency limits, transitions the store
// state to downloading, calls StartDownload, and registers the mapping.
func (m *MonitoringService) startSingleDownload(rec *Record) {
	downloadID := rec.ID
	sourceName := rec.SourceName

	// Look up the plugin.
	plugin := m.registry.Get(sourceName)
	if plugin == nil {
		m.failRecord(downloadID, StateQueued,
			fmt.Sprintf("plugin %q not found", sourceName))
		return
	}

	mp, ok := plugin.(MonitoredProvider)
	if !ok {
		m.failRecord(downloadID, StateQueued,
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
	if ok, err := m.store.TransitionState(m.ctx, downloadID, StateQueued, StateDownloading); err != nil {
		m.log.Error("startSingleDownload: transition failed",
			"download_id", downloadID, "error", err, "component", "monitor")
		return
	} else if !ok {
		m.log.Warn("startSingleDownload: state changed before start, skipping",
			"download_id", downloadID, "component", "monitor")
		return
	}

	// Publish state-changed event.
	m.publishRecord(downloadID, StateDownloading, events.TopicDownloadStateChanged)

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
	meta := Meta{
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
		m.failRecord(downloadID, StateDownloading,
			fmt.Sprintf("start download for %s: %v", displayName, err))
		return
	}

	// Track the mapping.
	m.addMapping(downloadID, providerID, plugin.Name(), time.Now(), deadline)

	// Persist ProviderID so Service.Cancel can cancel the provider directly.
	if rec, err := m.store.Get(m.ctx, downloadID); err == nil && rec != nil && rec.State == StateDownloading {
		rec.ProviderID = providerID
		if err := m.store.Update(m.ctx, rec); err != nil {
			m.log.Warn("failed to persist provider ID on record",
				"download_id", downloadID,
				"provider_id", providerID,
				"error", err,
				"component", "monitor",
			)
		}
	}

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

// startAlbumDownload dispatches an album-level download record through
// the configured DownloadClient. Unlike per-track downloads which use
// MonitoredProvider, album downloads route through the DownloadClientRegistry.
func (m *MonitoringService) startAlbumDownload(rec *Record) {
	downloadID := rec.ID

	if m.downloadClients == nil {
		m.failRecord(downloadID, StateQueued, "no download client registry configured")
		return
	}

	clientName := rec.DownloadClient
	if clientName == "" {
		m.failRecord(downloadID, StateQueued, "no download_client set on album record")
		return
	}

	dc := m.downloadClients.Get(clientName)
	if dc == nil {
		m.failRecord(downloadID, StateQueued,
			fmt.Sprintf("download client %q not found in registry", clientName))
		return
	}

	// Acquire per-client concurrency slot.
	maxConc := dc.MaxConcurrent()
	semAcquired := false
	if maxConc > 0 {
		sem := m.getSemaphore(clientName, maxConc)
		select {
		case sem <- struct{}{}:
			semAcquired = true
		default:
			return // pool full, retry next tick
		}
	}
	defer func() {
		if semAcquired {
			m.releaseSemaphore(clientName)
		}
	}()

	// Transition state: queued → downloading.
	if ok, err := m.store.TransitionState(m.ctx, downloadID, StateQueued, StateDownloading); err != nil {
		m.log.Error("startAlbumDownload: transition failed",
			"download_id", downloadID, "error", err, "component", "monitor")
		return
	} else if !ok {
		return
	}

	m.publishRecord(downloadID, StateDownloading, events.TopicDownloadStateChanged)

	savePath := filepath.Join(clientBasePath(m.downloadPath(), dc), sanitize.PathSegment(rec.Album))

	uri := rec.MagnetURI
	if uri == "" {
		uri = rec.Filename // fallback
	}

	providerID, err := dc.AddDownload(m.ctx, uri, "music", savePath)
	if err != nil {
		m.failRecord(downloadID, StateDownloading,
			fmt.Sprintf("add download: %v", err))
		return
	}

	// Track in the active map BEFORE persisting ProviderID to the store.
	// This ensures checkCancellations has the correct providerID even if the
	// DB write hasn't completed yet.
	deadline := time.Now().Add(dc.DownloadTimeout())
	m.addDownloadClientMapping(downloadID, providerID, clientName, time.Now(), deadline)
	semAcquired = false

	// Store provider ID on the record so Cancel can find it later.
	// Only write if the record is still in StateDownloading — a concurrent
	// Cancel may have already set it to StateIgnored.
	if rec, err := m.store.Get(m.ctx, downloadID); err == nil && rec != nil && rec.State == StateDownloading {
		rec.ProviderID = providerID
		if err := m.store.Update(m.ctx, rec); err != nil {
			m.log.Warn("failed to persist provider ID on record",
				"download_id", downloadID,
				"provider_id", providerID,
				"error", err,
				"component", "monitor",
			)
		}
	}

	m.log.Info("album download started",
		"download_id", downloadID,
		"client", clientName,
		"provider_id", providerID,
		"album", rec.Album,
		"component", "monitor",
	)
}

// downloadPath returns the configured download base path.
func (m *MonitoringService) downloadPath() string {
	if m.downloadBasePathFunc != nil {
		if p := m.downloadBasePathFunc(); p != "" {
			return p
		}
	}
	if m.downloadBasePath != "" {
		return m.downloadBasePath
	}
	return "./downloads"
}

// clientBasePath returns the download client's configured base path if set,
// otherwise falls back to the library's global download path.
func clientBasePath(globalPath string, dc DownloadClient) string {
	if dc != nil {
		if p := dc.DownloadBasePath(); p != "" {
			return p
		}
	}
	return globalPath
}

// ---------------------------------------------------------------------------
// Poll active downloads
// ---------------------------------------------------------------------------

// pollActiveDownloads iterates over all tracked downloads and queries each
// provider for status and progress in parallel. This ensures a smooth live
// view — before PR #2 each download had its own goroutine polling independently.
func (m *MonitoringService) pollActiveDownloads() {
	m.activeMu.Lock()
	snapshot := make([]*monitoredDownload, 0, len(m.active))
	for _, md := range m.active {
		cp := *md
		snapshot = append(snapshot, &cp)
	}
	m.activeMu.Unlock()

	var wg sync.WaitGroup
	for _, md := range snapshot {
		wg.Add(1)
		go func(md *monitoredDownload) {
			defer wg.Done()
			m.pollSingle(md)
		}(md)
	}
	wg.Wait()
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
		m.failRecord(md.recordID, StateDownloading, reason)
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)
		return
	}

	// Track-level downloads poll via MonitoredProvider,
	// album downloads poll via DownloadClient.
	if md.downloadClientName != "" {
		m.pollAlbumDownload(md)
		return
	}

	// Look up the provider via the registry.
	plugin := m.registry.Get(md.pluginName)
	if plugin == nil {
		m.log.Warn("pollSingle: plugin not found in registry, failing download",
			"plugin", md.pluginName, "download_id", md.recordID, "component", "monitor")
		m.failRecord(md.recordID, StateDownloading,
			fmt.Sprintf("plugin %q unavailable", md.pluginName))
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)
		return
	}
	mp, ok := plugin.(MonitoredProvider)
	if !ok {
		m.log.Warn("pollSingle: plugin no longer supports monitored downloads, failing download",
			"plugin", md.pluginName, "download_id", md.recordID, "component", "monitor")
		m.failRecord(md.recordID, StateDownloading,
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
	_ = m.store.UpdateProgress(m.ctx, md.recordID, StateDownloading,
		bestProgress, bestTotal, bestTransferred, bestSpeed,
		status.FilePath, status.CoverURL)

	// Fire progress event.
	m.fireProgress(md.recordID, bestProgress, bestTransferred, bestTotal, bestSpeed)

	// Handle terminal states from provider.
	m.handleProviderState(md, status)
}

// handleProviderState inspects the provider's reported download state and
// transitions the groovearr record accordingly.
func (m *MonitoringService) handleProviderState(md *monitoredDownload, status *Record) {
	switch status.State {
	case StateImported, StateImportPending:
		// Provider says file is on disk → transition to importPending.
		if ok, err := m.store.TransitionState(m.ctx, md.recordID, StateDownloading, StateImportPending); err != nil {
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
			_ = m.store.UpdateProgress(m.ctx, md.recordID, StateImportPending,
				100, status.Size, status.Size, 0, status.FilePath, status.CoverURL)
		}

		// Re-read the record for the event so subscribers see the latest data.
		fresh, err := m.store.Get(m.ctx, md.recordID)
		if err == nil && fresh != nil {
			m.bus.Publish(m.ctx, events.TopicDownloadCompleted, fresh)
		} else {
			m.publishRecord(md.recordID, StateImportPending, events.TopicDownloadCompleted)
		}

		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)

	case StateFailed:
		reason := status.Error
		if reason == "" {
			reason = "provider reported download failure"
		}
		m.failRecord(md.recordID, StateDownloading, reason)
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)

	case StateIgnored:
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

		if fresh.State != StateDownloading {
			// StateIgnored means Service.Cancel() already handled the provider
			// cancellation — skip to avoid double-cancelling.
			if fresh.State == StateIgnored {
				m.removeTracking(md.recordID, md.providerID)
				m.releaseSemaphore(md.pluginName)
				continue
			}

			m.log.Info("checkCancellations: state changed externally, cancelling provider download",
				"download_id", md.recordID, "state", fresh.State, "component", "monitor")

			// Cancel via DownloadClient for album downloads, MonitoredProvider for tracks.
			if md.downloadClientName != "" && m.downloadClients != nil {
				if dc := m.downloadClients.Get(md.downloadClientName); dc != nil {
					if err := dc.Cancel(m.ctx, md.providerID, false); err != nil {
						m.log.Warn("checkCancellations: client cancel failed",
							"download_id", md.recordID, "provider_id", md.providerID,
							"error", err, "component", "monitor")
					}
				} else {
					m.log.Warn("checkCancellations: download client not found, cannot cancel",
						"download_id", md.recordID, "client", md.downloadClientName, "component", "monitor")
				}
			} else if plugin := m.registry.Get(md.pluginName); plugin != nil {
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
// Album download polling
// ---------------------------------------------------------------------------

// pollAlbumDownload polls a DownloadClient for an album download's status
// and handles state transitions.
func (m *MonitoringService) pollAlbumDownload(md *monitoredDownload) {
	if m.downloadClients == nil {
		return
	}

	dc := m.downloadClients.Get(md.downloadClientName)
	if dc == nil {
		m.failRecord(md.recordID, StateDownloading,
			fmt.Sprintf("download client %q not found", md.downloadClientName))
		m.removeTracking(md.recordID, md.providerID)
		m.releaseSemaphore(md.pluginName)
		return
	}

	status, err := dc.GetStatus(m.ctx, md.providerID)
	if err != nil {
		m.log.Warn("pollAlbumDownload: get status failed",
			"download_id", md.recordID, "provider_id", md.providerID,
			"error", err, "component", "monitor")
		return
	}
	if status == nil {
		return
	}

	prog, _ := dc.GetProgress(m.ctx, md.providerID)

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

	_ = m.store.UpdateProgress(m.ctx, md.recordID, StateDownloading,
		bestProgress, bestTotal, bestTransferred, bestSpeed,
		status.FilePath, status.CoverURL)

	m.fireProgress(md.recordID, bestProgress, bestTransferred, bestTotal, bestSpeed)
	m.handleProviderState(md, status)
}

// ---------------------------------------------------------------------------
// Fail record
// ---------------------------------------------------------------------------

// failRecord atomically transitions a download to the failed state and
// publishes a TopicDownloadFailed event. If the download is already in a
// terminal state, this is a no-op.
func (m *MonitoringService) failRecord(downloadID string, oldState State, errMsg string) {
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
	if ok, _ := m.store.TransitionState(m.ctx, downloadID, attemptOld, StateFailed); !ok {
		return // another caller won the race
	}

	// Re-read to get the freshest record for the event.
	if fresh, err := m.store.Get(m.ctx, downloadID); err == nil && fresh != nil {
		record = fresh
	}
	if record == nil {
		record = &Record{ID: downloadID}
	}
	record.State = StateFailed
	record.Error = errMsg
	_ = m.store.Update(m.ctx, record)

	m.bus.Publish(m.ctx, events.TopicDownloadFailed, record)
}
