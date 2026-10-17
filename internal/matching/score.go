package matching

import "time"

// CandidateSignals is the provider-neutral evidence available for one
// provider candidate. The caller maps its own result/track types onto it; this
// file stays free of I/O and provider names (AGENTS §1).
type CandidateSignals struct {
	ISRCExact               bool
	ProviderTrackIDExact    bool
	TitleMatches            bool
	ArtistMatches           bool
	DurationKnown           bool
	DurationWithinTolerance bool
	AlbumTitleMatches       bool
	TrackCountMatches       bool
	TrackNumberMatches      bool
}

// Confidence bands for CandidateScore.
const (
	AutoApplyThreshold = 75
	ReviewThreshold    = 50
)

// CandidateScore turns the evidence into one 0-100 confidence:
//
//	>= AutoApplyThreshold (75)  auto-apply
//	ReviewThreshold..74 (50)    review
//	< ReviewThreshold (50)      local-only / no match
//
// Weights are deliberately additive but capped. An exact ISRC/provider track ID
// is strong identity, so it starts high; a title/artist/duration combination
// can reach auto-apply without an ID. A KNOWN duration conflict always caps the
// score below auto-apply, even when the ID matched: a wrong ISRC must land in
// review, never be trusted just because the ID matched.
func CandidateScore(s CandidateSignals) int {
	score := 0
	if s.ISRCExact {
		score += 60
	}
	if s.ProviderTrackIDExact {
		score += 50
	}
	if s.TitleMatches {
		score += 35
	}
	if s.ArtistMatches {
		score += 20
	}
	if s.DurationKnown && s.DurationWithinTolerance {
		score += 20
	}
	if s.AlbumTitleMatches {
		score += 5
	}
	if s.TrackCountMatches {
		score += 5
	}
	if s.TrackNumberMatches {
		score += 5
	}
	if score > 100 {
		score = 100
	}
	if s.DurationKnown && !s.DurationWithinTolerance && score >= AutoApplyThreshold {
		score = AutoApplyThreshold - 1
	}
	return score
}

// DurationWithinTolerance reports whether two track durations are close enough
// to be the same recording. Zero/unknown durations never match. The tolerance
// belongs to the caller (typically ±5s) because provider versions differ by
// intros/outros/remasters.
func DurationWithinTolerance(localMs, candidateMs int64, tolerance time.Duration) bool {
	if localMs <= 0 || candidateMs <= 0 {
		return false
	}
	if tolerance < 0 {
		tolerance = 0
	}
	diff := localMs - candidateMs
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance.Milliseconds()
}
