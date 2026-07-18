// Package matching provides track matching for search results and sync operations.
// Ported from the Python MusicMatchingEngine — same algorithm, different language.
package matching

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Engine matches source tracks (Spotify, iTunes, etc.) against download candidates.
type Engine struct {
	titlePatterns  []*regexp.Regexp
	artistPatterns []*regexp.Regexp

	// Version keywords that indicate a different cut of the same song.
	versionKeywords []string
	remasterKeywords []string
}

// New creates a matching engine with default patterns.
func New() *Engine {
	return &Engine{
		titlePatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\s*\(explicit\)`),
			regexp.MustCompile(`(?i)\s*\(clean\)`),
			regexp.MustCompile(`(?i)\s*\(feat\.?[^)]*\)`),
			regexp.MustCompile(`(?i)\s*\(ft\.?[^)]*\)`),
			regexp.MustCompile(`(?i)\s*\(featuring[^)]*\)`),
			regexp.MustCompile(`(?i)\sfeat\.?.*`),
			regexp.MustCompile(`(?i)\sft\.?.*`),
			regexp.MustCompile(`(?i)\sfeaturing.*`),
		},
		artistPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\s*feat\..*`),
			regexp.MustCompile(`(?i)\s*ft\..*`),
			regexp.MustCompile(`(?i)\s*featuring.*`),
		},
		versionKeywords: []string{
			"remix", "mix", "rmx",
			"live", "live at", "live from",
			"acoustic", "unplugged",
			"slowed", "reverb", "sped up", "speed up",
			"radio edit", "radio version",
			"single edit", "album edit",
			"instrumental", "karaoke",
			"extended", "extended version",
			"demo", "rough cut",
		},
		remasterKeywords: []string{"remaster", "remastered"},
	}
}

// Normalize reduces a string to a canonical form for comparison.
// Steps: accent removal (NFD), lowercase, abbreviation expansion, separator normalization, alphanumeric strip.
func (e *Engine) Normalize(s string) string {
	if s == "" {
		return ""
	}

	// Detect CJK — preserve characters instead of stripping them.
	hasCJK := containsCJK(s)

	// Remove accents (NFD decomposition + strip non-spacing marks).
	if !hasCJK {
		t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)))
		result, _, _ := transform.String(t, s)
		s = result
	}

	s = strings.ToLower(s)

	// Expand abbreviations.
	s = regexp.MustCompile(`\bpt\.`).ReplaceAllString(s, "part")
	s = regexp.MustCompile(`\bvol\.`).ReplaceAllString(s, "volume")

	// Replace separators with spaces.
	s = regexp.MustCompile(`[._/&-]`).ReplaceAllString(s, " ")

	// Strip non-alphanumeric.
	if hasCJK {
		s = regexp.MustCompile(`[^a-z0-9\s$\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}]`).ReplaceAllString(s, "")
	} else {
		s = regexp.MustCompile(`[^a-z0-9\s$]`).ReplaceAllString(s, "")
	}

	// Collapse whitespace.
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	return s
}

// CoreString returns only alphanumeric characters — strict comparison.
func (e *Engine) CoreString(s string) string {
	normalized := e.Normalize(s)
	return regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(normalized, "")
}

// CleanTitle removes featuring markers and normalizes.
func (e *Engine) CleanTitle(title string) string {
	cleaned := title
	for _, p := range e.titlePatterns {
		cleaned = p.ReplaceAllString(cleaned, "")
	}
	return e.Normalize(strings.TrimSpace(cleaned))
}

// CleanArtist removes featured artist markers and normalizes.
func (e *Engine) CleanArtist(artist string) string {
	cleaned := artist
	for _, p := range e.artistPatterns {
		cleaned = p.ReplaceAllString(cleaned, "")
	}
	return e.Normalize(strings.TrimSpace(cleaned))
}

