// Package library provides file scanning and library import.
package library

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrDurationUnavailable means the configured duration probe binary is not on
// PATH. Callers treat it as "duration unknown" rather than a scan failure.
var ErrDurationUnavailable = errors.New("library: duration probe unavailable")

// durationProbeTimeout bounds one ffprobe call. A probe is a small metadata
// read; a hung subprocess must not stall a scan or a backfill.
const durationProbeTimeout = 5 * time.Second

// ProbeDuration returns the file's media duration using ffprobe. It is the
// single duration source for scan/import backfills so every caller agrees on
// the same value.
//
// The binary is FFPROBE_PATH when set, otherwise "ffprobe". A missing binary is
// ErrDurationUnavailable (the caller leaves duration at zero); an unreadable or
// duration-less file is a normal error. The path is passed as an argument to
// exec.CommandContext, never through a shell, so a filename cannot inject
// flags. "-i" also keeps a path that starts with "-" from being parsed as an
// option.
func ProbeDuration(ctx context.Context, path string) (time.Duration, error) {
	bin := strings.TrimSpace(os.Getenv("FFPROBE_PATH"))
	if bin == "" {
		bin = "ffprobe"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return 0, ErrDurationUnavailable
	}

	cctx, cancel := context.WithTimeout(ctx, durationProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, bin,
		"-v", "error",
		"-protocol_whitelist", "file",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", path,
	)
	out, err := cmd.Output()
	if err != nil {
		if cctx.Err() != nil {
			return 0, fmt.Errorf("duration probe %s: %w", filepath.Base(path), cctx.Err())
		}
		return 0, fmt.Errorf("duration probe %s: %w", filepath.Base(path), err)
	}

	d, err := parseDurationOutput(string(out))
	if err != nil {
		return 0, fmt.Errorf("duration probe %s: %w", filepath.Base(path), err)
	}
	return d, nil
}

// parseDurationOutput converts ffprobe's format=duration seconds into a
// time.Duration. It rejects empty/N/A, non-finite, non-positive and absurd
// values before the float->duration conversion can overflow.
func parseDurationOutput(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "N/A" {
		return 0, errors.New("no duration")
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) ||
		seconds <= 0 || seconds > 100*3600 {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
