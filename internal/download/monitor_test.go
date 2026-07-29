package download

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/events"
)

// ─── Wait helper ─────────────────────────────────────────────────────

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout waiting for condition")
}

// ─── Monitor test provider ───────────────────────────────────────────

// monitorTestProvider implements MonitoredProvider and Plugin for white-box
// testing of the MonitoringService download state machine.
type monitorTestProvider struct {
	mu   sync.Mutex
	name string

	records  map[string]*Record   // providerID → record
	progress map[string]*Progress // providerID → progress

	// Behavior control
	maxConcurrent int
	timeout       time.Duration
	startDelay    time.Duration // delay before goroutine sets DownloadImported
	failStart     bool          // if true, StartDownload returns error

	// Search behavior for retry tests
	searchResults []domain.TrackResult
	searchError   error

	// Capture the last StartDownload meta (used in TestStartSingleDownload_BuildsMetaFields)
	lastMeta Meta
}

// Compile-time interface checks.
var _ MonitoredProvider = (*monitorTestProvider)(nil)
var _ Plugin = (*monitorTestProvider)(nil)

func newMonitorTestProvider(name string) *monitorTestProvider {
	return &monitorTestProvider{
		name:          name,
		records:       make(map[string]*Record),
		progress:      make(map[string]*Progress),
		maxConcurrent: 2,
		timeout:       30 * time.Minute,
	}
}

// ─── MonitoredProvider methods ───────────────────────────────────────

func (p *monitorTestProvider) StartDownload(ctx context.Context, meta Meta) (string, error) {
	if p.failStart {
		return "", fmt.Errorf("start download failed")
	}

	p.mu.Lock()
	p.lastMeta = meta
	providerID := fmt.Sprintf("prov-%s-%d", meta.Filename, time.Now().UnixNano())
	rec := &Record{
		ID:       providerID,
		State:    StateDownloading,
		Filename: meta.Filename,
	}
	p.records[providerID] = rec
	p.mu.Unlock()

	// Optional: asynchronously mark as imported after delay.
	if p.startDelay > 0 {
		delay := p.startDelay
		go func() {
			time.Sleep(delay)
			p.mu.Lock()
			rec.State = StateImported
			rec.FilePath = "/tmp/" + meta.Filename
			rec.CoverURL = "http://cover.example.com/" + meta.Filename
			p.mu.Unlock()
		}()
	}

	return providerID, nil
}

func (p *monitorTestProvider) GetStatus(_ context.Context, providerID string) (*Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.records[providerID]
	if !ok {
		return nil, fmt.Errorf("provider download %q not found", providerID)
	}
	cp := *rec
	return &cp, nil
}

func (p *monitorTestProvider) GetProgress(_ context.Context, providerID string) (*Progress, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prog, ok := p.progress[providerID]
	if !ok {
		return nil, nil
	}
	cp := *prog
	return &cp, nil
}

func (p *monitorTestProvider) Cancel(_ context.Context, providerID string, remove bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if rec, ok := p.records[providerID]; ok {
		rec.State = StateIgnored
	}
	return nil
}

func (p *monitorTestProvider) ActiveDownloads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.records))
	for id := range p.records {
		ids = append(ids, id)
	}
	return ids
}

func (p *monitorTestProvider) MaxConcurrent() int             { return p.maxConcurrent }
func (p *monitorTestProvider) DownloadTimeout() time.Duration { return p.timeout }

// ─── Plugin methods ──────────────────────────────────────────────────

func (p *monitorTestProvider) Name() string                            { return p.name }
func (p *monitorTestProvider) DisplayName() string                     { return "Test Provider" }
func (p *monitorTestProvider) IsConfigured() bool                      { return true }
func (p *monitorTestProvider) CheckConnection(_ context.Context) error { return nil }
func (p *monitorTestProvider) Connected() bool                         { return true }
func (p *monitorTestProvider) CapabilityStatus() map[string]string {
	return map[string]string{"download": "connected"}
}

