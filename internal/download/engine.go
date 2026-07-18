// Package download provides the download engine and orchestrator for the MVP.
package download

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/matching"
)

// Engine manages download state across all sources.
type Engine struct {
	mu      sync.RWMutex
	records map[string]*domain.DownloadRecord // downloadID → record
}

// NewEngine creates a download engine.
func NewEngine() *Engine {
	return &Engine{
		records: make(map[string]*domain.DownloadRecord),
	}
}

// Start creates a new download record and spawns the download in a goroutine.
// fn receives the record pointer for progress updates.
func (e *Engine) Start(sourceName, filename, displayName string, fn func(record *domain.DownloadRecord)) string {
	id := fmt.Sprintf("%s-%d", sourceName, time.Now().UnixNano())

	record := &domain.DownloadRecord{
		ID:          id,
		SourceName:  sourceName,
		Filename:    filename,
		DisplayName: displayName,
		State:       domain.DownloadDownloading,
	}

	e.mu.Lock()
	e.records[id] = record
	e.mu.Unlock()

	go func() {
		fn(record)
		// If fn didn't set a terminal state, mark errored.
		e.mu.RLock()
		state := record.State
		e.mu.RUnlock()
		if !state.Terminal() {
			e.Update(id, func(r *domain.DownloadRecord) {
				if !r.State.Terminal() {
					r.State = domain.DownloadSucceeded
					r.Progress = 100.0
				}
			})
		}
	}()

	return id
}

// Update atomically modifies a download record.
func (e *Engine) Update(id string, fn func(r *domain.DownloadRecord)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.records[id]; ok {
		fn(r)
	}
}

// Get returns a copy of a download record.
func (e *Engine) Get(id string) *domain.DownloadRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if r, ok := e.records[id]; ok {
		cp := *r
		return &cp
	}
	return nil
}

// List returns all download records.
func (e *Engine) List() []domain.DownloadRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]domain.DownloadRecord, 0, len(e.records))
	for _, r := range e.records {
		out = append(out, *r)
	}
	return out
}

// Cancel marks a download as cancelled. No-op if already terminal.
func (e *Engine) Cancel(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.records[id]
	if !ok {
		return false
	}
	if !r.State.Terminal() {
		r.State = domain.DownloadCancelled
	}
	return true
}

// ClearCompleted removes terminal-state downloads.
func (e *Engine) ClearCompleted() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, r := range e.records {
		if r.State.Terminal() {
			delete(e.records, id)
		}
	}
}

// ─── Orchestrator ───────────────────────────────────────────────────

// Orchestrator routes search and download to configured plugins.
type Orchestrator struct {
	registry     *Registry
	matcher      *matching.Engine
	pathOverride map[string]string // downloadID → corrected file path
	mu           sync.RWMutex
}

// NewOrchestrator creates an orchestrator with the given plugin registry.
func NewOrchestrator(registry *Registry) *Orchestrator {
	return &Orchestrator{
		registry:     registry,
		matcher:      matching.New(),
		pathOverride: make(map[string]string),
	}
}

// Registry returns the plugin registry.
func (o *Orchestrator) Registry() *Registry { return o.registry }

// Search queries a specific source or falls back through configured plugins.
// source: "soulseek", "deezer", "hybrid" (try all), or "" (first configured).
func (o *Orchestrator) Search(ctx context.Context, source, query string) ([]domain.TrackResult, []domain.AlbumResult, error) {
	if source != "" && source != "hybrid" {
		p := o.registry.Get(source)
		if p == nil {
			return nil, nil, fmt.Errorf("source %q not found", source)
		}
		if !p.IsConfigured() {
			return nil, nil, fmt.Errorf("source %q not configured", source)
		}
		return p.Search(ctx, query)
	}

	plugins := o.registry.Configured()
	if len(plugins) == 0 {
		return nil, nil, fmt.Errorf("no download sources configured")
	}

	// Single source (not hybrid) — return first configured.
	if source != "hybrid" {
		return plugins[0].Search(ctx, query)
	}

	// Hybrid: try each source, merge results.
	var allTracks []domain.TrackResult
	var allAlbums []domain.AlbumResult
	for _, p := range plugins {
		tracks, albums, err := p.Search(ctx, query)
		if err != nil {
			continue
		}
		allTracks = append(allTracks, tracks...)
		allAlbums = append(allAlbums, albums...)
	}
	return allTracks, allAlbums, nil
}

