package download

import (
	"testing"

	"github.com/ramonskie/groovearr/internal/quality"
)

func makeProfile(targets quality.RankedTargets, policy quality.UpgradePolicy, cutoffIdx int, fallback bool) *quality.QualityProfile {
	return &quality.QualityProfile{
		Name:              "test",
		RankedTargets:     targets,
		FallbackEnabled:   fallback,
		UpgradePolicy:     policy,
		UpgradeCutoffIndex: cutoffIdx,
	}
}

func TestCheckUpgradeNeeded_Acceptable_Matches(t *testing.T) {
	targets := quality.RankedTargets{
		{Label: "FLAC", Format: "flac"},
		{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
	}
	aq := quality.AudioQuality{Format: "mp3", Bitrate: 320}
	needs, _, _ := checkUpgradeNeeded(aq, makeProfile(targets, quality.UpgradeAcceptable, 0, false))
	if needs {
		t.Error("MP3 320 should be acceptable when matching target")
	}
}

func TestCheckUpgradeNeeded_Acceptable_NoMatch_NoFallback(t *testing.T) {
	targets := quality.RankedTargets{{Label: "FLAC", Format: "flac"}}
	aq := quality.AudioQuality{Format: "mp3", Bitrate: 128}
	needs, label, _ := checkUpgradeNeeded(aq, makeProfile(targets, quality.UpgradeAcceptable, 0, false))
	if !needs {
		t.Error("MP3 128 should need upgrade when no match & no fallback")
	}
	if label != "FLAC" {
		t.Errorf("expected target 'FLAC', got '%s'", label)
	}
}

func TestCheckUpgradeNeeded_Acceptable_NoMatch_Fallback(t *testing.T) {
	targets := quality.RankedTargets{{Label: "FLAC", Format: "flac"}}
	aq := quality.AudioQuality{Format: "mp3", Bitrate: 128}
	needs, _, _ := checkUpgradeNeeded(aq, makeProfile(targets, quality.UpgradeAcceptable, 0, true))
	if needs {
		t.Error("MP3 128 should be acceptable when fallback enabled")
	}
}

func TestCheckUpgradeNeeded_UntilCutoff_BelowCutoff(t *testing.T) {
	targets := quality.RankedTargets{
		{Label: "FLAC", Format: "flac"},
		{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
	}
	// MP3 320 matches index 1. Cutoff at index 0 (FLAC). Should need upgrade.
	aq := quality.AudioQuality{Format: "mp3", Bitrate: 320}
	needs, label, _ := checkUpgradeNeeded(aq, makeProfile(targets, quality.UpgradeUntilCutoff, 0, false))
	if !needs {
		t.Error("MP3 320 should need upgrade when cutoff is FLAC")
	}
	if label != "FLAC" {
		t.Errorf("expected target 'FLAC', got '%s'", label)
	}
}

func TestCheckUpgradeNeeded_UntilCutoff_AtCutoff(t *testing.T) {
	targets := quality.RankedTargets{
		{Label: "FLAC", Format: "flac"},
		{Label: "MP3 320", Format: "mp3", MinBitrate: 320},
	}
	// FLAC matches index 0. Cutoff at 0. Should be fine.
	aq := quality.AudioQuality{Format: "flac", Bitrate: 1411, SampleRate: 44100, BitDepth: 16}
	needs, _, _ := checkUpgradeNeeded(aq, makeProfile(targets, quality.UpgradeUntilCutoff, 0, false))
	if needs {
		t.Error("FLAC at cutoff should NOT need upgrade")
	}
}

func TestCheckUpgradeNeeded_UntilTop(t *testing.T) {
	targets := quality.RankedTargets{
		{Label: "FLAC 24/96", Format: "flac", MinBitDepth: 24, MinSampleRate: 96000},
		{Label: "FLAC 16", Format: "flac", MinBitDepth: 16},
	}
	// FLAC 16 matches index 1. UntilTop means needs upgrade to index 0.
	aq := quality.AudioQuality{Format: "flac", Bitrate: 1411, SampleRate: 44100, BitDepth: 16}
	needs, label, _ := checkUpgradeNeeded(aq, makeProfile(targets, quality.UpgradeUntilTop, 0, false))
	if !needs {
		t.Error("FLAC 16 should need upgrade under UntilTop with 24/96 as top")
	}
	if label != "FLAC 24/96" {
		t.Errorf("expected target 'FLAC 24/96', got '%s'", label)
	}
}

func TestCheckUpgradeNeeded_UntilTop_AlreadyTop(t *testing.T) {
	targets := quality.RankedTargets{
		{Label: "FLAC 24/96", Format: "flac", MinBitDepth: 24, MinSampleRate: 96000},
		{Label: "FLAC 16", Format: "flac", MinBitDepth: 16},
	}
	aq := quality.AudioQuality{Format: "flac", Bitrate: 2116, SampleRate: 96000, BitDepth: 24}
	needs, _, _ := checkUpgradeNeeded(aq, makeProfile(targets, quality.UpgradeUntilTop, 0, false))
	if needs {
		t.Error("FLAC 24/96 at top should NOT need upgrade")
	}
}