func (p *monitorTestProvider) Search(_ context.Context, _ string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	if p.searchError != nil {
		return nil, nil, p.searchError
	}
	return p.searchResults, nil, nil
}

// ─── Test helpers ────────────────────────────────────────────────────

// hasEvent returns true if the mock bus published at least one event with
// the given topic and a download record whose ID matches.
func hasEvent(t *testing.T, bus *mockBus, topic, downloadID string) bool {
	t.Helper()
	for _, ev := range bus.published() {
		if ev.Topic != topic {
			continue
		}
		if rec, ok := ev.Event.(*Record); ok && rec.ID == downloadID {
			return true
		}
	}
	return false
}

// ─── Tests ───────────────────────────────────────────────────────────

// TestStartQueuedDownloads_PicksUpQueued verifies that startQueuedDownloads
// scans the store for queued records, transitions them to downloading, and
// tracks them via the provider.
func TestStartQueuedDownloads_PicksUpQueued(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	rec := &Record{
		ID:         "dl-1",
		SourceName: "testsource",
		Filename:   "track.flac",
		State:      StateQueued,
		Artist:     "a-ha",
		Title:      "Take on Me",
	}
	if err := store.Insert(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	prov := newMonitorTestProvider("testsource")
	if err := reg.Register(prov); err != nil {
		t.Fatal(err)
	}

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.startQueuedDownloads()

	fresh, err := store.Get(context.Background(), "dl-1")
	if err != nil {
		t.Fatal(err)
	}
	if fresh == nil {
		t.Fatal("record not found in store")
	}
	if fresh.State != StateDownloading {
		t.Errorf("expected state downloading, got %s", fresh.State)
	}

	active := prov.ActiveDownloads()
	if len(active) != 1 {
		t.Fatalf("expected 1 active download on provider, got %d", len(active))
	}
}

// TestStartSingleDownload_BuildsMetaFields verifies that startSingleDownload
// passes a correctly-populated Meta to StartDownload. The Filename
// field must not be empty (regression test for the bug where meta.Filename
// was left blank).
func TestStartSingleDownload_BuildsMetaFields(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	rec := &Record{
		ID:         "dl-2",
		SourceName: "testsource",
		Filename:   "song.flac",
		Username:   "peer123",
		Size:       9876543,
		Artist:     "TestBand",
		Album:      "TestAlbum",
		Title:      "TestSong",
		Bitrate:    320,
		Format:     "flac",
		State:      StateQueued,
	}
	if err := store.Insert(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	prov := newMonitorTestProvider("testsource")
	if err := reg.Register(prov); err != nil {
		t.Fatal(err)
	}

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.startSingleDownload(rec)

	prov.mu.Lock()
	meta := prov.lastMeta
	prov.mu.Unlock()

	if meta.Filename == "" {
		t.Error("BUG REGRESSION: meta.Filename is empty — StartDownload received blank filename")
	}
	if meta.Filename != "song.flac" {
		t.Errorf("expected Filename=song.flac, got %q", meta.Filename)
	}
	if meta.Username != "peer123" {
		t.Errorf("expected Username=peer123, got %q", meta.Username)
	}
	if meta.Size != 9876543 {
		t.Errorf("expected Size=9876543, got %d", meta.Size)
	}
	if meta.Artist != "TestBand" {
		t.Errorf("expected Artist=TestBand, got %q", meta.Artist)
	}
	if meta.Title != "TestSong" {
		t.Errorf("expected Title=TestSong, got %q", meta.Title)
	}
}

// TestStartSingleDownload_ConcurrencyLimit verifies that when a provider
// reaches MaxConcurrent, additional downloads are skipped (pool full).
func TestStartSingleDownload_ConcurrencyLimit(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	prov := newMonitorTestProvider("limited")
	prov.maxConcurrent = 1

	reg := NewRegistry()
	if err := reg.Register(prov); err != nil {
		t.Fatal(err)
	}

	rec1 := &Record{
		ID: "dl-a", SourceName: "limited", Filename: "a.flac",
		State: StateQueued,
	}
	rec2 := &Record{
		ID: "dl-b", SourceName: "limited", Filename: "b.flac",
		State: StateQueued,
	}
	store.Insert(context.Background(), rec1)
	store.Insert(context.Background(), rec2)

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.startQueuedDownloads()

	freshA, _ := store.Get(context.Background(), "dl-a")
	freshB, _ := store.Get(context.Background(), "dl-b")

	downloading := 0
	queued := 0
	for _, r := range []*Record{freshA, freshB} {
		switch r.State {
		case StateDownloading:
			downloading++
		case StateQueued:
			queued++
		}
	}
	if downloading != 1 || queued != 1 {
		t.Errorf("expected 1 downloading + 1 queued, got %d downloading + %d queued", downloading, queued)
	}
}

// TestPollSingle_DownloadImported_TransitionAndCoverSync verifies that
// pollSingle transitions a completed download to importPending, syncs
// FilePath and CoverURL, and publishes a TopicDownloadCompleted event.
func TestPollSingle_DownloadImported_TransitionAndCoverSync(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	storeRec := &Record{
		ID:         "dl-imported",
		SourceName: "testsource",
		State:      StateDownloading,
	}
	if err := store.Insert(context.Background(), storeRec); err != nil {
		t.Fatal(err)
	}

	prov := newMonitorTestProvider("testsource")
	// Set up provider-side record as imported with file path and cover URL.
	prov.mu.Lock()
	prov.records["prov-imported"] = &Record{
		ID:       "prov-imported",
		State:    StateImported,
		FilePath: "/tmp/song.flac",
		CoverURL: "http://cover.example.com/song.jpg",
	}
	prov.mu.Unlock()

	reg := NewRegistry()
	reg.Register(prov)

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())

	// Manually add tracking so pollSingle can find the download.
	svc.addMapping("dl-imported", "prov-imported", "testsource",
		time.Now(), time.Now().Add(time.Hour))

	// Build the monitoredDownload that pollSingle expects.
	md := &monitoredDownload{
		recordID:   "dl-imported",
		providerID: "prov-imported",
		pluginName: "testsource",
		startedAt:  time.Now(),
		deadline:   time.Now().Add(time.Hour),
	}
	svc.pollSingle(md)

	fresh, err := store.Get(context.Background(), "dl-imported")
	if err != nil {
		t.Fatal(err)
	}
	if fresh == nil {
		t.Fatal("record not found after poll")
	}
	if fresh.State != StateImportPending {
		t.Errorf("expected importPending, got %s", fresh.State)
	}
	if fresh.CoverURL != "http://cover.example.com/song.jpg" {
		t.Errorf("expected CoverURL synced, got %q", fresh.CoverURL)
	}
	if fresh.FilePath != "/tmp/song.flac" {
		t.Errorf("expected FilePath synced, got %q", fresh.FilePath)
	}
	if !hasEvent(t, bus, events.TopicDownloadCompleted, "dl-imported") {
		t.Error("TopicDownloadCompleted event not published")
	}
}

// TestPollSingle_DownloadFailed verifies that when the provider reports
// a failed download, the record transitions to failed and a
// TopicDownloadFailed event is published.
func TestPollSingle_DownloadFailed(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	storeRec := &Record{
		ID:         "dl-fail",
		SourceName: "testsource",
		State:      StateDownloading,
	}
	store.Insert(context.Background(), storeRec)

	prov := newMonitorTestProvider("testsource")
	prov.mu.Lock()
	prov.records["prov-fail"] = &Record{
		ID:    "prov-fail",
		State: StateFailed,
		Error: "peer offline",
	}
	prov.mu.Unlock()

	reg := NewRegistry()
	reg.Register(prov)

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.addMapping("dl-fail", "prov-fail", "testsource",
		time.Now(), time.Now().Add(time.Hour))

	md := &monitoredDownload{
		recordID:   "dl-fail",
		providerID: "prov-fail",
		pluginName: "testsource",
		startedAt:  time.Now(),
		deadline:   time.Now().Add(time.Hour),
	}
	svc.pollSingle(md)

	fresh, _ := store.Get(context.Background(), "dl-fail")
	if fresh == nil {
		t.Fatal("record not found")
	}
	if fresh.State != StateFailed {
		t.Errorf("expected failed, got %s", fresh.State)
	}
	if fresh.Error != "peer offline" {
		t.Errorf("expected Error='peer offline', got %q", fresh.Error)
	}
	if !hasEvent(t, bus, events.TopicDownloadFailed, "dl-fail") {
		t.Error("TopicDownloadFailed event not published")
	}
}

// TestPollSingle_Timeout verifies that pollSingle fails a download whose
// deadline has elapsed, even when the provider hasn't reported a terminal
// state yet.
func TestPollSingle_Timeout(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	storeRec := &Record{
		ID:         "dl-timeout",
		SourceName: "testsource",
		State:      StateDownloading,
	}
	store.Insert(context.Background(), storeRec)

	reg := NewRegistry()
	// Provider with short timeout — deadline set in the past on mapping.
	prov := newMonitorTestProvider("testsource")
	prov.timeout = 10 * time.Millisecond
	reg.Register(prov)

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	// Map with deadline already expired.
	svc.addMapping("dl-timeout", "prov-timeout", "testsource",
		time.Now(), time.Now().Add(-1*time.Second))

	md := &monitoredDownload{
		recordID:   "dl-timeout",
		providerID: "prov-timeout",
		pluginName: "testsource",
		startedAt:  time.Now(),
		deadline:   time.Now().Add(-1 * time.Second),
	}
	svc.pollSingle(md)

	fresh, _ := store.Get(context.Background(), "dl-timeout")
	if fresh == nil {
		t.Fatal("record not found")
	}
	if fresh.State != StateFailed {
		t.Errorf("expected failed, got %s", fresh.State)
	}
	if fresh.Error == "" {
		t.Error("expected timeout error message, got empty")
	}
	if !hasEvent(t, bus, events.TopicDownloadFailed, "dl-timeout") {
		t.Error("TopicDownloadFailed event not published")
	}
}

// TestCheckCancellations_StopsTracking verifies that checkCancellations
// detects when the store record has been externally transitioned to ignored
// and removes it from active tracking, releasing the concurrency semaphore.
func TestCheckCancellations_StopsTracking(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	storeRec := &Record{
		ID:         "dl-cancel",
		SourceName: "testsource",
		State:      StateDownloading,
	}
	store.Insert(context.Background(), storeRec)

	prov := newMonitorTestProvider("testsource")
	prov.maxConcurrent = 1
	reg := NewRegistry()
	reg.Register(prov)

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())

	// Acquire semaphore (simulating what startSingleDownload does).
	sem := svc.getSemaphore("testsource", 1)
	sem <- struct{}{}

	svc.addMapping("dl-cancel", "prov-cancel", "testsource",
		time.Now(), time.Now().Add(time.Hour))

	// Externally cancel the record.
	if ok, err := store.TransitionState(context.Background(), "dl-cancel",
		StateDownloading, StateIgnored); err != nil || !ok {
		t.Fatal("failed to transition to ignored")
	}

	svc.checkCancellations()

	// Confirm no longer tracked.
	svc.activeMu.Lock()
	_, tracking := svc.active["dl-cancel"]
	svc.activeMu.Unlock()
	if tracking {
		t.Error("download still tracked after external cancellation")
	}

	// Confirm semaphore released.
	select {
	case sem <- struct{}{}:
		// Success — semaphore had a free slot.
		<-sem // drain it back
	default:
		t.Error("semaphore not released after cancellation")
	}
}

