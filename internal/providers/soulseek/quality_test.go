package soulseek

import (
	"testing"

	"github.com/ramonskie/groovearr/internal/quality"
)

func TestSlskdToAudioQuality_FLAC(t *testing.T) {
	aq := slskdToAudioQuality("song.flac", 1024)
	if aq.Format != "flac" {
		t.Errorf("expected flac, got %s", aq.Format)
	}
	if aq.Bitrate != 1024 {
		t.Errorf("expected 1024, got %d", aq.Bitrate)
	}
	// SampleRate/BitDepth left zero — TierScore uses kbps heuristic for hi-res estimation.
	if aq.SampleRate != 0 {
		t.Errorf("expected 0 sample_rate (heuristic-driven), got %d", aq.SampleRate)
	}
	if aq.BitDepth != 0 {
		t.Errorf("expected 0 bit_depth (heuristic-driven), got %d", aq.BitDepth)
	}
	// TierScore > 100 for lossless with 1024kbps bitrate (heuristic bonus).
	if aq.TierScore() < 100 {
		t.Errorf("FLAC tier score should be >=100, got %.1f", aq.TierScore())
	}
}

func TestSlskdToAudioQuality_MP3_320(t *testing.T) {
	aq := slskdToAudioQuality("track.mp3", 320)
	if aq.Format != "mp3" {
		t.Errorf("expected mp3, got %s", aq.Format)
	}
	if aq.Bitrate != 320 {
		t.Errorf("expected 320, got %d", aq.Bitrate)
	}
}

func TestSlskdToAudioQuality_MP3_128(t *testing.T) {
	aq := slskdToAudioQuality("file.mp3", 128)
	if aq.Format != "mp3" {
		t.Errorf("expected mp3, got %s", aq.Format)
	}
	if aq.Bitrate != 128 {
		t.Errorf("expected 128, got %d", aq.Bitrate)
	}
	// 128kbps should score lower than 320kbps.
	aq320 := quality.AudioQuality{Format: "mp3", Bitrate: 320}
	if aq.TierScore() >= aq320.TierScore() {
		t.Error("128kbps should score lower than 320kbps")
	}
}

func TestSlskdToAudioQuality_WAV(t *testing.T) {
	aq := slskdToAudioQuality("audio.wav", 1411)
	if aq.Format != "wav" {
		t.Errorf("expected wav, got %s", aq.Format)
	}
	if aq.Bitrate != 1411 {
		t.Errorf("expected 1411, got %d", aq.Bitrate)
	}
}

func TestSlskdToAudioQuality_Ogg(t *testing.T) {
	aq := slskdToAudioQuality("song.ogg", 256)
	if aq.Format != "ogg" {
		t.Errorf("expected ogg, got %s", aq.Format)
	}
	if aq.Bitrate != 256 {
		t.Errorf("expected 256, got %d", aq.Bitrate)
	}
}

func TestSlskdToAudioQuality_NoExtension(t *testing.T) {
	aq := slskdToAudioQuality("noextension", 0)
	// path.Ext returns "" for files with no extension, trim "" → "".
	if aq.Format != "" {
		t.Errorf("expected empty format, got %q", aq.Format)
	}
	if aq.Bitrate != 0 {
		t.Errorf("expected 0, got %d", aq.Bitrate)
	}
}
