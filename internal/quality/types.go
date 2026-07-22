package quality

import (
	"strings"
	"time"
)

// AudioQuality is a source-agnostic descriptor of an audio file's quality.
// Every provider maps its raw data into this struct. No provider-specific code here.
type AudioQuality struct {
	Format     string `json:"format"`                // flac, mp3, aac, ogg, wav, wma, m4a
	Bitrate    int    `json:"bitrate,omitempty"`     // kbps (always populated)
	SampleRate int    `json:"sample_rate,omitempty"` // Hz (from probe, for lossless hi-res detection)
	BitDepth   int    `json:"bit_depth,omitempty"`   // 16, 24 (from probe)
}

// TierScore returns a continuous quality score for ranking within a matched target group.
// Higher = better quality. Scale: roughly 0-130.
// Lossless: format_base + sample_rate bonus (0-20) + bit_depth bonus (0-10).
// Lossy: format_base + bitrate bonus (0-10 scaled to 320kbps).
func (aq AudioQuality) TierScore() float64 {
	formatBase := map[string]float64{
		"flac": 100, "wav": 95, "alac": 98,
		"ogg": 70, "opus": 65, "aac": 60, "m4a": 60,
		"mp3": 50, "wma": 30,
	}
	base := formatBase[aq.Format]
	if base == 0 {
		base = 40 // unknown format
	}

	isLossless := aq.Format == "flac" || aq.Format == "wav" || aq.Format == "alac"

	if isLossless {
		// Sample rate bonus: 0 at 0Hz, +10 at 96kHz, +20 at 192kHz+
		srBonus := 0.0
		if aq.SampleRate > 0 {
			srBonus = float64(aq.SampleRate) / 192000.0 * 20.0
			if srBonus > 20 {
				srBonus = 20
			}
		} else if aq.Bitrate > 0 {
			// Heuristic: kbps/8 ≈ uncompressed rate, avoid over-claiming hi-res
			estRate := float64(aq.Bitrate) / 8.0
			srBonus = estRate / 192000.0 * 20.0
			if srBonus > 20 {
				srBonus = 20
			}
		}

		// Bit depth bonus: +5 for 24-bit, +10 for 32-bit
		bdBonus := 0.0
		switch aq.BitDepth {
		case 24:
			bdBonus = 5
		case 32:
			bdBonus = 10
		}

		return base + srBonus + bdBonus
	}

	// Lossy: bitrate bonus scaled to 320kbps
	brBonus := float64(aq.Bitrate) / 320.0 * 10.0
	if brBonus > 10 {
		brBonus = 10
	}
	return base + brBonus
}

// QualityTarget defines a single tier in a profile's ranked fallback chain.
// Stored as JSON in quality_profiles.ranked_targets.
// All constraint fields are optional — only non-zero values are enforced.
type QualityTarget struct {
	Label         string `json:"label"`                    // "FLAC 24-bit/192kHz"
	Format        string `json:"format,omitempty"`         // restrict to this format (case-insensitive)
	MinBitrate    int    `json:"min_bitrate,omitempty"`    // minimum kbps (lossy only)
	MinSampleRate int    `json:"min_sample_rate,omitempty"` // minimum Hz
	MinBitDepth   int    `json:"min_bit_depth,omitempty"`  // minimum bit depth
}

// MatchesTarget checks all non-zero constraint fields.
// Case-insensitive format match. Bitrate must be >= MinBitrate if set.
// SampleRate must be >= MinSampleRate if set. BitDepth must be >= MinBitDepth if set.
func (aq AudioQuality) MatchesTarget(t QualityTarget) bool {
	if t.Format != "" && !strings.EqualFold(aq.Format, t.Format) {
		return false
	}
	if t.MinBitrate > 0 && aq.Bitrate < t.MinBitrate {
		return false
	}
	if t.MinSampleRate > 0 && aq.SampleRate < t.MinSampleRate {
		return false
	}
	if t.MinBitDepth > 0 && aq.BitDepth < t.MinBitDepth {
		return false
	}
	return true
}

// UpgradePolicy controls how aggressive the upgrade behavior is.
type UpgradePolicy string

const (
	UpgradeAcceptable  UpgradePolicy = "acceptable"  // download anything meeting any target
	UpgradeUntilCutoff UpgradePolicy = "until_cutoff" // keep upgrading until reaching cutoff index
	UpgradeUntilTop    UpgradePolicy = "until_top"    // keep upgrading until reaching top target
)

// SearchMode controls how candidates are selected from search results.
type SearchMode string

const (
	SearchPriority    SearchMode = "priority"    // first source wins, try targets in order
	SearchBestQuality SearchMode = "best_quality" // pool all sources, pick best quality
)

// RankedTargets is a convenience type for JSON (de)serialization.
type RankedTargets []QualityTarget

// QualityProfile is a named set of quality preferences.
type QualityProfile struct {
	ID                      int64         `json:"id"`
	Name                    string        `json:"name"`
	Description             string        `json:"description"`
	RankedTargets           RankedTargets `json:"ranked_targets"`
	FallbackEnabled         bool          `json:"fallback_enabled"`
	SearchMode              SearchMode    `json:"search_mode"`
	RankCandidatesByQuality bool          `json:"rank_candidates_by_quality"`
	UpgradePolicy           UpgradePolicy `json:"upgrade_policy"`
	UpgradeCutoffIndex      int           `json:"upgrade_cutoff_index"`
	ReplaceLowerQuality     bool          `json:"replace_lower_quality"`
	IsDefault               bool          `json:"is_default"`
	CreatedAt               time.Time     `json:"created_at"`
	UpdatedAt               time.Time     `json:"updated_at"`
}
