// Package download provides download management and orchestration.
package download

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ramonskie/groovearr/internal/events"
	"github.com/ramonskie/groovearr/internal/quality"
)

const (
	monitorPollInterval        = 1 * time.Second
	monitorRetryInterval       = 5 * time.Second
	monitorRetryTickCount      = 5  // run retry scan every N ticks
	monitorPendingResolveTicks = 10 // run pending-source resolution every N ticks
)

// monitoredDownload tracks a single download being driven by the monitoring service.
type monitoredDownload struct {
	recordID   string // groovearr download ID (store key)
	providerID string // provider-managed download ID (returned by StartDownload)
	pluginName string // plugin canonical name for provider lookup
	downloadClientName string // if set, poll via DownloadClient instead of MonitoredProvider
	startedAt  time.Time
	deadline   time.Time
}

// MonitoringService drives the download state machine by polling MonitoredProvider
// plugins. It replaces the worker pool, dispatcher, and retry worker with a
// single ticker-based loop running at 1-second intervals.
//
// Lifecycle:
//   - NewMonitoringService → create
//   - Start(ctx) → recover orphans, resolve pending sources, launch main loop
//   - Shutdown() → cancel context, wait for goroutine exit
type MonitoringService struct {
	log      *slog.Logger
	store    Store
	registry *Registry
	bus      events.IEventAggregator

	// downloadClients provides DownloadClient lookup for album-level downloads.
	downloadClients *DownloadClientRegistry

	// downloadBasePath is the configured download staging directory.
	// Prefer live config getter if set; falls back to static value.
	downloadBasePath     string
	downloadBasePathFunc func() string

	// qualityProfileStore optionally supplies quality profiles for
	// auto-retry source resolution. When nil, all sources are equally
	// eligible (default profile applied by Orchestrator).
	qualityProfileStore quality.ProfileStore

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// active maps groovearr download IDs to their provider tracking info.
	activeMu sync.Mutex
	active   map[string]*monitoredDownload

	// providerToGroovearr is the reverse mapping: provider ID → groovearr ID.
	// Used when ActiveDownloads returns provider IDs we need to cross-reference.
	providerMu          sync.Mutex
	providerToGroovearr map[string]string

	// per-plugin concurrency semaphores (buffered channels).
	semMu      sync.Mutex
	semaphores map[string]chan struct{}

	// ticksSinceRetry counts main-loop ticks for periodic retry scanning.
	ticksSinceRetry int

	// ticksSincePendingResolve counts ticks for periodic pending-source resolution.
	ticksSincePendingResolve int

	// pendingResolveRunning prevents concurrent resolvePendingSources calls.
	pendingResolveRunning atomic.Bool
}

// NewMonitoringService creates a MonitoringService that accepts its dependencies
// through the constructor: store for persistence, registry for plugin lookup,
// bus for event publishing, and logger for structured output.
func NewMonitoringService(
	store Store,
	registry *Registry,
	downloadClients *DownloadClientRegistry,
	downloadBasePath string,
	bus events.IEventAggregator,
	logger *slog.Logger,
) *MonitoringService {
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MonitoringService{
		log:              logger,
		store:            store,
		registry:         registry,
		downloadClients:  downloadClients,
		downloadBasePath: downloadBasePath,
		bus:              bus,
		ctx:              ctx,
		cancel:           cancel,
		active:              make(map[string]*monitoredDownload),
		providerToGroovearr: make(map[string]string),
		semaphores:          make(map[string]chan struct{}),
	}
}

// SetDownloadPathFunc provides a live config getter for the download path.
// When set, downloadPath() uses this instead of the static base path.
func (m *MonitoringService) SetDownloadPathFunc(fn func() string) {
	m.downloadBasePathFunc = fn
}

// SetQualityProfileStore sets the quality profile store for retry source
// resolution. When set, the monitor applies user-configured quality preferences
// (bitrate minimums, format preferences, etc.) when searching for alternative
// sources during automatic retries.
func (m *MonitoringService) SetQualityProfileStore(store quality.ProfileStore) {
	m.qualityProfileStore = store
}

