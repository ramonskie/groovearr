package matching

// Property and fuzz tests for the shared matcher. They deliberately assert
// invariants that production code claims to hold (see match.go doc comments):
// normalization is idempotent and lossless for meaningful titles, edition
// trimming only ever shortens and is idempotent, and BestMatch is a
// deterministic, index-stable function. If an invariant is violated the test
// FAILS — the invariant is never weakened to hide a defect.

import (
	"math/rand"
	"strings"
	"testing"
	"unicode"
)

// hasAlnum reports whether s contains at least one Unicode letter or digit.
// It is the "meaningful title" predicate behind the never-empty invariant.
func hasAlnum(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) >= 0
}

// propertyBases are real-looking base titles, each containing alphanumerics.
// A variant of a base must normalize to the same key as the base.
var propertyBases = []string{
	"Échame La Culpa",
	"Seven Days in Sunny June",
	"Desecration Smile",
	"Unconditionally",
	"Don't Stop",
	"R&B",
	"AC/DC",
	"Spider-Man",
	"New Edition",
	"Album",
}

// strippableSuffixes are delimited suffixes the matcher is contracted to drop
// (edition markers, bare years, or a separator-only single/EP). Appending one
// to a base must leave both NormalizeTitle and TrimEditionSuffix at the base.
// The empty suffix exercises the no-suffix path.
var strippableSuffixes = []string{
	"",
	" (Deluxe Edition)",
	" [Deluxe]",
	" - Remastered",
	" - Single",
	" - EP",
	" (2020 Remaster)",
}

// nonStrippableSuffixes are delimited groups that intentionally survive: a
// performance variant or a different tracklisting. They must change the key,
// otherwise the matcher would bind a library album to the wrong release.
var nonStrippableSuffixes = []string{
	" (Live)",
	" (Remixes)",
	" - Live",
}

// propertyArtists covers multi-word acts, punctuation-heavy acts and
// collaborators, plus the empty (unknown) credit.
var propertyArtists = []string{
	"Luis Fonsi",
	"Red Hot Chili Peppers",
	"Jamiroquai",
	"Katy Perry",
	"AC/DC",
	"Daft Punk",
	"Simon & Garfunkel",
	"The Cure",
	"",
}

// variantKey returns a case/whitespace variant of s that MUST normalize to the
// same key: normalize() folds case and collapses runs of separators, so only
// case and surrounding/interior whitespace may change. Intra-word punctuation
// is deliberately NOT injected — that would split a token and legitimately
// change the key.
func variantKey(rng *rand.Rand, s string) string {
	switch rng.Intn(5) {
	case 0:
		return s
	case 1:
		return strings.ToUpper(s)
	case 2:
		return strings.ToLower(s)
	case 3:
		return "  " + s + " "
	default:
		return strings.ReplaceAll(s, " ", "  ")
	}
}

// TestMatchPropertiesRandomized drives the matcher with a FIXED-seed stream of
// generated (library, provider, artist) triples and asserts the package's
// documented invariants. Determinism of the generator (rand.NewSource(1)) makes
// failures reproducible; the iteration count is capped under -short so a quick
// run stays fast.
func TestMatchPropertiesRandomized(t *testing.T) {
	iterations := 5000
	if testing.Short() {
		iterations = 300
	}
	rng := rand.New(rand.NewSource(1))
	pick := func(items []string) string { return items[rng.Intn(len(items))] }

	for i := 0; i < iterations; i++ {
		base := pick(propertyBases)
		wantSuffix := pick(strippableSuffixes)
		wantTitle := base + wantSuffix
		wantArtist := pick(propertyArtists)

		// --- NormalizeTitle: idempotent and lossless for meaningful input. ---
		for _, s := range []string{base, wantTitle} {
			got := NormalizeTitle(s)
			if again := NormalizeTitle(got); again != got {
				t.Fatalf("NormalizeTitle not idempotent: %q -> %q -> %q", s, got, again)
			}
			if got == "" && hasAlnum(s) {
				t.Fatalf("NormalizeTitle(%q) = %q for input containing a letter/digit", s, got)
			}
		}

		// --- TrimEditionSuffix: never lengthens, idempotent, base for known
		// suffix, and never collapses a non-strippable variant. ---
		trimmed := TrimEditionSuffix(wantTitle)
		if len(trimmed) > len(wantTitle) {
			t.Fatalf("TrimEditionSuffix lengthened %q -> %q", wantTitle, trimmed)
		}
		if again := TrimEditionSuffix(trimmed); again != trimmed {
			t.Fatalf("TrimEditionSuffix not idempotent: %q -> %q -> %q", wantTitle, trimmed, again)
		}
		if wantSuffix != "" && trimmed != base {
			t.Fatalf("TrimEditionSuffix(%q) = %q, want base %q", wantTitle, trimmed, base)
		}

		nonStrippable := base + pick(nonStrippableSuffixes)
		if NormalizeTitle(nonStrippable) == NormalizeTitle(base) {
			t.Fatalf("non-strippable suffix collapsed %q to %q", nonStrippable, NormalizeTitle(base))
		}

		// --- BestMatch on an empty candidate set. ---
		if idx, ok := BestMatch(wantArtist, wantTitle, nil); ok || idx != 0 {
			t.Fatalf("BestMatch(empty candidates) = (%d,%v), want (0,false)", idx, ok)
		}

		// --- Positive pair: equal normalized title + compatible artist must match. ---
		candTitle := variantKey(rng, base) + wantSuffix
		candArtist := wantArtist
		if wantArtist != "" && rng.Intn(2) == 0 {
			candArtist = wantArtist + " & Guest Star"
		}
		if NormalizeTitle(candTitle) != NormalizeTitle(wantTitle) {
			t.Fatalf("construction bug: candidate key %q != want key %q",
				NormalizeTitle(candTitle), NormalizeTitle(wantTitle))
		}
		if !ArtistCompatible(wantArtist, candArtist) {
			t.Fatalf("construction bug: artists %q / %q incompatible", wantArtist, candArtist)
		}
		positive := AlbumCandidate{ArtistName: candArtist, Title: candTitle}
		if idx, ok := BestMatch(wantArtist, wantTitle, []AlbumCandidate{positive}); !ok || idx != 0 {
			t.Fatalf("false negative: want=%q/%q cand=%q/%q => (%d,%v), want (0,true)",
				wantArtist, wantTitle, candArtist, candTitle, idx, ok)
		}

		// --- Determinism over a mixed candidate list. ---
		mixed := []AlbumCandidate{
			{ArtistName: "Some Other Artist", Title: base + pick(nonStrippableSuffixes)},
			positive,
		}
		idx1, ok1 := BestMatch(wantArtist, wantTitle, mixed)
		idx2, ok2 := BestMatch(wantArtist, wantTitle, mixed)
		if idx1 != idx2 || ok1 != ok2 {
			t.Fatalf("BestMatch nondeterministic: (%d,%v) vs (%d,%v)", idx1, ok1, idx2, ok2)
		}

		// --- Negative control: a candidate whose title normalizes differently
		// must not match, whatever the artist. ---
		otherBase := pick(propertyBases)
		negative := AlbumCandidate{
			ArtistName: wantArtist,
			Title:      variantKey(rng, otherBase) + pick(nonStrippableSuffixes),
		}
		if NormalizeTitle(negative.Title) != NormalizeTitle(wantTitle) {
			if idx, ok := BestMatch(wantArtist, wantTitle, []AlbumCandidate{negative}); ok || idx != 0 {
				t.Fatalf("false positive: want=%q/%q cand=%q/%q => (%d,%v)",
					wantArtist, wantTitle, negative.ArtistName, negative.Title, idx, ok)
			}
		}
	}
}

