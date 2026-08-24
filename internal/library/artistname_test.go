package library

import "testing"

func TestPrimaryArtistName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"feat with period", "2Pac feat. Anthony Hamilton", "2Pac"},
		{"featuring", "2Pac featuring Trick Daddy", "2Pac"},
		{"feat without period", "Akwasi feat Gerson Main & Rob Dekay", "Akwasi"},
		{"ft with period", "Beck ft. Jay-Z", "Beck"},
		{"multiple separators after feat", "2Pac feat. Jazze Pha, T.I. & Johntá Austin", "2Pac"},
		{"ampersand collab", "Aya Nakamura & Jayo", "Aya Nakamura"},
		{"comma collab", "Blackstreet, Dr. Dre, Queen Pen", "Blackstreet"},
		{"vs", "Alp Vs Outwork", "Alp"},
		{"x collab", "A x B", "A"},
		{"non-breaking space", "2Pac\u00a0feat.\u00a0Anthony Hamilton", "2Pac"},
		{"no separator", "2Pac", "2Pac"},
		{"single word", "ABBA", "ABBA"},
		{"band with ampersand splits at &", "Chaka Demus & Pliers", "Chaka Demus"},
		{"empty string", "", ""},
		{"feat at start", "feat. Unknown", "feat. Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrimaryArtistName(tt.input); got != tt.want {
				t.Errorf("PrimaryArtistName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeArtistKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Unicode/accent variants group together.
		{"accented vs plain", "Tiësto", "tiesto"},
		{"accented vs plain reverse", "Tiesto", "tiesto"},
		{"rene accented", "René Froger", "rene froger"},
		{"rene plain", "Rene Froger", "rene froger"},
		{"jazz accented", "JAŸ-Z", "jay-z"},
		{"jazz hyphen variant", "JAŸ‐Z", "jay-z"},

		// Curly vs straight apostrophes/quotes.
		{"curly apostrophe", "Destiny’s Child", "destiny's child"},
		{"straight apostrophe", "Destiny's Child", "destiny's child"},
		{"curly quote", "Sniff ’n’ the Tears", "sniff 'n' the tears"},

		// Unicode hyphens/dashes → ASCII.
		{"u2010 hyphen", "Ne‐Yo", "ne-yo"},
		{"ascii hyphen", "Ne-Yo", "ne-yo"},
		{"em dash", "D‐Block", "d-block"},
		{"u2013 en dash", "Blink–182", "blink-182"},

		// Non-breaking space collapses.
		{"nbsp", "a\u00a0b", "a b"},

		// Whitespace collapse + lowercase.
		{"double space", "The   Beatles", "the beatles"},
		{"leading trailing", "  ABBA  ", "abba"},

		// Feat markers NOT folded here — that's IdentityArtistName's job.
		{"feat kept", "2Pac feat. Anthony Hamilton", "2pac feat. anthony hamilton"},
		{"real band kept", "Simon & Garfunkel", "simon & garfunkel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeArtistKey(tt.input); got != tt.want {
				t.Errorf("NormalizeArtistKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHasFeaturingMarker(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"2Pac feat. Anthony Hamilton", true},
		{"2Pac featuring Trick Daddy", true},
		{"Akwasi feat Gerson Main", true},
		{"Beck ft. Jay-Z", true},
		{"Aya Nakamura & Jayo", false},
		{"Blackstreet, Dr. Dre, Queen Pen", false},
		{"2Pac", false},
	}
	for _, tt := range tests {
		if got := HasFeaturingMarker(tt.input); got != tt.want {
			t.Errorf("HasFeaturingMarker(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestIdentityArtistName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Real artist entities known to metadata — never split, even when the
		// first member happens to be a standalone artist in the library.
		{"real band with ampersand", "Simon & Garfunkel", "Simon & Garfunkel"},
		{"real duo", "Chaka Demus & Pliers", "Chaka Demus & Pliers"},
		{"band with the", "Tom Petty & The Heartbreakers", "Tom Petty & The Heartbreakers"},
		{"collab with comma", "Aya Nakamura & Jayo", "Aya Nakamura & Jayo"},
		{"collab with comma sep", "Blackstreet, Dr. Dre, Queen Pen", "Blackstreet, Dr. Dre, Queen Pen"},
		{"vs collab", "Alp Vs Outwork", "Alp Vs Outwork"},
		{"x collab", "A x B", "A x B"},
		{"plain artist", "2Pac", "2Pac"},
		{"single word", "ABBA", "ABBA"},

		// Explicit feat markers are authoritative — the main artist wins.
		{"feat with period", "2Pac feat. Anthony Hamilton", "2Pac"},
		{"featuring", "2Pac featuring Trick Daddy", "2Pac"},
		{"feat without period", "Akwasi feat Gerson Main & Rob Dekay", "Akwasi"},
		{"ft with period", "Beck ft. Jay-Z", "Beck"},
		{"multiple separators after feat", "2Pac feat. Jazze Pha, T.I. & Johntá Austin", "2Pac"},
		{"non-breaking space", "2Pac\u00a0feat.\u00a0Anthony Hamilton", "2Pac"},
		{"feat at start", "feat. Unknown", "feat. Unknown"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IdentityArtistName(tt.input); got != tt.want {
				t.Errorf("IdentityArtistName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
