package library

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestWAV writes a mono 16-bit PCM WAV of the requested duration. It is
// intentionally tiny (8 kHz) so tests stay fast; ffprobe still reports an
// accurate duration from the RIFF/data sizes.
func writeTestWAV(t *testing.T, path string, duration time.Duration) {
	t.Helper()
	const sampleRate = 8000
	const channels = 1
	const bitsPerSample = 16
	dataSize := uint32(sampleRate * channels * bitsPerSample / 8 * int(duration/time.Second))
	byteRate := uint32(sampleRate * channels * bitsPerSample / 8)
	blockAlign := uint16(channels * bitsPerSample / 8)

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(channels))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&buf, binary.LittleEndian, byteRate)
	binary.Write(&buf, binary.LittleEndian, blockAlign)
	binary.Write(&buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, dataSize)
	buf.Write(make([]byte, dataSize))

	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write wav: %v", err)
	}
}

func requireFFprobe(t *testing.T) {
	t.Helper()
	bin := strings.TrimSpace(os.Getenv("FFPROBE_PATH"))
	if bin == "" {
		bin = "ffprobe"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip("ffprobe not installed")
	}
}

func TestProbeDurationWAV(t *testing.T) {
	requireFFprobe(t)
	path := filepath.Join(t.TempDir(), "tone.wav")
	writeTestWAV(t, path, time.Second)

	got, err := ProbeDuration(context.Background(), path)
	if err != nil {
		t.Fatalf("ProbeDuration: %v", err)
	}
	if diff := got - time.Second; diff < -50*time.Millisecond || diff > 50*time.Millisecond {
		t.Fatalf("duration = %v, want 1s ±50ms", got)
	}
}

func TestParseDurationOutput(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{"seconds", "1.5", 1500 * time.Millisecond, false},
		{"whitespace", " 2.25\n", 2250 * time.Millisecond, false},
		{"empty", "", 0, true},
		{"na", "N/A", 0, true},
		{"zero", "0", 0, true},
		{"negative", "-1", 0, true},
		{"nan", "NaN", 0, true},
		{"inf", "Inf", 0, true},
		{"absurd", "1e20", 0, true},
		{"garbage", "abc", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDurationOutput(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseDurationOutput(%q) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parseDurationOutput(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestProbeDurationMissingBinary(t *testing.T) {
	t.Setenv("FFPROBE_PATH", "definitely-not-a-real-ffprobe")
	_, err := ProbeDuration(context.Background(), filepath.Join(t.TempDir(), "x.wav"))
	if !errors.Is(err, ErrDurationUnavailable) {
		t.Fatalf("err = %v, want ErrDurationUnavailable", err)
	}
}

func TestProbeDurationUnreadableFile(t *testing.T) {
	requireFFprobe(t)
	path := filepath.Join(t.TempDir(), "broken.wav")
	if err := os.WriteFile(path, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeDuration(context.Background(), path); err == nil {
		t.Fatal("expected an error for a non-audio file")
	}
}