// TestScanRetry_CrossProviderSearch verifies that scanRetry searches all
// registered providers for an alternative download source, updates the
// record to use the new source, re-queues it, and applies exponential
// backoff.
func TestScanRetry_CrossProviderSearch(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	failedRec := &Record{
		ID:         "dl-retry",
		SourceName: "soulseek",
		Filename:   "oldfile.flac",
		State:      StateFailed,
		Artist:     "a-ha",
		Title:      "Take on Me",
	}
	store.Insert(context.Background(), failedRec)

	// Register a different provider that will be found via search.
	altProv := newMonitorTestProvider("deezer")
	altProv.searchResults = []domain.TrackResult{{
		Artist: "a-ha",
		Title:  "Take on Me",
		SearchResult: domain.SearchResult{
			Filename: "newfile.flac",
			Size:     31415926,
			Bitrate:  320,
			Quality:  "mp3",
			Username: "deezer_alt",
		},
	}}

	reg := NewRegistry()
	// Also register the original provider (needed for scanRetry to skip
	// records whose SourceName still has a valid plugin).
	origProv := newMonitorTestProvider("soulseek")
	reg.Register(origProv)
	reg.Register(altProv)

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.scanRetry()

	fresh, _ := store.Get(context.Background(), "dl-retry")
	if fresh == nil {
		t.Fatal("record not found")
	}
	if fresh.SourceName != "deezer" {
		t.Errorf("expected SourceName=deezer after cross-provider search, got %q", fresh.SourceName)
	}
	if fresh.State != StateQueued {
		t.Errorf("expected state queued, got %s", fresh.State)
	}
	if fresh.RetryCount != 1 {
		t.Errorf("expected RetryCount=1, got %d", fresh.RetryCount)
	}
	if fresh.RetryAfter == "" {
		t.Error("expected RetryAfter to be set with backoff")
	}
	if fresh.Filename != "newfile.flac" {
		t.Errorf("expected Filename from new source, got %q", fresh.Filename)
	}
}

