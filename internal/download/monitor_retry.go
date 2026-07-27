package download

import (
	"context"
	"time"

	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/quality"
)

// ---------------------------------------------------------------------------
// Retry logic
// ---------------------------------------------------------------------------

// scanRetry lists failed downloads and retries those that haven't exceeded
// MaxRetries and whose RetryAfter backoff has elapsed. Uses exponential
// backoff: 2^retryCount minutes, capped at 60.
//
// Before re-queuing, it searches all registered providers for an alternative
// download source via Orchestrator.FindBestMatch. If a better source is found,
// the record is updated to use it. If no alternative is found, the original
// source is kept and re-queued anyway (the same provider may have recovered).
func (m *MonitoringService) scanRetry() {
	failed, err := m.store.ListByState(m.ctx, StateFailed)
	if err != nil {
		m.log.Error("scanRetry: list failed", "error", err, "component", "monitor")
		return
	}
	failedPending, err := m.store.ListByState(m.ctx, StateFailedPending)
	if err != nil {
		m.log.Error("scanRetry: list failedPending", "error", err, "component", "monitor")
		return
	}
	failed = append(failed, failedPending...)
	if len(failed) == 0 {
		return
	}

	retried := 0
	for _, rec := range failed {
		if rec.RetryCount >= MaxRetries {
			continue
		}
		if rec.RetryAfter != "" {
			retryAfter, parseErr := time.Parse(time.RFC3339, rec.RetryAfter)
			if parseErr == nil && time.Now().UTC().Before(retryAfter) {
				continue
			}
		}

		// Search for alternative sources across all providers.
		// Use a short timeout to prevent a hanging plugin from blocking the loop.
		// If no source is found, skip this retry — do NOT re-queue with a source
		// that just failed. The retry cycle will try again when backoff elapses.
		found := true
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.log.Error("scanRetry: search panicked",
						"download_id", rec.ID, "panic", r, "component", "monitor")
				}
			}()
			searchCtx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
			defer cancel()
			found = m.resolveRetrySource(searchCtx, &rec)
		}()

		if !found {
			m.log.Warn("scanRetry: no source found, skipping retry",
				"download_id", rec.ID, "artist", rec.Artist, "title", rec.Title, "component", "monitor")
			continue
		}

		// Increment retry count and set exponential backoff.
		// First retry: 2 min, then 4, 8, 16, 32, capped at 60 min.
		rec.RetryCount++
		backoffMin := 1 << rec.RetryCount // 2, 4, 8, 16, 32
		if backoffMin > 60 {
			backoffMin = 60
		}
		rec.RetryAfter = time.Now().UTC().Add(time.Duration(backoffMin) * time.Minute).Format(time.RFC3339)

		// Reset to queued — the main loop will pick it up with the (potentially
		// updated) source info on the next tick.
		rec.State = StateQueued
		rec.Error = ""
		rec.Progress = 0

		if err := m.store.Update(m.ctx, &rec); err != nil {
			m.log.Warn("scanRetry: update failed",
				"download_id", rec.ID, "error", err, "component", "monitor")
			continue
		}

		m.publishRecord(rec.ID, StateQueued, events.TopicDownloadStateChanged)
		retried++
	}

	if retried > 0 {
		m.log.Info("scanRetry: retried downloads", "count", retried, "component", "monitor")
	}
}

// resolveRetrySource searches all registered plugins for a download source
// matching the record's artist and title. Source fields (SourceName, Filename,
// Size, Bitrate, Format, Username) are updated from the best match.
// Returns true if a source was found, false if no results were returned.
func (m *MonitoringService) resolveRetrySource(ctx context.Context, rec *Record) bool {
	if rec.Artist == "" || rec.Title == "" {
		return false
	}

	orch := NewOrchestrator(m.registry, m.log)

	var profile *quality.QualityProfile
	if m.qualityProfileStore != nil {
		p, err := m.qualityProfileStore.LoadProfileByID(ctx, nil)
		if err != nil {
			m.log.Warn("resolveRetrySource: failed to load quality profile, using default",
				"download_id", rec.ID, "error", err, "component", "monitor")
		}
		profile = p
	}

	best, err := orch.FindBestMatch(ctx, rec.Title, rec.Artist, rec.Album, 0, "", profile)
	if err != nil {
		m.log.Warn("resolveRetrySource: search failed",
			"download_id", rec.ID, "artist", rec.Artist, "title", rec.Title,
			"error", err, "component", "monitor")
		return false
	}

	oldKey := rec.SourceName + "/" + rec.Filename
	newKey := best.SourceName + "/" + best.Track.Filename

	if newKey != oldKey {
		m.log.Info("resolveRetrySource: found alternative source",
			"download_id", rec.ID,
			"old_source", oldKey,
			"new_source", newKey,
			"component", "monitor",
		)
	}

	PopulateDownloadSource(rec, best.SourceName, best.Track)
	return true
}

// ---------------------------------------------------------------------------
// Orphan recovery
// ---------------------------------------------------------------------------

