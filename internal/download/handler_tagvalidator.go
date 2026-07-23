package download

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/matching"
)

// TagValidatorHandler verifies that a downloaded file's ID3/FLAC tags match
// the expected metadata from the discovery/playlist source. Runs as position 1
// in the import chain, before the renamer.
type TagValidatorHandler struct {
	engine *matching.Engine
	log    *slog.Logger
}

// NewTagValidatorHandler creates a handler that validates downloaded file tags.
// The matching engine is created internally — no dependency injection needed
// since matching.New() has no external dependencies.
func NewTagValidatorHandler(logger *slog.Logger) *TagValidatorHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &TagValidatorHandler{
		engine: matching.New(),
		log:    logger,
	}
}

// Handle reads the file's audio tags and compares them against the expected
// metadata in the download record. Tag mismatches block import — file tags are
// more authoritative than parsed filenames.
func (h *TagValidatorHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.FilePath == "" || record.Artist == "" {
		return nil
	}

	tags, err := library.ReadTags(record.FilePath)
	if err != nil || tags == nil {
		return nil // can't read tags, allow through
	}

	if !h.artistMatches(record.Artist, tags.Artist) {
		h.log.Warn("tag mismatch: artist",
			"download_id", record.ID,
			"expected", record.Artist,
			"got", tags.Artist,
		)
		return fmt.Errorf("tag mismatch: expected artist %q, got %q", record.Artist, tags.Artist)
	}

	if record.Title != "" && !h.titleMatches(record.Title, tags.Title) {
		h.log.Warn("tag mismatch: title",
			"download_id", record.ID,
			"expected", record.Title,
			"got", tags.Title,
		)
		return fmt.Errorf("tag mismatch: expected title %q, got %q", record.Title, tags.Title)
	}

	return nil
}

// artistMatches checks if the tag artist matches the expected artist.
// Uses token-based comparison: each artist token from the expected field must
// appear in the file tag. Handles multi-artist collabs where Spotify uses
// comma-separated format while file tags use " x " or different separators.
func (h *TagValidatorHandler) artistMatches(expected, actual string) bool {
	exp := h.engine.Normalize(expected)
	act := h.engine.Normalize(actual)
	if exp == "" || act == "" {
		return true // can't validate, allow
	}
	// Straight containment check (catches single-artist cases).
	if strings.Contains(act, exp) || strings.Contains(exp, act) {
		return true
	}
	// Token-based: all expected artist tokens must appear in the tag.
	expTokens := strings.Fields(exp)
	actTokens := make(map[string]bool)
	for _, t := range strings.Fields(act) {
		if len(t) > 1 {
			actTokens[t] = true
		}
	}
	matched := 0
	for _, t := range expTokens {
		if len(t) <= 1 {
			continue
		}
		if actTokens[t] {
			matched++
		}
	}
	if len(expTokens) == 0 {
		return true
	}
	return float64(matched)/float64(len(expTokens)) >= 0.7
}

// titleMatches checks if the tag title matches the expected title.
// Uses token-based comparison with version-tolerant fallback.
func (h *TagValidatorHandler) titleMatches(expected, actual string) bool {
	exp := h.engine.Normalize(expected)
	act := h.engine.Normalize(actual)
	if exp == "" || act == "" {
		return true
	}
	if strings.Contains(act, exp) || strings.Contains(exp, act) {
		return true
	}
	// Strip parenthetical version suffixes and try again.
	expClean := stripParenthetical(exp)
	actClean := stripParenthetical(act)
	if expClean != "" && actClean != "" {
		if strings.Contains(actClean, expClean) || strings.Contains(expClean, actClean) {
			return true
		}
	}
	// Token-based: most expected title tokens must appear in the tag.
	expTokens := strings.Fields(exp)
	actTokens := make(map[string]bool)
	for _, t := range strings.Fields(act) {
		if len(t) > 1 {
			actTokens[t] = true
		}
	}
	matched := 0
	for _, t := range expTokens {
		if len(t) <= 1 {
			continue
		}
		if actTokens[t] {
			matched++
		}
	}
	if len(expTokens) == 0 {
		return true
	}
	return float64(matched)/float64(len(expTokens)) >= 0.8
}

// stripParenthetical removes all parenthetical content from a string.
// Kept as a private helper since the matching engine has no equivalent.
func stripParenthetical(s string) string {
	for {
		start := strings.Index(s, "(")
		end := strings.Index(s, ")")
		if start >= 0 && end > start {
			s = strings.TrimSpace(s[:start] + s[end+1:])
		} else {
			break
		}
	}
	return strings.TrimSpace(s)
}