// TestScanRetry_NoSourceFound_Skips verifies that when no alternative
// download source can be found, the record is NOT re-queued and stays in
// the failed state.
func TestScanRetry_NoSourceFound_Skips(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	failedRec := &Record{
		ID:         "dl-nosource",
		SourceName: "soulseek",
		Filename:   "unknown.flac",
		State:      StateFailed,
		Artist:     "obscure",
		Title:      "nobody has this",
	}
	store.Insert(context.Background(), failedRec)

	// Register a provider that returns no search results.
	emptyProv := newMonitorTestProvider("soulseek")
	emptyProv.searchResults = nil // empty — orchestrator finds no match

	reg := NewRegistry()
	reg.Register(emptyProv)

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.scanRetry()

	fresh, _ := store.Get(context.Background(), "dl-nosource")
	if fresh == nil {
		t.Fatal("record not found")
	}
	if fresh.State != StateFailed {
		t.Errorf("expected state to remain failed, got %s", fresh.State)
	}
	if fresh.RetryCount != 0 {
		t.Error("RetryCount should not have been incremented when no source found")
	}
}

// TestScanRetry_MaxRetries_Exhausted verifies that records at MaxRetries
// are skipped by scanRetry and not re-queued.
func TestScanRetry_MaxRetries_Exhausted(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	exhaustedRec := &Record{
		ID:         "dl-exhausted",
		SourceName: "soulseek",
		Filename:   "stale.flac",
		State:      StateFailed,
		Artist:     "a-ha",
		Title:      "Take on Me",
		RetryCount: MaxRetries, // 5
	}
	store.Insert(context.Background(), exhaustedRec)

	reg := NewRegistry()
	reg.Register(newMonitorTestProvider("soulseek"))

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.scanRetry()

	fresh, _ := store.Get(context.Background(), "dl-exhausted")
	if fresh == nil {
		t.Fatal("record not found")
	}
	if fresh.State != StateFailed {
		t.Errorf("expected state to remain failed, got %s", fresh.State)
	}
}