// ScoreTrackMatch returns a confidence score (0.0–1.0) for a candidate track.
// Weights: 60% title, 30% artist, 10% duration.
func (e *Engine) ScoreTrackMatch(
	sourceTitle string, sourceArtists []string, sourceDurationMs int64,
	candidateTitle string, candidateArtists []string, candidateDurationMs int64,
) (confidence float64, matchType string) {
	// Artist scoring (30%).
	artistScore := 0.0
	for _, sa := range sourceArtists {
		saC := e.CleanArtist(sa)
		if saC == "" {
			continue
		}
		for _, ca := range candidateArtists {
			caN := e.Normalize(ca)
			caC := e.CleanArtist(ca)
			// Containment check (e.g. "drake" in "drake 21 savage").
			if len(saC) > 2 && strings.Contains(caN, saC) {
				artistScore = 1.0
				break
			}
			if saC == caN {
				artistScore = 1.0
				break
			}
			s := stringSimilarity(saC, caC)
			if s > artistScore {
				artistScore = s
			}
		}
		if artistScore >= 1.0 {
			break
		}
	}

	// Title scoring (60%).
	sourceCore := e.CoreString(sourceTitle)
	candidateCore := e.CoreString(candidateTitle)
	titleScore := 0.0

	if sourceCore != "" && sourceCore == candidateCore {
		titleScore = 1.0
	} else {
		cleanSource := e.CleanTitle(sourceTitle)
		cleanCandidate := e.CleanTitle(candidateTitle)
		titleScore = e.titleSimilarity(cleanSource, cleanCandidate)
	}

	// Duration scoring (10%).
	durationScore := durationSimilarity(sourceDurationMs, candidateDurationMs)

	// Weighted final score.
	confidence = titleScore*0.6 + artistScore*0.3 + durationScore*0.1

	if titleScore >= 0.95 && artistScore >= 0.9 {
		matchType = "exact"
	} else if confidence >= 0.7 {
		matchType = "high_confidence"
	} else if confidence >= 0.55 {
		matchType = "medium_confidence"
	} else {
		matchType = "low_confidence"
	}

	return confidence, matchType
}

// titleSimilarity compares two cleaned titles with version-awareness.
// Different versions (remix vs live vs original) get heavily penalized.
func (e *Engine) titleSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1.0
	}

	ratio := stringSimilarity(a, b)

	// Prefix check: is one a version of the other?
	shorter, longer := a, b
	if len(a) > len(b) {
		shorter, longer = b, a
	}

	if strings.HasPrefix(longer, shorter) {
		extra := strings.TrimSpace(longer[len(shorter):])
		extra = strings.ToLower(strings.Trim(extra, " -()[]"))

		// Remaster penalty (light — same song, different mastering).
		for _, kw := range e.remasterKeywords {
			if strings.Contains(extra, kw) {
				return 0.75
			}
		}

		// Version penalty (heavy — different cut of the song).
		for _, kw := range e.versionKeywords {
			if strings.Contains(extra, kw) {
				return 0.30
			}
		}
	}

	// Divergent versions: both have version markers but different ones.
	if e.hasVersionMarker(a) && e.hasVersionMarker(b) && ratio >= 0.5 {
		return 0.30
	}

	return ratio
}

func (e *Engine) hasVersionMarker(s string) bool {
	lower := strings.ToLower(s)
	for _, kw := range e.versionKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// stringSimilarity is a simple bigram-based similarity (O(n) vs O(n²) for full Levenshtein).
func stringSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	// Bigram overlap.
	bgramsA := bigrams(a)
	bgramsB := bigrams(b)

	if len(bgramsA) == 0 && len(bgramsB) == 0 {
		if a == b {
			return 1.0
		}
		return 0.0 // both empty but different (e.g., single chars)
	}

	intersection := 0
	for bg := range bgramsA {
		if bgramsB[bg] {
			intersection++
		}
	}

	union := len(bgramsA) + len(bgramsB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func bigrams(s string) map[string]bool {
	bg := make(map[string]bool)
	runes := []rune(s)
	for i := 0; i < len(runes)-1; i++ {
		bg[string(runes[i:i+2])] = true
	}
	return bg
}

// durationSimilarity scores track duration closeness.
// ±5 seconds = perfect match. Beyond that, penalty scales with ratio difference.
func durationSimilarity(d1, d2 int64) float64 {
	if d1 == 0 || d2 == 0 {
		return 0.5 // neutral
	}

	diff := d1 - d2
	if diff < 0 {
		diff = -diff
	}

	if diff <= 5000 {
		return 1.0
	}

	diffRatio := float64(diff) / float64(max(d1, d2))
	return max(0, 1.0-diffRatio*5)
}

// containsCJK returns true if the string contains CJK characters.
func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) ||
			unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) ||
			unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}
