package quality

import "sort"

// ScoredCandidate wraps an AudioQuality with its ranking metadata.
type ScoredCandidate struct {
	TargetIndex   int          // which ranked_target matched (-1 = fallback/none), lower = better priority
	TierScore     float64      // within-group quality score, higher = better
	Quality       AudioQuality // the underlying quality descriptor
	OriginalIndex int          // index into the original candidates slice
}

// FilterAndRank applies ranked targets to candidates.
//
// Algorithm:
//  1. For each candidate, find the best (lowest index) matching target
//  2. Take the group with the best (lowest) target_index across all candidates
//  3. Return only candidates in that group, sorted by TierScore descending
//  4. If no candidates match any target AND fallbackEnabled: return all candidates sorted by TierScore
//  5. If no candidates match any target AND NOT fallbackEnabled: return nil
func FilterAndRank(candidates []AudioQuality, targets []QualityTarget, fallbackEnabled bool) []ScoredCandidate {
	if len(candidates) == 0 {
		return nil
	}

	// Score each candidate: find best matching target index
	scored := make([]ScoredCandidate, len(candidates))
	bestTargetIdx := len(targets) // start at "none matched"

	for i, c := range candidates {
		targetIdx := len(targets) // default: no match
		for j, t := range targets {
			if c.MatchesTarget(t) {
				targetIdx = j
				break // first match = best (targets are priority-ordered)
			}
		}
		scored[i] = ScoredCandidate{
			TargetIndex:   targetIdx,
			TierScore:     c.TierScore(),
			Quality:       c,
			OriginalIndex: i,
		}
		if targetIdx < bestTargetIdx {
			bestTargetIdx = targetIdx
		}
	}

	// Filter to best target group
	if bestTargetIdx < len(targets) {
		// At least one candidate matched a target — keep only those at bestTargetIdx
		result := scored[:0]
		for _, s := range scored {
			if s.TargetIndex == bestTargetIdx {
				result = append(result, s)
			}
		}
		scored = result
	} else if !fallbackEnabled {
		// No matches and fallback disabled — return nothing
		return nil
	}
	// else: no matches but fallback enabled — keep all candidates

	// Sort by TierScore descending (best first)
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].TierScore > scored[j].TierScore
	})

	return scored
}

// FilterByProfile is a convenience wrapper that extracts targets from a profile.
func FilterByProfile(candidates []AudioQuality, profile QualityProfile) []ScoredCandidate {
	return FilterAndRank(candidates, profile.RankedTargets, profile.FallbackEnabled)
}