// TestRecoverOrphans_DownloadingToFailed verifies that recoverOrphans
// transitions abandoned downloading records to failed with an
// "interrupted" error message.
func TestRecoverOrphans_DownloadingToFailed(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	orphanRec := &Record{
		ID:         "dl-orphan",
		SourceName: "soulseek",
		State:      StateDownloading,
	}
	store.Insert(context.Background(), orphanRec)

	reg := NewRegistry()
	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.recoverOrphans(context.Background())

	fresh, _ := store.Get(context.Background(), "dl-orphan")
	if fresh == nil {
		t.Fatal("record not found")
	}
	if fresh.State != StateFailed {
		t.Errorf("expected state failed, got %s", fresh.State)
	}
	if fresh.Error != "download interrupted by server restart" {
		t.Errorf("expected interrupted error, got %q", fresh.Error)
	}
	if !hasEvent(t, bus, events.TopicDownloadFailed, "dl-orphan") {
		t.Error("TopicDownloadFailed event not published")
	}
}

// TestRecoverOrphans_ImportPending_ReTrigger verifies that recoverOrphans
// re-publishes TopicDownloadCompleted for records stuck in importPending
// state, so the import chain picks up the file already on disk.
func TestRecoverOrphans_ImportPending_ReTrigger(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	importPendingRec := &Record{
		ID:         "dl-reimport",
		SourceName: "soulseek",
		State:      StateImportPending,
		FilePath:   "/tmp/song.flac",
	}
	store.Insert(context.Background(), importPendingRec)

	reg := NewRegistry()
	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())
	svc.recoverOrphans(context.Background())

	// State should remain importPending (not modified by recovery).
	fresh, _ := store.Get(context.Background(), "dl-reimport")
	if fresh == nil {
		t.Fatal("record not found")
	}
	if fresh.State != StateImportPending {
		t.Errorf("expected state to remain importPending, got %s", fresh.State)
	}
	if !hasEvent(t, bus, events.TopicDownloadCompleted, "dl-reimport") {
		t.Error("TopicDownloadCompleted not re-published for importPending recovery")
	}
}