// Start recovers orphaned downloads from a previous run, resolves pending
// source records, and launches the main polling goroutine. The provided ctx
// is used only for the initial recovery and resolution — the main loop uses
// the service's internal context, which is cancelled by Shutdown.
func (m *MonitoringService) Start(ctx context.Context) {
	m.recoverOrphans(ctx)
	if m.pendingResolveRunning.CompareAndSwap(false, true) {
		go func() {
			defer m.pendingResolveRunning.Store(false)
			m.resolvePendingSources(m.ctx)
		}()
	}
	m.wg.Add(1)
	go m.run()
	m.log.Info("monitoring service started", "component", "monitor")
}

// Shutdown gracefully stops the main loop: cancels the internal context and
// waits for the polling goroutine to exit. Active downloads are not cancelled
// — the provider continues managing them until completion or external cancel.
func (m *MonitoringService) Shutdown() {
	m.log.Info("monitoring service shutdown started", "component", "monitor")
	m.cancel()
	m.wg.Wait()
	m.log.Info("monitoring service shutdown complete", "component", "monitor")
}

// ---------------------------------------------------------------------------
// Main loop
// ---------------------------------------------------------------------------

// run is the main polling goroutine. It ticks at monitorPollInterval (1s)
// and orchestrates: queued-record scanning, active-download polling,
// cancellation detection, ActiveDownloads provider sync, and periodic retry.
// Each tick is individually recoverable — a panic or hang in one tick never
// kills the monitor.
func (m *MonitoringService) run() {
	defer m.wg.Done()

	ticker := time.NewTicker(monitorPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			m.log.Info("monitor loop stopped", "component", "monitor")
			return
		case <-ticker.C:
			m.safeTick()
		}
	}
}

// safeTick wraps tick() with panic recovery and a 30s timeout so a single
// stuck HTTP call or provider hang never blocks the monitor indefinitely.
// (Goroutines launched by tick — e.g. resolvePendingSources — use m.ctx
// and are not bound by this timeout.)
func (m *MonitoringService) safeTick() {
	done := make(chan struct{}, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.log.Error("monitor tick panic recovered", "panic", r, "component", "monitor")
			}
			done <- struct{}{}
		}()
		m.tick()
	}()

	select {
	case <-done:
		// normal completion
	case <-time.After(30 * time.Second):
		m.log.Error("monitor tick timed out after 30s — provider may be hung", "component", "monitor")
	}
}

// tick performs one cycle of the monitoring loop.
func (m *MonitoringService) tick() {
	m.syncFromProviders()
	m.startQueuedDownloads()
	m.pollActiveDownloads()
	m.checkCancellations()

	m.ticksSinceRetry++
	if m.ticksSinceRetry >= monitorRetryTickCount {
		m.ticksSinceRetry = 0
		m.scanRetry()
	}

	m.ticksSincePendingResolve++
	if m.ticksSincePendingResolve >= monitorPendingResolveTicks {
		m.ticksSincePendingResolve = 0
		if m.pendingResolveRunning.CompareAndSwap(false, true) {
			go func() {
				defer m.pendingResolveRunning.Store(false)
				m.resolvePendingSources(m.ctx)
			}()
		}
	}
}

// ---------------------------------------------------------------------------
// Provider sync (ActiveDownloads)
// ---------------------------------------------------------------------------