// TestBestMatchNegativeControlBlanco is the fixed negative control from the
// property spec: "Blanco" must not match "La Reina De Blanco" even with a
// compatible (identical) artist, because the normalized titles differ.
func TestBestMatchNegativeControlBlanco(t *testing.T) {
	idx, ok := BestMatch("Blanco", "Blanco", []AlbumCandidate{
		{ArtistName: "Blanco", Title: "La Reina De Blanco"},
	})
	if ok || idx != 0 {
		t.Fatalf("BestMatch(Blanco vs La Reina De Blanco) = (%d,%v), want (0,false)", idx, ok)
	}
}

// FuzzNormalizeTitle checks that NormalizeTitle never panics, is idempotent,
// and only maps input to the empty key when the title has no letters/digits
// outside a stripped suffix (e.g. "!- Single" -> "", which BestMatch treats as
// no-match). Input with a meaningful base must never collapse to "".
func FuzzNormalizeTitle(f *testing.F) {
	seeds := []string{
		// Evidenced cases.
		"Échame la culpa - Single",
		"Échame La Culpa",
		"Desecration Smile",
		"Don't Stop",
		"AC/DC",
		"Album (Deluxe Edition)",
		"Album",
		// Boundary / suffix shapes.
		"Album (Live)",
		"Album - Single",
		"New Edition",
		"Spider-Man",
		"R&B",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := NormalizeTitle(s)
		if again := NormalizeTitle(got); again != got {
			t.Fatalf("NormalizeTitle not idempotent: %q -> %q -> %q", s, got, again)
		}
		if got == "" && normalize(stripEditionSuffix(s)) != "" {
			t.Fatalf("NormalizeTitle(%q) = %q, want \"\" only when the suffix-stripped base has no alnum", s, got)
		}
	})
}

// FuzzBestMatch checks that BestMatch never panics, is deterministic, returns
// index 0 on a miss, and returns index 0 on an empty candidate set.
func FuzzBestMatch(f *testing.F) {
	seeds := []struct{ artist, title, candArtist, candTitle string }{
		{"Luis Fonsi", "Échame la culpa - Single", "Luis Fonsi", "Échame La Culpa"},
		{"Red Hot Chili Peppers", "Desecration Smile", "Red Hot Chili Peppers", "Desecration Smile"},
		{"AC/DC", "Don't Stop", "AC/DC", "Don't Stop"},
		{"Album Artist", "Album", "Album Artist", "Album (Deluxe Edition)"},
	}
	for _, s := range seeds {
		f.Add(s.artist, s.title, s.candArtist, s.candTitle)
	}
	f.Fuzz(func(t *testing.T, wantArtist, wantTitle, candArtist, candTitle string) {
		candidates := []AlbumCandidate{{ArtistName: candArtist, Title: candTitle}}

		idx1, ok1 := BestMatch(wantArtist, wantTitle, candidates)
		idx2, ok2 := BestMatch(wantArtist, wantTitle, candidates)
		if idx1 != idx2 || ok1 != ok2 {
			t.Fatalf("BestMatch nondeterministic: (%d,%v) vs (%d,%v)", idx1, ok1, idx2, ok2)
		}
		if !ok1 && idx1 != 0 {
			t.Fatalf("BestMatch ok=false with index %d, want 0", idx1)
		}
		if idx, ok := BestMatch(wantArtist, wantTitle, nil); ok || idx != 0 {
			t.Fatalf("BestMatch(empty candidates) = (%d,%v), want (0,false)", idx, ok)
		}
	})
}
