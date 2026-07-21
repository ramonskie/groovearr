package download

import (
	"context"
	"fmt"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// TagValidatorHandler verifies that a downloaded file's ID3/FLAC tags match
// the expected metadata from the discovery/playlist source. Runs as position 1
// in the import chain, before the renamer.
type TagValidatorHandler struct{}

// NewTagValidatorHandler creates a handler that validates downloaded file tags.
func NewTagValidatorHandler() *TagValidatorHandler {
	return &TagValidatorHandler{}
}

// Handle reads the file's audio tags and compares them against the expected
// metadata in the download record.
func (h *TagValidatorHandler) Handle(ctx context.Context, record *domain.DownloadRecord) error {
	if record.FilePath == "" || record.Artist == "" {
		return nil
	}

	tags, err := library.ReadTags(record.FilePath)
	if err != nil || tags == nil {
		return nil // can't read tags, allow through
	}

	if !artistMatches(record.Artist, tags.Artist) {
		return fmt.Errorf("tag mismatch: expected artist %q, got %q", record.Artist, tags.Artist)
	}

	if record.Title != "" && !titleMatches(record.Title, tags.Title) {
		return fmt.Errorf("tag mismatch: expected title %q, got %q", record.Title, tags.Title)
	}

	return nil
}

// artistMatches checks if the tag artist matches the expected artist.
// Case-insensitive containment or approximate match.
func artistMatches(expected, actual string) bool {
	exp := normalizeCompare(expected)
	act := normalizeCompare(actual)
	if exp == "" || act == "" {
		return true // can't validate, allow
	}
	if strings.Contains(act, exp) {
		return true
	}
	if strings.Contains(exp, act) {
		return true
	}
	return false
}

// titleMatches checks if the tag title matches the expected title.
// Normalized containment, version-tolerant.
func titleMatches(expected, actual string) bool {
	exp := normalizeCompare(expected)
	act := normalizeCompare(actual)
	if exp == "" || act == "" {
		return true
	}
	if strings.Contains(act, exp) {
		return true
	}
	if strings.Contains(exp, act) {
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
	return false
}

func normalizeCompare(s string) string {
	s = strings.ToLower(s)
	// Strip common suffixes that differ between sources.
	for _, suffix := range []string{
		" - radio edit", " - radio mix", " - radio version",
		" - single edit", " - album version", " - 7\" mix",
		" (radio edit)", " (radio mix)", " (radio version)",
		" (single edit)", " (album version)", " (7\" mix)",
	} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

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
