package library

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// featMarkers are unambiguous featured-artist markers. When any appears, the
// text before it is always the main artist ("2Pac feat. Anthony Hamilton" →
// "2Pac"). Checked before the general separators because a track can carry
// both ("2Pac feat. Jazze Pha, T.I. & Johntá Austin" must split at "feat.",
// not at the later comma).
var featMarkers = []string{
	" feat. ", " feat ", " featuring ", " ft. ", " ft ", " f/ ",
}

// collabSeparators are ambiguous collaboration separators. A name containing
// one MAY be a multi-artist track ("Aya Nakamura & Jayo"), but it may also be
// a real band name ("Simon & Garfunkel", "Chaka Demus & Pliers"). Callers that
// create identity records should only split on these when the leading name is
// already known, so genuine bands are never fabricated from their first member.
var collabSeparators = []string{", ", " & ", " vs ", " vs. ", " x "}

// PrimaryArtistName returns the primary artist name by stripping
// featured/collaboration suffixes. Handles non-breaking spaces (\u00a0)
// commonly found in audio file metadata.
//
// Featured markers ("feat.", "featuring", "ft.", ...) are always stripped —
// they unambiguously identify the main artist. Collaboration separators
// (", ", " & ", " vs. ", " x ") are stripped only when the leading portion
// is non-empty. Returns the original name when nothing matches.
func PrimaryArtistName(artist string) string {
	name := strings.ReplaceAll(artist, "\u00a0", " ")
	lower := strings.ToLower(name)

	for _, marker := range featMarkers {
		if idx := strings.Index(lower, marker); idx > 0 {
			return strings.TrimSpace(name[:idx])
		}
	}

	for _, sep := range collabSeparators {
		if idx := strings.Index(lower, sep); idx > 0 {
			return strings.TrimSpace(name[:idx])
		}
	}

	return artist
}

// HasFeaturingMarker reports whether the artist name contains an explicit
// featuring marker ("feat.", "featuring", "ft.", ...). Used to decide when a
// split is authoritative even if the leading artist is not yet in the library.
func HasFeaturingMarker(artist string) bool {
	lower := strings.ToLower(strings.ReplaceAll(artist, "\u00a0", " "))
	for _, marker := range featMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// IdentityArtistName returns the artist name to use for identity records and
// file layout. Only explicit featuring markers split the name ("2Pac feat. X"
// → "2Pac"). Ambiguous collaboration separators (" & ", ", ", " vs ", " x ")
// are preserved: real bands ("Simon & Garfunkel", "Chaka Demus & Pliers") are
// known artist entities in metadata, and folding them to their first member
// would mis-attribute every track to the wrong artist.
func IdentityArtistName(artist string) string {
	if HasFeaturingMarker(artist) {
		return PrimaryArtistName(artist)
	}
	return artist
}

// NormalizeArtistKey returns a stable comparison key for artist names so
// spelling variants of the same artist group together for duplicate detection.
// It decomposes accented letters (ë → e, é → e), strips combining marks,
// maps curly apostrophes/quotes and unicode hyphens to ASCII, lowercases, and
// collapses whitespace. "Tiësto" and "Tiesto" both key to "tiesto".
func NormalizeArtistKey(artist string) string {
	decomposed := norm.NFKD.String(artist)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue // combining mark (accent, diaeresis, ...)
		}
		switch r {
		case '\u2018', '\u2019', '\u201a', '\u201b': // curly apostrophes
			r = '\''
		case '\u201c', '\u201d', '\u201e', '\u201f': // curly quotes
			r = '"'
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014': // hyphens/dashes
			r = '-'
		case '\u00a0', '\u2007', '\u202f': // non-breaking spaces
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(strings.ToLower(b.String())), " ")
}