// TestResolveRetrySource_PopulatesFields verifies that resolveRetrySource
// updates all relevant source fields on the record based on the best match
// returned by the orchestrator.
func TestResolveRetrySource_PopulatesFields(t *testing.T) {
	store := newMockStore()
	bus := newMockBus()

	rec := Record{
		ID:         "dl-resolve",
		SourceName: "oldsource",
		Filename:   "old.flac",
		State:      StateFailed,
		Artist:     "TestArtist",
		Title:      "TestTrack",
	}
	store.Insert(context.Background(), &rec)

	prov := newMonitorTestProvider("newsource")
	prov.searchResults = []domain.TrackResult{{
		Artist: "TestArtist",
		Title:  "TestTrack",
		SearchResult: domain.SearchResult{
			Filename: "resolved.flac",
			Size:     12345678,
			Bitrate:  320,
			Quality:  "flac",
			Username: "peer42",
		},
	}}

	reg := NewRegistry()
	reg.Register(prov)

	svc := NewMonitoringService(store, reg, nil, "", bus, testLogger())

	found := svc.resolveRetrySource(context.Background(), &rec)
	if !found {
		t.Fatal("expected resolveRetrySource to find a source")
	}

	if rec.SourceName != "newsource" {
		t.Errorf("expected SourceName=newsource, got %q", rec.SourceName)
	}
	if rec.Filename != "resolved.flac" {
		t.Errorf("expected Filename=resolved.flac, got %q", rec.Filename)
	}
	if rec.Size != 12345678 {
		t.Errorf("expected Size=12345678, got %d", rec.Size)
	}
	if rec.Bitrate != 320 {
		t.Errorf("expected Bitrate=320, got %d", rec.Bitrate)
	}
	if rec.Format != "flac" {
		t.Errorf("expected Format=flac, got %q", rec.Format)
	}
	if rec.Username != "peer42" {
		t.Errorf("expected Username=peer42, got %q", rec.Username)
	}
}
