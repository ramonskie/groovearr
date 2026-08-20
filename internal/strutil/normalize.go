// Package strutil provides shared string helpers used across packages.
package strutil

import "strings"

// NormalizeName lowercases, strips common Latin accents and removes
// punctuation, producing a stable key for case-insensitive name comparison
// and dedup. "Acda en De Munnik" and "Acda en de Munnik" both become
// "acdaendemunnik".
func NormalizeName(s string) string {
	s = strings.ToLower(s)
	// Simple accent removal for common Latin accents.
	replacer := strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ñ", "n", "ç", "c",
	)
	s = replacer.Replace(s)
	// Remove remaining non-alphanumeric.
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
