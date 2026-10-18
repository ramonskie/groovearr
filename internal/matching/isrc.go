package matching

import "strings"

// NormalizeISRC canonicalizes an ISRC for comparison and lookup. Provider APIs
// store ISRCs without separators, while tags commonly use the hyphenated form
// (US-RC1-76-07839); the two must describe the same recording.
func NormalizeISRC(isrc string) string {
	var b strings.Builder
	b.Grow(len(isrc))
	for _, r := range isrc {
		switch r {
		case '-', ' ', '\t', '\r', '\n':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.ToUpper(b.String())
}
