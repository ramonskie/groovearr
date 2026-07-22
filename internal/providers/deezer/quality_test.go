package deezer

import (
	"testing"

	"github.com/ramonskie/groovearr/internal/quality"
)

func TestDeezerToAudioQuality_FLAC(t *testing.T) {
	aq := deezerToAudioQuality("flac")
	if aq.Format != "flac" {
		t.Errorf("expected flac, got %s", aq.Format)
	}
	if aq.Bitrate != 1411 {
		t.Errorf("expected 1411, got %d", aq.Bitrate)
	}
	if aq.SampleRate != 44100 {
		t.Errorf("expected 44100, got %d", aq.SampleRate)
	}
	if aq.BitDepth != 16 {
		t.Errorf("expected 16, got %d", aq.BitDepth)
	}
	// TierScore > 100 for lossless.
	if aq.TierScore() < 100 {
		t.Errorf("FLAC tier score should be >=100, got %.1f", aq.TierScore())
	}
}

func TestDeezerToAudioQuality_MP3_320(t *testing.T) {
	aq := deezerToAudioQuality("mp3_320")
	if aq.Format != "mp3" {
		t.Errorf("expected mp3, got %s", aq.Format)
	}
	if aq.Bitrate != 320 {
		t.Errorf("expected 320, got %d", aq.Bitrate)
	}
}

func TestDeezerToAudioQuality_MP3_128(t *testing.T) {
	aq := deezerToAudioQuality("mp3_128")
	if aq.Format != "mp3" {
		t.Errorf("expected mp3, got %s", aq.Format)
	}
	if aq.Bitrate != 128 {
		t.Errorf("expected 128, got %d", aq.Bitrate)
	}
}

func TestDeezerToAudioQuality_Unknown(t *testing.T) {
	aq := deezerToAudioQuality("aac_96")
	if aq.Format != "mp3" {
		t.Errorf("expected mp3 for unknown tier, got %s", aq.Format)
	}
	if aq.Bitrate != 128 {
		t.Errorf("expected 128 for unknown tier, got %d", aq.Bitrate)
	}
}

func TestDeezerToAudioQuality_MatchesTarget(t *testing.T) {
	// FLAC should match a FLAC target.
	flacTarget := quality.QualityTarget{Label: "FLAC", Format: "flac"}
	aq := deezerToAudioQuality("flac")
	if !aq.MatchesTarget(flacTarget) {
		t.Error("FLAC should match FLAC target")
	}

	// MP3 320 should match MP3 with min_bitrate 320.
	mp3Target := quality.QualityTarget{Label: "MP3 320", Format: "mp3", MinBitrate: 320}
	aq320 := deezerToAudioQuality("mp3_320")
	if !aq320.MatchesTarget(mp3Target) {
		t.Error("MP3 320 should match MP3 320 target")
	}

	// MP3 128 should NOT match MP3 target with min_bitrate 320.
	aq128 := deezerToAudioQuality("mp3_128")
	if aq128.MatchesTarget(mp3Target) {
		t.Error("MP3 128 should NOT match MP3 320 target")
	}
}