// syncFromProviders calls ActiveDownloads() on every MonitoredProvider in
// the registry. For provider-tracked IDs that are unknown to us, it queries
// GetStatus to attempt to match them to groovearr records. For tracked
// downloads whose provider IDs have disappeared from ActiveDownloads, it
// assumes the provider cleaned up and removes our tracking.
func (m *MonitoringService) syncFromProviders() {
	for _, p := range m.registry.All() {
		mp, ok := p.(MonitoredProvider)
		if !ok {
			continue
		}

		providerIDs := mp.ActiveDownloads()
		seen := make(map[string]bool, len(providerIDs))
		for _, pid := range providerIDs {
			seen[pid] = true
			m.providerMu.Lock()
			_, known := m.providerToGroovearr[pid]
			m.providerMu.Unlock()

			if !known {
				// Provider has a download we didn't start. This is normal if the
				// provider has downloads from outside the monitor (e.g., another
				// process, direct slskd interaction, or a provider restart).
				m.log.Debug("syncFromProviders: unknown provider download, skipping (normal if provider has external downloads)",
					"provider_id", pid, "plugin", p.Name(), "component", "monitor")
			}
		}

		// Clean up: any groovearr downloads for this plugin whose provider
		// ID is no longer in ActiveDownloads should be removed from tracking
		// (the provider considered them done). If the provider lost the download
		// (e.g., restart), transition the store record to failed so the retry
		// system picks it up.
		m.activeMu.Lock()
		var lost []*monitoredDownload
		for gid, md := range m.active {
			if md.pluginName != p.Name() {
				continue
			}
			if !seen[md.providerID] {
				lost = append(lost, md)
				m.removeTrackingLocked(gid, md.providerID)
			}
		}
		m.activeMu.Unlock()

		for _, md := range lost {
			m.log.Warn("provider lost download, marking as failed",
				"download_id", md.recordID, "provider_id", md.providerID,
				"plugin", p.Name(), "component", "monitor")
			m.failRecord(md.recordID, StateDownloading,
				fmt.Sprintf("provider %s lost download (restart or cleanup)", p.Name()))
			m.releaseSemaphore(md.pluginName)
		}
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// addMapping registers a new provider download mapping and adds the download
// to the active tracking set.
func (m *MonitoringService) addMapping(groovearrID, providerID, pluginName string, startedAt, deadline time.Time) {
	m.activeMu.Lock()
	m.active[groovearrID] = &monitoredDownload{
		recordID:   groovearrID,
		providerID: providerID,
		pluginName: pluginName,
		startedAt:  startedAt,
		deadline:   deadline,
	}
	m.activeMu.Unlock()

	m.providerMu.Lock()
	m.providerToGroovearr[providerID] = groovearrID
	m.providerMu.Unlock()
}

// addDownloadClientMapping registers an album download tracked via a DownloadClient.
func (m *MonitoringService) addDownloadClientMapping(groovearrID, providerID, clientName string, startedAt, deadline time.Time) {
	m.activeMu.Lock()
	m.active[groovearrID] = &monitoredDownload{
		recordID:           groovearrID,
		providerID:         providerID,
		pluginName:         clientName,
		downloadClientName: clientName,
		startedAt:          startedAt,
		deadline:           deadline,
	}
	m.activeMu.Unlock()

	m.providerMu.Lock()
	m.providerToGroovearr[providerID] = groovearrID
	m.providerMu.Unlock()
}

// removeTracking removes a download from the active set and provider mapping.
// Must be called without holding activeMu (acquires it internally).
func (m *MonitoringService) removeTracking(groovearrID, providerID string) {
	m.activeMu.Lock()
	m.removeTrackingLocked(groovearrID, providerID)
	m.activeMu.Unlock()
}

// removeTrackingLocked is the internal version that assumes activeMu is held.
// ProviderMu is acquired internally — callers must not hold providerMu.
func (m *MonitoringService) removeTrackingLocked(groovearrID, providerID string) {
	delete(m.active, groovearrID)
	m.providerMu.Lock()
	delete(m.providerToGroovearr, providerID)
	m.providerMu.Unlock()
}

// getSemaphore returns or lazily creates a buffered channel for per-plugin
// concurrency control. Capacity is max (MaxConcurrent).
func (m *MonitoringService) getSemaphore(pluginName string, max int) chan struct{} {
	m.semMu.Lock()
	defer m.semMu.Unlock()
	if sem, ok := m.semaphores[pluginName]; ok {
		return sem
	}
	sem := make(chan struct{}, max)
	m.semaphores[pluginName] = sem
	return sem
}

// releaseSemaphore non-blockingly releases a concurrency slot for the plugin.
// Safe to call even when no slot was acquired.
func (m *MonitoringService) releaseSemaphore(pluginName string) {
	m.semMu.Lock()
	defer m.semMu.Unlock()
	sem, ok := m.semaphores[pluginName]
	if !ok {
		return
	}
	select {
	case <-sem:
	default:
	}
}

// publishRecord fires a lifecycle event with a minimal download record.
func (m *MonitoringService) publishRecord(downloadID string, state State, topic string) {
	m.bus.Publish(m.ctx, topic, &Record{
		ID:    downloadID,
		State: state,
	})
}

// fireProgress publishes a TopicDownloadProgress event with live transfer data.
func (m *MonitoringService) fireProgress(downloadID string, progress float64, transferred, total, speed int64) {
	m.bus.Publish(m.ctx, events.TopicDownloadProgress, &Record{
		ID:          downloadID,
		State:       StateDownloading,
		Progress:    progress,
		Transferred: transferred,
		Size:        total,
		Speed:       speed,
	})
}
