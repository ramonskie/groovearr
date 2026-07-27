// Package library provides file scanning and path resolution.
package library

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ramonskie/groovearr/internal/sanitize"
)

// PathResolver expands a folder template into filesystem-safe paths.
// Template tokens (matching Lidarr conventions):
//
//	{artist}          → artist name
//	{album}           → album title
//	{year}            → release year (e.g., "2024")
//	{track}           → track number (bare, e.g., "5")
//	{track:00}        → zero-padded track number (e.g., "05")
//	{track:000}       → triple zero-padded (e.g., "005")
//	{title}           → track title
//	{ext}             → file extension (without dot)
//	{disc}            → disc number (bare; "1" if single-disc / unknown)
//	{disc:00}         → zero-padded disc number
//	{album_type}      → Album, EP, Single, Compilation, etc.
//
// Example template: "{artist}/{album} ({year})/{track:02d} - {title}"
// Produces:        "Daft Punk/Random Access Memories (2013)/01 - Get Lucky"
type PathResolver struct {
	template string
}

// ResolveArgs holds the values available for template expansion.
type ResolveArgs struct {
	Artist    string
	Album     string
	Year      int
	TrackNum  int // 1-based
	Title     string
	Ext       string // without leading dot, e.g. "flac", "mp3"
	DiscNum   int    // 1-based; 0 or 1 treated as "no multi-disc"
	AlbumType string // Album, EP, Single, Compilation
}

// NewPathResolver creates a resolver from a folder template string.
// Falls back to the Lidarr default if template is empty.
func NewPathResolver(template string) *PathResolver {
	if template == "" {
		template = DefaultFolderTemplate
	}
	return &PathResolver{template: template}
}

// DefaultFolderTemplate matches Lidarr's default naming convention.
const DefaultFolderTemplate = "{artist}/{album}/{track:00} - {title}"

// Resolve expands the template with the given args and returns a relative path.
// The relative path is filesystem-safe (special characters replaced).
func (r *PathResolver) Resolve(args ResolveArgs) string {
	result := r.template

	// Sanitize values for filesystem safety (remove / \ : * ? " < > |).
	artist := sanitize.PathSegment(args.Artist)
	album := sanitize.PathSegment(args.Album)
	title := sanitize.PathSegment(args.Title)

	result = strings.ReplaceAll(result, "{artist}", artist)
	result = strings.ReplaceAll(result, "{album}", album)
	result = strings.ReplaceAll(result, "{title}", title)
	result = strings.ReplaceAll(result, "{ext}", args.Ext)
	result = strings.ReplaceAll(result, "{album_type}", args.AlbumType)

	// Year.
	if args.Year > 0 {
		result = strings.ReplaceAll(result, "{year}", fmt.Sprintf("%d", args.Year))
	} else {
		// Remove year placeholder and surrounding punctuation.
		result = cleanMissingToken(result, "{year}")
	}

	// Disc number (only show if multi-disc).
	if args.DiscNum > 1 {
		result = strings.ReplaceAll(result, "{disc}", fmt.Sprintf("%d", args.DiscNum))
		result = replacePaddedToken(result, "{disc:", args.DiscNum)
	} else {
		result = cleanMissingToken(result, "{disc}")
		result = cleanMissingTokenRegex(result, `\{disc:\d+\}`)
	}

	// Track number with optional padding.
	result = strings.ReplaceAll(result, "{track}", fmt.Sprintf("%d", args.TrackNum))
	result = replacePaddedToken(result, "{track:", args.TrackNum)
	// {position} alias for playlist ordering.
	result = strings.ReplaceAll(result, "{position}", fmt.Sprintf("%d", args.TrackNum))
	result = replacePaddedToken(result, "{position:", args.TrackNum)

	// Clean up any remaining unresolved tokens (remove them).
	result = cleanRemainingTokens(result)

	// Normalize: remove leading/trailing path separators and dots, collapse double separators.
	result = strings.Trim(result, "/\\")
	result = strings.Trim(result, ". ")

	// Collapse double separators and spacing artifacts.
	for strings.Contains(result, "//") {
		result = strings.ReplaceAll(result, "//", "/")
	}
	// Remove space before slash and collapse double spaces.
	result = strings.ReplaceAll(result, " /", "/")
	result = strings.ReplaceAll(result, "/ ", "/")
	spaceRE := regexp.MustCompile(`  +`)
	result = spaceRE.ReplaceAllString(result, " ")

	return strings.TrimSpace(result)
}

// ─── Helpers ──────────────────────────────────────────────────

// replacePaddedToken handles tokens like {track:02} or {track:02d}.
func replacePaddedToken(result, prefix string, value int) string {
	re := regexp.MustCompile(regexp.QuoteMeta(prefix) + `(\d+)[dD]?\}`)
	return re.ReplaceAllStringFunc(result, func(match string) string {
		sub := re.FindStringSubmatch(match)
		if sub == nil {
			return match
		}
		digits := len(sub[1])
		format := fmt.Sprintf("%%0%dd", digits)
		return fmt.Sprintf(format, value)
	})
}

// cleanMissingToken removes a literal token like {year} and surrounding separators/parens.
// Uses a single-pass regex to avoid partial matches stripping the token without the separator.
func cleanMissingToken(result, token string) string {
	quoted := regexp.QuoteMeta(token)
	// Single alternation: separator-token, token-separator, paren(token), bracket[token], bare token.
	// Order matters: separator patterns first to capture the surrounding punctuation.
	re := regexp.MustCompile(
		`[-–—]\s*` + quoted + `|` + // " - {token}"
			quoted + `\s*[-–—]|` + // "{token} -"
			`\s*\(\s*` + quoted + `\s*\)|` + // " ({token})" — leading \s*
			`\s*\[\s*` + quoted + `\s*\]|` + // " [{token}]" — leading \s*
			`\s+` + quoted + `|` + // " {token}"
			quoted + `\s+` + // "{token} "
			quoted, // bare token (last resort)
	)
	return re.ReplaceAllString(result, "")
}

// cleanMissingTokenRegex removes a regex pattern token and surrounding separators/parens.
// Single-pass regex, same logic as cleanMissingToken.
func cleanMissingTokenRegex(result, pattern string) string {
	re := regexp.MustCompile(
		`[-–—]\s*` + pattern + `|` +
			pattern + `\s*[-–—]|` +
			`\s*\(\s*` + pattern + `\s*\)|` +
			`\s*\[\s*` + pattern + `\s*\]|` +
			`\s+` + pattern + `|` +
			pattern + `\s+` +
			`|` + pattern,
	)
	return re.ReplaceAllString(result, "")
}

// cleanRemainingTokens strips any unresolved {token} placeholders.
func cleanRemainingTokens(result string) string {
	re := regexp.MustCompile(`\{[^}]+\}`)
	result = re.ReplaceAllString(result, "")

	// Clean up leftover separators.
	result = regexp.MustCompile(`\s*[-–—]\s*$`).ReplaceAllString(result, "")
	result = regexp.MustCompile(`^\s*[-–—]\s*`).ReplaceAllString(result, "")

	return strings.TrimSpace(result)
}
