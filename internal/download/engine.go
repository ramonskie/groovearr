// Package download provides the download engine and orchestrator for the MVP.
package download

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/quality"
)

// ─── Orchestrator ───────────────────────────────────────────────────

// Orchestrator routes search to configured plugins.
type Orchestrator struct {
	log      *slog.Logger
	registry *Registry
	matcher  *matching.Engine
}

// NewOrchestrator creates an orchestrator with the given plugin registry.
func NewOrchestrator(registry *Registry, logger *slog.Logger) *Orchestrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		log:      logger,
		registry: registry,
		matcher:  matching.New(),
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
			o.log.Error("search failed", "plugin", p.Name(), "error", err, "component", "orchestrator")
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
//
// Quality profile behavior:
//   - search_mode="priority" (default): FilterByProfile selects best target group,
//     then within-group ranking controlled by rank_candidates_by_quality.
//   - search_mode="best_quality": All candidates ranked by TierScore across all
//     target groups — quality trumps match score.
//   - rank_candidates_by_quality=true: Final selection by TierScore instead of
//     match confidence within the selected group.
func (o *Orchestrator) FindBestMatch(ctx context.Context, title, artist string, durationMs int64, excludeSource string, profile *quality.QualityProfile) (*Candidate, error) {
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
			o.log.Error("search failed", "plugin", p.Name(), "query", query, "error", searchErr, "component", "orchestrator")
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

	if profile != nil {
		if profile.SearchMode == quality.SearchBestQuality {
			// best_quality: rank all candidates by TierScore across all target groups.
			candidates = rankAllByQuality(candidates, profile)
		} else {
			// priority: filter to best-matching target group.
			candidates = FilterByProfile(candidates, profile)
		}
	}
	if len(candidates) == 0 {
		if profile != nil {
			return nil, fmt.Errorf("no results match quality profile %q", profile.Name)
		}
		return nil, fmt.Errorf("no results match quality constraints")
	}

	// Final selection: pick the best candidate by the appropriate metric.
	if profile != nil && profile.RankCandidatesByQuality {
		return pickBestByTierScore(candidates), nil
	}
	return pickBestByScore(candidates), nil
}

// Candidate is a search result with match score used by FilterByProfile
// and download-best selection.
type Candidate struct {
	Track      domain.TrackResult
	SourceName string
	Score      float64
}

// FilterByProfile applies ranked quality targets to candidates and returns
// only the best-matching group, sorted by tier score.
func FilterByProfile(candidates []Candidate, profile *quality.QualityProfile) []Candidate {
	if profile == nil || len(profile.RankedTargets) == 0 {
		return candidates
	}

	// Convert candidates to AudioQuality slice for ranking.
	aqs := make([]quality.AudioQuality, len(candidates))
	for i, c := range candidates {
		aqs[i] = c.Track.AudioQuality
	}

	scored := quality.FilterByProfile(aqs, *profile)
	if len(scored) == 0 {
		return nil
	}

	// Map scored results back to candidates via OriginalIndex (O(1) lookup, zero collisions).
	result := make([]Candidate, len(scored))
	for i, s := range scored {
		result[i] = candidates[s.OriginalIndex]
	}
	return result
}

// rankAllByQuality scores and sorts all candidates by TierScore across all target
// groups (best_quality search mode). Unlike FilterByProfile which selects only the
// best target group, this keeps all candidates that match ANY target (or all
// candidates if fallback is enabled), sorted by quality score descending.
func rankAllByQuality(candidates []Candidate, profile *quality.QualityProfile) []Candidate {
	aqs := make([]quality.AudioQuality, len(candidates))
	for i, c := range candidates {
		aqs[i] = c.Track.AudioQuality
	}

	// Score each candidate against the profile targets.
	type scored struct {
		aq           quality.AudioQuality
		tierScore    float64
		matched      bool
		originalIdx  int
	}
	entries := make([]scored, len(candidates))
	for i, aq := range aqs {
		matched := false
		for _, t := range profile.RankedTargets {
			if aq.MatchesTarget(t) {
				matched = true
				break
			}
		}
		entries[i] = scored{aq: aq, tierScore: aq.TierScore(), matched: matched, originalIdx: i}
	}

	// Filter: keep candidates that match a target, or all candidates if fallback is enabled.
	filtered := entries[:0]
	for _, s := range entries {
		if s.matched || profile.FallbackEnabled {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	// Sort by TierScore descending.
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].tierScore > filtered[j].tierScore
	})

	result := make([]Candidate, len(filtered))
	for i, s := range filtered {
		result[i] = candidates[s.originalIdx]
	}
	return result
}

// pickBestByScore returns the candidate with the highest match confidence score.
// Returns nil if candidates is empty.
func pickBestByScore(candidates []Candidate) *Candidate {
	if len(candidates) == 0 {
		return nil
	}
	best := &candidates[0]
	for i := range candidates[1:] {
		if candidates[i+1].Score > best.Score {
			best = &candidates[i+1]
		}
	}
	return best
}

// pickBestByTierScore returns the candidate with the highest audio quality tier score.
// Returns nil if candidates is empty.
func pickBestByTierScore(candidates []Candidate) *Candidate {
	if len(candidates) == 0 {
		return nil
	}
	best := &candidates[0]
	bestTier := best.Track.AudioQuality.TierScore()
	for i := range candidates[1:] {
		tier := candidates[i+1].Track.AudioQuality.TierScore()
		if tier > bestTier {
			best = &candidates[i+1]
			bestTier = tier
		}
	}
	return best
}
