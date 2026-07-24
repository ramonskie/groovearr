// Package download provides the download engine and orchestrator for the MVP.
package download

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/matching"
	"github.com/ramonskie/groovearr/internal/quality"
)

// minMatchConfidence is the minimum ScoreTrackMatchWithPath score to consider
// a candidate as a potential match.
const minMatchConfidence = 0.55

// ─── Orchestrator ───────────────────────────────────────────────────

// Orchestrator routes search to configured plugins.
type Orchestrator struct {
	log           *slog.Logger
	registry      *Registry
	matcher       *matching.Engine
	orderMu       sync.RWMutex
	downloadOrder []string // priority order for download source queries
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

// SetDownloadOrder configures the priority order for download source queries.
func (o *Orchestrator) SetDownloadOrder(order []string) {
	o.orderMu.Lock()
	o.downloadOrder = order
	o.orderMu.Unlock()
}

// orderedConfigured returns connected download plugins sorted by downloadOrder.
func (o *Orchestrator) orderedConfigured() []Plugin {
	plugins := o.registry.Configured()
	o.orderMu.RLock()
	order := o.downloadOrder
	o.orderMu.RUnlock()
	if len(order) == 0 || len(plugins) <= 1 {
		return plugins
	}
	byName := make(map[string]Plugin, len(plugins))
	for _, p := range plugins {
		byName[p.Name()] = p
	}
	var ordered []Plugin
	seen := make(map[string]bool)
	for _, name := range order {
		if p, ok := byName[name]; ok && !seen[name] {
			ordered = append(ordered, p)
			seen[name] = true
		}
	}
	for _, p := range plugins {
		if !seen[p.Name()] {
			ordered = append(ordered, p)
		}
	}
	return ordered
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

	plugins := o.orderedConfigured()
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
// candidate. Uses a multi-query walk: primary (artist+title), album variant
// (album+artist+title), and cleaned-title fallback — trying each in sequence
// and merging deduplicated results. Excludes results from excludeSource.
// Returns an error if no candidates meet the confidence threshold or quality
// constraints.
//
// Parameters:
//   - album: optional album name for the album-variant query (pass "" if unknown).
//
// Quality profile behavior:
//   - search_mode="priority" (default): FilterByProfile selects best target group,
//     then within-group ranking controlled by rank_candidates_by_quality.
//   - search_mode="best_quality": All candidates ranked by TierScore across all
//     target groups — quality trumps match score.
//   - rank_candidates_by_quality=true: Final selection by TierScore instead of
//     match confidence within the selected group.
func (o *Orchestrator) FindBestMatch(ctx context.Context, title, artist, album string, durationMs int64, excludeSource string, profile *quality.QualityProfile) (*Candidate, error) {
	queries := o.configuredQueries(title, artist, album)

	var allCandidates []Candidate
	seenFilenames := make(map[string]bool)

	for i, query := range queries {
		candidates := o.searchSingleQuery(ctx, query, title, artist, durationMs, excludeSource)
		o.log.Debug("FindBestMatch: query", "n", i+1, "total", len(queries), "query", query, "results", len(candidates), "component", "orchestrator")

		// Deduplicate by Filename. Skip empty filenames — they can't be deduped reliably.
		for _, c := range candidates {
			if c.Track.Filename == "" {
				allCandidates = append(allCandidates, c)
			} else if !seenFilenames[c.Track.Filename] {
				seenFilenames[c.Track.Filename] = true
				allCandidates = append(allCandidates, c)
			}
		}
	}

	if len(allCandidates) == 0 {
		return nil, fmt.Errorf("no matching track found across sources (min confidence %.0f%%)", minMatchConfidence*100)
	}

	candidates := allCandidates
	if profile == nil {
		profile = quality.DefaultProfile()
	}
	if profile.SearchMode == quality.SearchBestQuality {
		candidates = rankAllByQuality(candidates, profile)
	} else {
		candidates = FilterByProfile(candidates, profile)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no results match quality profile %q", profile.Name)
	}

	// Final selection: pick the best candidate by the appropriate metric.
	if profile.RankCandidatesByQuality {
		return pickBestByTierScore(candidates), nil
	}
	return pickBestByScore(candidates), nil
}

// configuredQueries builds an ordered, deduplicated list of search query
// variants from the available metadata. Queries are tried in order — the
// first query is the most specific (artist+title), followed by broader
// variants that may turn up results the primary query missed.
//
// Query generation rules:
//   - Query 1: "{artist} {title}" — always included when artist is non-empty.
//   - Query 2: "{album} {artist} {title}" — only when album and artist are both non-empty.
//   - Query 3: cleaned title (via matching.Engine.CleanTitle) — only when different
//     from the original title and non-empty.
func (o *Orchestrator) configuredQueries(title, artist, album string) []string {
	var queries []string
	seen := make(map[string]bool)

	addIfNew := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" {
			return
		}
		key := strings.ToLower(q)
		if !seen[key] {
			seen[key] = true
			queries = append(queries, q)
		}
	}

	// Query 1: "{artist} {title}" — primary query.
	if artist != "" {
		addIfNew(artist + " " + title)
	} else if title != "" {
		addIfNew(title)
	}

	// Query 2: "{album} {artist} {title}" — album variant.
	if album != "" && artist != "" {
		addIfNew(album + " " + artist + " " + title)
	}

	// Query 3: cleaned title — fallback for when features/remixes confuse search.
	if title != "" {
		cleanedTitle := o.matcher.CleanTitle(title)
		if cleanedTitle != title && cleanedTitle != "" {
			addIfNew(cleanedTitle)
		}
	}

	return queries
}

// searchSingleQuery queries all configured plugins with a single search string,
// scores results with ScoreTrackMatchWithPath (using the candidate's Filename
// for word-boundary artist matching), and returns candidates meeting the
// minConfidence threshold.
func (o *Orchestrator) searchSingleQuery(ctx context.Context, query, title, artist string, durationMs int64, excludeSource string) []Candidate {
	var candidates []Candidate
	sourceArtists := []string{}
	if artist != "" {
		sourceArtists = []string{artist}
	}

	for _, p := range o.orderedConfigured() {
		if p.Name() == excludeSource {
			continue
		}
		searchTracks, _, searchErr := p.Search(ctx, query)
		if searchErr != nil {
			o.log.Error("search failed", "plugin", p.Name(), "query", query, "error", searchErr, "component", "orchestrator")
			continue
		}
		for _, t := range searchTracks {
			candidateArtists := []string{}
			if t.Artist != "" {
				candidateArtists = []string{t.Artist}
			}
			// Use ScoreTrackMatchWithPath for word-boundary artist matching on the file path.
			score, _ := o.matcher.ScoreTrackMatchWithPath(
				title, sourceArtists, durationMs,
				t.Title, candidateArtists, t.Duration,
				t.Filename,
			)
			if score >= minMatchConfidence {
				candidates = append(candidates, Candidate{Track: t, SourceName: p.Name(), Score: score})
			}
		}
	}
	return candidates
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
// When scores are close (within 0.05), prefers higher audio quality as tie-breaker.
// Returns nil if candidates is empty.
func pickBestByScore(candidates []Candidate) *Candidate {
	if len(candidates) == 0 {
		return nil
	}
	best := &candidates[0]
	bestTier := best.Track.AudioQuality.TierScore()
	for i := range candidates[1:] {
		c := &candidates[i+1]
		tier := c.Track.AudioQuality.TierScore()
		// Primary: match score. Secondary: quality tier when scores are close.
		if c.Score > best.Score+0.05 || (c.Score > best.Score-0.05 && tier > bestTier) {
			best = c
			bestTier = tier
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
