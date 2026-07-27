package quality

import (
	"testing"
)

func TestTierScore_Lossless(t *testing.T) {
	flac := AudioQuality{Format: "flac", Bitrate: 1411, SampleRate: 44100, BitDepth: 16}
	score := flac.TierScore()
	if score < 100 {
		t.Errorf("FLAC 16/44.1 score %f < 100", score)
	}

	flac24 := AudioQuality{Format: "flac", Bitrate: 2116, SampleRate: 96000, BitDepth: 24}
	score24 := flac24.TierScore()
	if score24 <= score {
		t.Errorf("FLAC 24/96 score %f should be > FLAC 16/44.1 score %f", score24, score)
	}
}

func TestTierScore_Lossy(t *testing.T) {
	mp3320 := AudioQuality{Format: "mp3", Bitrate: 320}
	mp3128 := AudioQuality{Format: "mp3", Bitrate: 128}
	if mp3320.TierScore() <= mp3128.TierScore() {
		t.Error("MP3 320 should score higher than MP3 128")
	}
}

func TestMatchesTarget_Format(t *testing.T) {
	aq := AudioQuality{Format: "flac", Bitrate: 1411}
	target := QualityTarget{Label: "FLAC", Format: "flac"}
	if !aq.MatchesTarget(target) {
		t.Error("FLAC should match FLAC target")
	}
	target.Format = "FLAC" // case insensitive
	if !aq.MatchesTarget(target) {
		t.Error("FLAC should match case-insensitive target")
	}
	target.Format = "mp3"
	if aq.MatchesTarget(target) {
		t.Error("FLAC should NOT match mp3 target")
	}
}

func TestMatchesTarget_Bitrate(t *testing.T) {
	aq := AudioQuality{Format: "mp3", Bitrate: 320}
	target := QualityTarget{Label: "MP3 320", Format: "mp3", MinBitrate: 320}
	if !aq.MatchesTarget(target) {
		t.Error("MP3 320 should match >= 320 target")
	}
	target.MinBitrate = 321
	if aq.MatchesTarget(target) {
		t.Error("MP3 320 should NOT match > 320 target")
	}
}

func TestMatchesTarget_MultiConstraint(t *testing.T) {
	aq := AudioQuality{Format: "flac", Bitrate: 2116, SampleRate: 96000, BitDepth: 24}
	target := QualityTarget{Label: "FLAC 24/96", Format: "flac", MinSampleRate: 96000, MinBitDepth: 24}
	if !aq.MatchesTarget(target) {
		t.Error("FLAC 24/96 should match 24/96 target")
	}
	target.MinBitDepth = 32
	if aq.MatchesTarget(target) {
		t.Error("FLAC 24-bit should NOT match 32-bit target")
	}
}

func TestMatchesTarget_EmptyConstraints(t *testing.T) {
	aq := AudioQuality{Format: "wma", Bitrate: 64}
	target := QualityTarget{Label: "Anything"}
	if !aq.MatchesTarget(target) {
		t.Error("empty target should match anything")
	}
}

func TestFilterAndRank_BestGroup(t *testing.T) {
	targets := RankedTargets{
		{Label: "FLAC", Format: "flac"},
		{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
	}

	candidates := []AudioQuality{
		{Format: "mp3", Bitrate: 320},   // matches target index 1
		{Format: "flac", Bitrate: 1411}, // matches target index 0 (BEST)
		{Format: "mp3", Bitrate: 128},   // matches nothing
	}

	result := FilterAndRank(candidates, targets, false)
	if len(result) != 1 {
		t.Fatalf("expected 1 result in best group, got %d", len(result))
	}
	if result[0].Quality.Format != "flac" {
		t.Errorf("expected FLAC in best group, got %s", result[0].Quality.Format)
	}
}

func TestFilterAndRank_Fallback(t *testing.T) {
	targets := RankedTargets{
		{Label: "FLAC", Format: "flac"},
	}

	candidates := []AudioQuality{
		{Format: "mp3", Bitrate: 320},
		{Format: "mp3", Bitrate: 128},
	}

	// fallback disabled — nothing matches
	result := FilterAndRank(candidates, targets, false)
	if len(result) != 0 {
		t.Errorf("expected 0 results with fallback disabled, got %d", len(result))
	}

	// fallback enabled — all returned, best first
	result = FilterAndRank(candidates, targets, true)
	if len(result) != 2 {
		t.Fatalf("expected 2 results with fallback enabled, got %d", len(result))
	}
	if result[0].Quality.Bitrate != 320 {
		t.Error("expected MP3 320 first when fallback enabled")
	}
}

func TestFilterAndRank_EmptyCandidates(t *testing.T) {
	targets := RankedTargets{{Label: "FLAC", Format: "flac"}}
	result := FilterAndRank(nil, targets, true)
	if result != nil {
		t.Error("expected nil for empty candidates")
	}
}

func TestFilterAndRank_SortOrder(t *testing.T) {
	// Within the same target group, higher tier score first
	targets := RankedTargets{
		{Label: "FLAC", Format: "flac"},
	}

	candidates := []AudioQuality{
		{Format: "flac", Bitrate: 1411, SampleRate: 44100, BitDepth: 16},
		{Format: "flac", Bitrate: 2116, SampleRate: 96000, BitDepth: 24},
	}

	result := FilterAndRank(candidates, targets, false)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	if result[0].Quality.BitDepth != 24 {
		t.Error("expected 24-bit FLAC first (higher tier score)")
	}
}

func TestFilterByProfile(t *testing.T) {
	profile := QualityProfile{
		Name: "Test",
		RankedTargets: RankedTargets{
			{Label: "FLAC", Format: "flac"},
		},
		FallbackEnabled: false,
	}

	candidates := []AudioQuality{
		{Format: "flac", Bitrate: 1411},
		{Format: "mp3", Bitrate: 320},
	}

	result := FilterByProfile(candidates, profile)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0].Quality.Format != "flac" {
		t.Errorf("expected FLAC, got %s", result[0].Quality.Format)
	}
}