// Download starts a download on the given source plugin.
func (o *Orchestrator) Download(ctx context.Context, sourceName, username, filename string, fileSize int64) (string, error) {
	p := o.registry.Get(sourceName)
	if p == nil {
		return "", fmt.Errorf("source %q not found", sourceName)
	}
	return p.Download(ctx, username, filename, fileSize)
}

// DownloadBest searches all configured sources (except excludeSource) for a track
// matching the given metadata, scores candidates via the matching engine, and
// downloads the best match above the confidence threshold.
// Returns the download ID, the source used, and the confidence score.
func (o *Orchestrator) DownloadBest(ctx context.Context, title, artist string, durationMs int64, excludeSource string) (downloadID string, sourceName string, confidence float64, err error) {
	const minConfidence = 0.55 // matches original matching threshold

	query := title
	if artist != "" {
		query = artist + " " + title
	}

	type candidate struct {
		track      domain.TrackResult
		sourceName string
		score      float64
	}

	var candidates []candidate
	for _, p := range o.registry.Configured() {
		if p.Name() == excludeSource {
			continue
		}
		tracks, _, searchErr := p.Search(ctx, query)
		if searchErr != nil {
			log.Printf("orchestrator: search %s for %q: %v", p.Name(), query, searchErr)
			continue
		}
		for _, t := range tracks {
			sourceArtists := []string{}
			if artist != "" {
				sourceArtists = []string{artist}
			}
			candidateArtists := []string{}
			if t.Artist != "" {
				candidateArtists = []string{t.Artist}
			}
			if t.Username != "" {
				candidateArtists = append(candidateArtists, t.Username)
			}
			score, _ := o.matcher.ScoreTrackMatch(
				title, sourceArtists, durationMs,
				t.Title, candidateArtists, t.Duration,
			)
			if score >= minConfidence {
				candidates = append(candidates, candidate{track: t, sourceName: p.Name(), score: score})
			}
		}
	}

	if len(candidates) == 0 {
		return "", "", 0, fmt.Errorf("no matching track found across sources (min confidence %.0f%%)", minConfidence*100)
	}

	// Pick best score.
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}

	// Determine the correct source name for download.
	downloadSource := best.sourceName
	username := best.track.Username
	if best.sourceName == "deezer" {
		// Deezer downloads use "deezer" as source, but the plugin is registered as "deezer_dl".
		// The Search returns tracks with username "deezer_dl" or similar — normalize.
		dlPlugin := o.registry.Get("deezer")
		if dlPlugin == nil {
			dlPlugin = o.registry.Get("deezer_dl")
		}
		if dlPlugin != nil {
			downloadSource = dlPlugin.Name()
			username = dlPlugin.Name()
		}
	}

	id, dlErr := o.Download(ctx, downloadSource, username, best.track.Filename, best.track.Size)
	if dlErr != nil {
		return "", "", best.score, fmt.Errorf("download from %s failed: %w", best.sourceName, dlErr)
	}

	return id, best.sourceName, best.score, nil
}

// GetDownloads returns all downloads from all configured plugins.
func (o *Orchestrator) GetDownloads(ctx context.Context) []domain.DownloadRecord {
	var all []domain.DownloadRecord

	// Aggregate from all plugins.
	for _, p := range o.registry.All() {
		records, err := p.GetDownloads(ctx)
		if err != nil {
			continue
		}
		all = append(all, records...)
	}

	// Apply post-process path overrides.
	o.mu.RLock()
	defer o.mu.RUnlock()
	for i := range all {
		if override, ok := o.pathOverride[all[i].ID]; ok {
			all[i].FilePath = override
		}
	}

	return all
}

// SetDownloadPath records a corrected file path for a download (e.g., after post-download renaming).
func (o *Orchestrator) SetDownloadPath(downloadID, path string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pathOverride[downloadID] = path
}

// CancelDownload cancels a download by ID.
func (o *Orchestrator) CancelDownload(ctx context.Context, downloadID string) error {
	plugins := o.registry.All()
	for _, p := range plugins {
		if err := p.CancelDownload(ctx, downloadID, true); err == nil {
			return nil
		}
	}
	return fmt.Errorf("download %s not found", downloadID)
}