// recoverOrphans recovers download records left in non-terminal states after
// a previous run's shutdown (container restart, crash, etc.).
//
// Strategy per state:
//   - queued:        stay queued (main loop picks up)
//   - downloading:   transition to failed (monitoring goroutine died)
//   - importPending: re-publish TopicDownloadCompleted (file already on disk)
//   - importing:     transition to failed (import chain interrupted)
func (m *MonitoringService) recoverOrphans(ctx context.Context) {
	active, err := m.store.ListActive(ctx)
	if err != nil {
		m.log.Error("recoverOrphans: list active failed", "error", err, "component", "monitor")
		return
	}
	if len(active) == 0 {
		return
	}

	var downloading, importPending, importing int

	for _, rec := range active {
		switch rec.State {
		case StateDownloading:
			if ok, err := m.store.TransitionState(ctx, rec.ID, StateDownloading, StateFailed); err != nil {
				m.log.Error("recoverOrphans: downloading→failed transition failed",
					"download_id", rec.ID, "error", err, "component", "monitor")
				continue
			} else if !ok {
				m.log.Warn("recoverOrphans: downloading state changed, skipping",
					"download_id", rec.ID, "component", "monitor")
				continue
			}
			rec.State = StateFailed
			rec.Error = "download interrupted by server restart"
			if err := m.store.Update(ctx, &rec); err != nil {
				m.log.Warn("recoverOrphans: update error field failed",
					"download_id", rec.ID, "error", err, "component", "monitor")
			}
			m.bus.Publish(ctx, events.TopicDownloadFailed, &rec)
			downloading++

		case StateImportPending:
			// File is on disk — re-trigger the import chain via event.
			m.log.Info("recoverOrphans: re-triggering import",
				"download_id", rec.ID, "component", "monitor")
			m.bus.Publish(ctx, events.TopicDownloadCompleted, &rec)
			importPending++

		case StateImporting:
			if ok, err := m.store.TransitionState(ctx, rec.ID, StateImporting, StateFailed); err != nil {
				m.log.Error("recoverOrphans: importing→failed transition failed",
					"download_id", rec.ID, "error", err, "component", "monitor")
				continue
			} else if !ok {
				m.log.Warn("recoverOrphans: importing state changed, skipping",
					"download_id", rec.ID, "component", "monitor")
				continue
			}
			rec.State = StateFailed
			rec.Error = "import interrupted by server restart"
			if err := m.store.Update(ctx, &rec); err != nil {
				m.log.Warn("recoverOrphans: update error field failed",
					"download_id", rec.ID, "error", err, "component", "monitor")
			}
			m.bus.Publish(ctx, events.TopicDownloadFailed, &rec)
			importing++

		case StateFailedPending:
			m.log.Debug("recoverOrphans: skipping failedPending, handled by retry",
				"download_id", rec.ID, "component", "monitor")
		}
	}

	if downloading+importPending+importing > 0 {
		m.log.Info("recoverOrphans: state recovery complete",
			"downloading", downloading,
			"importPending", importPending,
			"importing", importing,
			"component", "monitor",
		)
	}
}

// ---------------------------------------------------------------------------
// Pending source resolution
// ---------------------------------------------------------------------------

// resolvePendingSources resolves all queued download records whose SourceName
// is "pending" (i.e., created via QueuePending without a known download
// source). For each record it searches for a matching download via the
// orchestrator, updates the record with resolved source fields, and leaves
// it queued for the main loop to pick up.
func (m *MonitoringService) resolvePendingSources(ctx context.Context) {
	queued, err := m.store.ListByState(ctx, StateQueued)
	if err != nil {
		m.log.Error("resolvePendingSources: list queued failed", "error", err, "component", "monitor")
		return
	}

	var pending []Record
	for _, rec := range queued {
		if rec.IsPendingSource() {
			pending = append(pending, rec)
		}
	}
	if len(pending) == 0 {
		return
	}

	m.log.Info("resolvePendingSources: resolving sources", "count", len(pending), "component", "monitor")
	orch := NewOrchestrator(m.registry, m.log)

	var profile *quality.QualityProfile
	if m.qualityProfileStore != nil {
		p, err := m.qualityProfileStore.LoadProfileByID(ctx, nil)
		if err != nil {
			m.log.Warn("resolvePendingSources: failed to load quality profile, using default",
				"error", err, "component", "monitor")
		}
		profile = p
	}

	resolved := 0

	for _, rec := range pending {
		if rec.State.Terminal() {
			continue
		}
		if rec.Artist == "" || rec.Title == "" {
			m.log.Warn("resolvePendingSources: missing artist/title",
				"download_id", rec.ID, "component", "monitor")
			continue
		}

		best, err := orch.FindBestMatch(ctx, rec.Title, rec.Artist, rec.Album, 0, "", profile)
		if err != nil {
			m.log.Warn("resolvePendingSources: search failed",
				"download_id", rec.ID, "artist", rec.Artist, "title", rec.Title, "error", err, "component", "monitor")
			rec.State = StateFailedPending
			rec.Error = err.Error()
			rec.RetryCount = 1
			rec.RetryAfter = time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
			_ = m.store.Update(ctx, &rec)
			continue
		}

		username := best.Track.Username
		if username == "" {
			username = best.SourceName
		}
		rec.SourceName = best.SourceName
		rec.Username = username
		rec.Filename = best.Track.Filename
		rec.Size = best.Track.Size
		rec.Bitrate = best.Track.Bitrate
		rec.Format = best.Track.Quality
		rec.DisplayName = rec.Artist + " - " + rec.Title

		if err := m.store.Update(ctx, &rec); err != nil {
			m.log.Error("resolvePendingSources: update failed",
				"download_id", rec.ID, "error", err, "component", "monitor")
			continue
		}
		resolved++
	}

	if resolved > 0 {
		m.log.Info("resolvePendingSources: done", "resolved", resolved, "component", "monitor")
	}
}
