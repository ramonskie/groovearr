// Package download provides the download engine and orchestrator for the MVP.
package download

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/matching"
)

// ─── Orchestrator ───────────────────────────────────────────────────

// Orchestrator routes search to configured plugins.
type Orchestrator struct {
	registry      *Registry
	matcher       *matching.Engine
	qualityConfig func() config.QualityConfig
}

// NewOrchestrator creates an orchestrator with the given plugin registry.
// qualityConfig is a function that returns the latest quality preferences
// (thread-safe via config.Persistence). Pass nil to use defaults (no filtering).
func NewOrchestrator(registry *Registry, qualityConfig func() config.QualityConfig) *Orchestrator {
	if qualityConfig == nil {
		qualityConfig = func() config.QualityConfig { return config.QualityConfig{} }
	}
	return &Orchestrator{
		registry:      registry,
		matcher:       matching.New(),
		qualityConfig: qualityConfig,
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
			log.Printf("orchestrator: search %s failed: %v", p.Name(), err)
			continue
		}
		allTracks = append(allTracks, tracks...)
		allAlbums = append(allAlbums, albums...)
	}
	return allTracks, allAlbums, nil
}

// FindBestMatch searches all configured sources for tracks matching the given
// metadata, scores candidates, applies quality filters, and returns the best
// candidate. Excludes results from excludeSource. Returns an error if no
// candidates meet the confidence threshold or quality constraints.
func (o *Orchestrator) FindBestMatch(ctx context.Context, title, artist string, durationMs int64, excludeSource string) (*Candidate, error) {
	const minConfidence = 0.55

	query := title
	if artist != "" {
		query = artist + " " + title
	}

	var candidates []Candidate
	for _, p := range o.registry.Configured() {
		if p.Name() == excludeSource {
			continue
		}
		searchTracks, _, searchErr := p.Search(ctx, query)
		if searchErr != nil {
			log.Printf("orchestrator: search %s for %q: %v", p.Name(), query, searchErr)
			continue
		}
		for _, t := range searchTracks {
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
				candidates = append(candidates, Candidate{Track: t, SourceName: p.Name(), Score: score})
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no matching track found across sources (min confidence %.0f%%)", minConfidence*100)
	}

	candidates = FilterByQuality(candidates, o.qualityConfig())
	if len(candidates) == 0 {
		qc := o.qualityConfig()
		return nil, fmt.Errorf("no results match quality constraints (format=%s, min_bitrate=%d kbps)",
			qc.PreferredFormat, qc.MinBitrate)
	}

	best := &candidates[0]
	for i := range candidates[1:] {
		if candidates[i+1].Score > best.Score {
			best = &candidates[i+1]
		}
	}

	return best, nil
}

// Candidate is a search result with match score used by FilterByQuality
// and download-best selection.
type Candidate struct {
	Track      domain.TrackResult
	SourceName string
	Score      float64
}

// FilterByQuality filters candidates by preferred_format and min_bitrate.
// It modifies the slice in-place and returns the filtered view.
func FilterByQuality(candidates []Candidate, qc config.QualityConfig) []Candidate {
	if qc.PreferredFormat == "" || qc.PreferredFormat == "any" {
		if qc.MinBitrate <= 0 {
			return candidates
		}
	}
	result := candidates[:0]
	for _, c := range candidates {
		if qc.PreferredFormat != "" && qc.PreferredFormat != "any" {
			if !QualityMatches(qc.PreferredFormat, c.Track.Quality) {
				continue
			}
		}
		if qc.MinBitrate > 0 && c.Track.Bitrate < qc.MinBitrate {
			continue
		}
		result = append(result, c)
	}
	return result
}

// qualityMatches returns true if the candidate's quality matches the preferred format.
// "flac" matches "flac". "mp3" matches "mp3", "mp3_320", "mp3_128".
func QualityMatches(want, have string) bool {
	want = strings.ToLower(want)
	have = strings.ToLower(have)
	if want == have {
		return true
	}
	if want == "mp3" && (have == "mp3_320" || have == "mp3_128") {
		return true
	}
	return false
}
