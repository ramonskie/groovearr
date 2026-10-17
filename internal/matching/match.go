package matching

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// This file holds the pure, provider-agnostic album-result matcher extracted
// from internal/refresh. It answers one question: given a wanted library
// artist/title and a provider's album candidates, which candidate (if any)
// refers to the same release? Nothing here performs I/O, calls a provider or
// keeps mutable state, so it is usable from any layer without an import cycle
// (AGENTS §1, §3). The caller maps its own result type onto AlbumCandidate.

// AlbumCandidate is the neutral view of one provider album result the matcher
// needs. internal/refresh maps discovery.AlbumResult onto it, which keeps this
// package free of a discovery dependency (no import cycle).
type AlbumCandidate struct {
	ArtistName string
	Title      string
}

// BestMatch returns the index of the FIRST acceptable candidate and true when
// one matches: a case-, punctuation- and version-insensitive artist+title match
// wins. It applies no cross-provider ranking — the caller owns provider order.
// A title match by an unrelated artist is a clash, not a match, and is rejected,
// but a collaborating credit ("Artist A", "Artist A & Artist B", "Artist A
// feat. Artist B") is accepted because the artists are compatible. A title-only
// match is still allowed when the provider omitted the artist. It applies no
// provider-specific logic (AGENTS §1).
func BestMatch(wantArtist, wantTitle string, candidates []AlbumCandidate) (int, bool) {
	wantTitleKey := NormalizeTitle(wantTitle)
	if wantTitleKey == "" {
		return 0, false
	}
	wantArtistKey := normalizeArtist(wantArtist)

	unknownArtist := -1
	for i := range candidates {
		if NormalizeTitle(candidates[i].Title) != wantTitleKey {
			continue
		}
		// No usable library artist: a title match is the strongest signal we have.
		if wantArtistKey == "" {
			return i, true
		}
		candArtistKey := normalizeArtist(candidates[i].ArtistName)
		if !ArtistCompatible(wantArtist, candidates[i].ArtistName) {
			continue
		}
		// The provider omitted the artist: keep it as a weaker fallback rather
		// than treating "unknown" as a mismatch (and rather than letting it
		// shadow a later known-artist match).
		if candArtistKey == "" {
			if unknownArtist < 0 {
				unknownArtist = i
			}
			continue
		}
		return i, true
	}
	if unknownArtist >= 0 {
		return unknownArtist, true
	}
	return 0, false
}

// ArtistCompatible reports whether a provider artist credit can refer to the
// same act as the library artist. It accepts:
//
//   - an exact normalized credit (accents folded, case/punctuation ignored);
//   - a core alphanumeric match, so an acronym/punctuation variant such as
//     "LL Cool J" vs "L.L. Cool J" still matches;
//   - any act inside a collaboration credit, not only the first
//     ("Roscoe" in "Nelly, Roscoe", "Masta Ace" in
//     "Eminem, J-Black, Masta Ace"). Member splitting uses comma, " & ",
//     " / ", and feat/ft/featuring only; "with"/"x" stay prefix tolerance so
//     single acts ("Dancing With Wolves", "Malcolm X Trio") are not split, and a
//     compact symbol inside one act ("AC/DC") is never a boundary;
//   - an article-only containment whose only added tokens are articles/stopwords
//     ("The Cure" vs "Cure");
//   - at least TWO shared significant tokens, so a reordered/reformatted
//     collaboration still matches ("Simon & Garfunkel" vs "Garfunkel & Simon").
//
// It rejects a bare containment ("Air" vs "Air Supply", "King" vs "King
// Crimson"), a single shared generic token outside a collaboration, and an
// overlap that is only stopwords/joiners ("The Cure" vs "The Beatles"). Raw
// credits are compared for the collaboration branch because normalize() strips
// the "&", "," and "/" joiners that check relies on. It is provider-agnostic
// (AGENTS §1).
func ArtistCompatible(wantRaw, candRaw string) bool {
	want := normalizeArtist(wantRaw)
	cand := normalizeArtist(candRaw)
	if want == "" || cand == "" {
		return true
	}
	if want == cand || coreArtistMatch(wantRaw, candRaw) {
		return true
	}
	if collabContains(wantRaw, candRaw) {
		return true
	}
	if articleOnlyContains(wantRaw, candRaw) {
		return true
	}
	return shareTwoSignificantTokens(want, cand)
}

// collabContains reports whether one raw credit is a collaboration containing
// the other. A joiner must be space-delimited (" & ", " / ", ", ") or a
// whitespace-delimited word joiner ("feat", "ft", "featuring", "with", "x"); a
// compact symbol inside a single act ("AC/DC") is NOT a joiner, so "AC/DC" does
// not bind to "AC". A containment that is not a whole collaboration member is
// left to articleOnlyContains.
//
// Every member is accepted, not only the first: "Roscoe" matches
// "Nelly, Roscoe", and "Masta Ace" matches "Eminem, J-Black, Masta Ace". The
// exact title match is the other half of BestMatch, so an alias inside a
// collaboration credit is safe here.
func collabContains(aRaw, bRaw string) bool {
	a := normalizeArtist(aRaw)
	b := normalizeArtist(bRaw)
	switch {
	case containsTokenSeq(a, b):
		return collabCreditMatches(aRaw, b) || collabPrefixIs(aRaw, b)
	case containsTokenSeq(b, a):
		return collabCreditMatches(bRaw, a) || collabPrefixIs(bRaw, a)
	}
	return false
}

// collabCreditMatches reports whether shorterNorm is one whole act inside
// longerRaw's collaboration credit. A longerRaw with no joiner (one act) never
// qualifies, so compact names such as "AC/DC" are not split.
func collabCreditMatches(longerRaw, shorterNorm string) bool {
	parts := collabCreditParts(longerRaw)
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if normalizeArtist(part) == shorterNorm {
			return true
		}
	}
	return false
}

// collabCreditParts splits a raw credit into acts. Member extraction uses only
// strong joiners (comma, " & ", " / ", feat/ft/featuring); "with" and "x" are
// NOT split here because they occur inside single-act names ("Dancing With
// Wolves", "Malcolm X Trio"). A leading strong joiner after a delimiter is
// skipped, so "A, feat. B" still yields B.
func collabCreditParts(raw string) []string {
	var parts []string
	rest := strings.TrimSpace(raw)
	for rest != "" {
		idx := firstMemberJoinerIndex(rest)
		if idx < 0 {
			parts = append(parts, rest)
			break
		}
		if idx == 0 {
			if len(parts) == 0 {
				// A joiner word at the very start is the act's own name
				// ("Feat", "With Confidence"), not a collaboration boundary.
				parts = append(parts, rest)
				break
			}
			rest = dropLeadingJoiner(rest, memberJoinerWords)
			continue
		}
		parts = append(parts, strings.TrimSpace(rest[:idx]))
		rest = dropLeadingJoiner(strings.TrimSpace(rest[idx:]), memberJoinerWords)
	}
	return parts
}

// dropLeadingJoiner removes one space-delimited symbol or member-word joiner
// from the front of rest. It returns rest unchanged when no joiner is present.
func dropLeadingJoiner(rest string, words map[string]struct{}) string {
	rest = strings.TrimSpace(rest)
	switch {
	case strings.HasPrefix(rest, "&"), strings.HasPrefix(rest, "/"), strings.HasPrefix(rest, ","):
		return strings.TrimSpace(rest[1:])
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return rest
	}
	if _, ok := words[normalize(fields[0])]; ok {
		return strings.TrimSpace(strings.Join(fields[1:], " "))
	}
	return rest
}

// creditJoinerWords are whitespace-delimited tokens that join two credits for
// the broad prefix tolerance. "with" and "x" stay here so the old
// "Artist A" vs "Artist A x Artist B" prefix match keeps working.
var creditJoinerWords = map[string]struct{}{
	"feat": {}, "ft": {}, "featuring": {}, "with": {}, "x": {},
}

// memberJoinerWords are the joiners strong enough to split a credit into
// independent acts for any-member matching. "with"/"x" are deliberately absent:
// they are common inside a single act's name.
var memberJoinerWords = map[string]struct{}{
	"feat": {}, "ft": {}, "featuring": {},
}

// hasCreditJoiner reports whether a raw credit carries a SPACE-DELIMITED
// multi-credit joiner: " & ", " / ", a comma followed by a space (", "), or a
// whitespace-delimited word joiner token ("feat", "ft", "featuring", "with",
// "x"). A compact "/" or "&" inside one act ("AC/DC") is NOT a joiner.
func hasCreditJoiner(raw string) bool {
	return firstCreditJoinerIndex(raw) >= 0
}

// firstCreditJoinerIndex returns the byte index at which the first
// space-delimited multi-credit joiner begins in raw, or -1 when raw has none.
func firstCreditJoinerIndex(raw string) int {
	return firstJoinerIndex(raw, creditJoinerWords)
}

// firstMemberJoinerIndex is firstCreditJoinerIndex restricted to the strong
// member-splitting joiners (comma, " & ", " / ", feat/ft/featuring).
func firstMemberJoinerIndex(raw string) int {
	return firstJoinerIndex(raw, memberJoinerWords)
}

// firstJoinerIndex returns the byte index at which the first space-delimited
// multi-credit joiner from words begins in raw, or -1 when raw has none. Word
// joiners match as whole whitespace-delimited tokens; symbol joiners only when
// surrounded by whitespace (" & ", " / ") or, for a comma, when followed by a
// space.
func firstJoinerIndex(raw string, words map[string]struct{}) int {
	idx := -1
	for i := 0; i < len(raw); {
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		start := i
		for i < len(raw) && raw[i] != ' ' && raw[i] != '\t' {
			i++
		}
		if start >= i {
			break
		}
		if _, ok := words[normalize(raw[start:i])]; ok && (idx < 0 || start < idx) {
			idx = start
		}
	}
	for _, sep := range []string{" & ", " / ", ", "} {
		if p := strings.Index(raw, sep); p >= 0 && (idx < 0 || p < idx) {
			idx = p
		}
	}
	return idx
}

// collabPrefixIs is the broad prefix tolerance: the shorter credit is exactly
// the normalized act before the first joiner, including "with"/"x".
func collabPrefixIs(longerRaw, shorterNorm string) bool {
	idx := firstCreditJoinerIndex(longerRaw)
	if idx <= 0 {
		return false
	}
	return normalizeArtist(longerRaw[:idx]) == shorterNorm
}

// containsTokenSeq reports whether haystack contains needle as a contiguous run
// of whole tokens, so "artist a" matches "artist a artist b" but not "artist ab".
func containsTokenSeq(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(" "+haystack+" ", " "+needle+" ")
}

// articleOnlyContains reports whether one normalized credit contains the other
// as a whole-token run and the LONGER credit adds only articles/stopwords
// ("The Cure" ⊃ "Cure"). It requires the longer RAW credit to carry no
// collaboration joiner, so this branch never overlaps collabContains. A longer
// credit that adds a real word ("Air Supply" ⊅ "Air", "New Order" ⊅ "Order") is
// a different act and is rejected.
func articleOnlyContains(aRaw, bRaw string) bool {
	a := normalizeArtist(aRaw)
	b := normalizeArtist(bRaw)
	switch {
	case containsTokenSeq(a, b):
		return !hasCreditJoiner(aRaw) && stopwordExtrasOnly(a, b)
	case containsTokenSeq(b, a):
		return !hasCreditJoiner(bRaw) && stopwordExtrasOnly(b, a)
	}
	return false
}

// stopwordExtrasOnly reports whether dropping every stopword from longer leaves
// exactly shorter's tokens — i.e. longer = shorter + articles/stopwords only.
func stopwordExtrasOnly(longer, shorter string) bool {
	return equalTokens(dropStopwords(longer), strings.Fields(shorter))
}

// dropStopwords returns s's tokens with every artist stopword removed.
func dropStopwords(s string) []string {
	var out []string
	for _, t := range strings.Fields(s) {
		if _, stop := artistStopwords[t]; stop {
			continue
		}
		out = append(out, t)
	}
	return out
}

// equalTokens reports whether two token slices are equal element-wise.
func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// artistStopwords are credit words with no identifying power: articles,
// prepositions and collaboration joiners. A shared token from this set must
// never make two different artists compatible. normalize() strips the "&"
// symbol, so it never appears as a token here; it is handled as a raw joiner in
// hasCreditJoiner.
var artistStopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "and": {}, "of": {}, "for": {}, "with": {},
	"feat": {}, "ft": {}, "featuring": {}, "vs": {}, "de": {}, "la": {}, "le": {},
	"los": {}, "las": {}, "el": {}, "di": {}, "da": {},
}

// shareTwoSignificantTokens reports whether the two normalized credits share at
// least TWO distinctive tokens. Two shared significant tokens are strong
// evidence of the same act even when reordered ("Simon & Garfunkel" vs
// "Garfunkel & Simon"). A single shared token is deliberately NOT enough:
// generic words bind different acts ("King" vs "King Crimson", "Rock" vs
// "Rock Band"). Tokens are deduplicated on both sides so a repeated word cannot
// inflate the shared count.
func shareTwoSignificantTokens(a, b string) bool {
	seen := make(map[string]struct{})
	for _, t := range significantTokens(a) {
		seen[t] = struct{}{}
	}
	shared := 0
	for _, t := range significantTokens(b) {
		if _, ok := seen[t]; !ok {
			continue
		}
		shared++
		if shared >= 2 {
			return true
		}
	}
	return false
}

// significantTokens returns the distinct distinctive tokens of a normalized
// credit: tokens that are at least four runes and not stopwords.
func significantTokens(s string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, t := range strings.Fields(s) {
		if !significantArtistToken(t) {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// significantArtistToken reports whether a normalized token is distinctive
// enough to identify an act: at least four RUNES (not bytes) and not a
// stopword. Rune count keeps multi-byte credits consistent with ASCII ones.
func significantArtistToken(t string) bool {
	if utf8.RuneCountInString(t) < 4 {
		return false
	}
	_, stop := artistStopwords[t]
	return !stop
}

// normalize reduces a name to a comparison key that ignores case and every
// non-alphanumeric character (quotes, brackets, punctuation), collapsing the
// result into space-separated tokens. `"WOW"` and `WOW` both become "wow";
// `AC/DC` becomes "ac dc". It carries no provider knowledge (AGENTS §1).
func normalize(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// foldAccents decomposes accented letters and drops the combining marks, so
// "Derülo" and "Derulo" compare equal. Titles keep their accents in
// NormalizeTitle; this helper is for artist credits, where the same act is
// routinely spelled both ways.
func foldAccents(s string) string {
	if s == "" {
		return ""
	}
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)))
	out, _, _ := transform.String(t, s)
	return out
}

// normalizeArtist is normalize plus accent folding. It is the artist-side
// comparison key; title normalization stays faithful to the title bytes.
func normalizeArtist(s string) string {
	return normalize(foldAccents(s))
}

// coreArtist is the strict alphanumeric form of an artist credit: accents
// folded, lowercase, and every non-alphanumeric character removed. It catches
// punctuation/acronym variants ("L.L. Cool J" vs "LL Cool J") that tokenization
// would otherwise split into different keys.
func coreArtist(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, foldAccents(s))
}

// coreArtistMatch accepts a punctuation/acronym variant only when both credits
// have at least two tokens and the dense core is long enough. That keeps
// "LL Cool J" vs "L.L. Cool J" working while rejecting distinct short acts such
// as "Mia" vs "M.I.A." or "A" vs "A+".
func coreArtistMatch(aRaw, bRaw string) bool {
	aCore, bCore := coreArtist(aRaw), coreArtist(bRaw)
	if utf8.RuneCountInString(aCore) < 4 || aCore != bCore {
		return false
	}
	return len(strings.Fields(normalizeArtist(aRaw))) >= 2 &&
		len(strings.Fields(normalizeArtist(bRaw))) >= 2
}

// titleVersionTokens are generic SAME-CONTENT edition/format markers providers
// append to a title. Matching tolerates them so `Album` and `Album (Deluxe
// Edition)` are treated as the same release. They are deliberately limited to
// markers that do not change the materialized tracklist: performance/format
// variants (live, acoustic, instrumental, karaoke, mono, stereo), a single, or
// a different track listing are NOT here, because collapsing them would bind a
// library album to the wrong provider release. They are generic music terms,
// never provider names (AGENTS §1).
var titleVersionTokens = map[string]bool{
	"deluxe": true, "edition": true, "expanded": true, "anniversary": true,
	"remaster": true, "remastered": true,
	"bonus": true, "explicit": true, "clean": true,
}

// separatorFormatTokens are trailing format words tolerated ONLY after a
// dash/colon separator — "Album - Single", "Album - EP". They are deliberately
// NOT added to titleVersionTokens: as a delimited bracket group ("Album
// (Single)", "Album (EP)") a single/EP marks a DIFFERENT release, so collapsing
// it would bind a library album to the wrong provider release. Keeping the
// tolerance on the separator-suffix path alone is the whole point.
var separatorFormatTokens = map[string]bool{"single": true, "ep": true}

// NormalizeTitle is the title-specific comparison key: it drops a DELIMITED
// trailing edition descriptor and a featuring credit, then normalizes (case-,
// quote-, bracket- and punctuation-insensitive), so a library title with quotes
// or a delimited version suffix matches the bare provider title.
//
// It returns "" only when the title carries no letters/digits outside a stripped
// suffix — e.g. "!- Single", whose only alphanumeric token is the suffix itself.
// Callers treat "" as "no usable key": BestMatch returns no match rather than a
// false positive, so a punctuation-only title simply stays unmatched.
func NormalizeTitle(s string) string {
	base := normalize(stripEditionSuffix(s))
	if base == "" {
		return ""
	}
	trimmed := stripFeaturing(base)
	if trimmed == "" {
		return base
	}
	return trimmed
}

// TrimEditionSuffix strips ONLY a trailing delimited edition/format descriptor
// from a title — a trailing parenthetical/bracket group ("Album (Deluxe
// Edition)", "Album [Deluxe]", "Album (2020 Remaster)") or a suffix after the
// last dash/colon separator ("Album - Remastered", "Album - Single", "Album -
// EP") — and returns the title unchanged when none is present.
//
// Unlike NormalizeTitle it does NOT lowercase, strip punctuation, or drop a
// featuring credit, so it is the right input for a provider query term that must
// keep a title's case and punctuation ("Don't Stop", "R&B", "AC/DC") while still
// tolerating the edition suffix the matcher ignores. It never returns an empty
// string for a non-empty input that lacks a suffix.
func TrimEditionSuffix(s string) string {
	return stripEditionSuffix(s)
}

// stripFeaturing drops a "feat"/"ft"/"featuring" credit and everything after it.
func stripFeaturing(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		if f == "feat" || f == "ft" || f == "featuring" {
			return strings.Join(fields[:i], " ")
		}
	}
	return s
}

// stripEditionSuffix removes a trailing edition/format descriptor only when it
// was DELIMITED in the original title: a trailing parenthetical/bracket group
// ("Album (Deluxe Edition)", "Album [Deluxe]", "Album (2020 Remaster)") or a
// suffix after the last dash/colon separator ("Album - Remastered", "Album -
// Single", "Album - EP"). A bare trailing word in a plain title ("New Edition")
// is never stripped, so a legitimate title is not collapsed to a shorter,
// different one. It never returns an empty base.
func stripEditionSuffix(s string) string {
	for {
		if next, ok := stripTrailingBracketGroup(s); ok {
			s = next
			continue
		}
		if next, ok := stripSeparatorSuffix(s); ok {
			s = next
			continue
		}
		return s
	}
}

// stripTrailingBracketGroup removes a trailing "(...)" or "[...]" group whose
// inner tokens are all edition markers (or bare years) and include at least one
// real edition marker. It returns the base title and true only when such a
// group is present and a non-empty base remains.
func stripTrailingBracketGroup(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return s, false
	}
	var open byte
	switch s[len(s)-1] {
	case ')':
		open = '('
	case ']':
		open = '['
	default:
		return s, false
	}
	idx := strings.LastIndexByte(s, open)
	if idx < 0 {
		return s, false
	}
	if !isEditionGroup(s[idx+1 : len(s)-1]) {
		return s, false
	}
	base := strings.TrimSpace(s[:idx])
	if base == "" {
		return s, false
	}
	return base, true
}

// stripSeparatorSuffix removes a suffix after the last dash/colon separator
// whose tokens form an edition group, e.g. "Album - Remastered", or that is
// exactly a separator-only format word ("Album - Single", "Album - EP"). If the
// last separator's suffix is neither, the title is returned unchanged (so
// "Spider-Man" and "Album - Live" are untouched).
func stripSeparatorSuffix(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for i := len(s) - 1; i > 0; i-- {
		if s[i] != '-' && s[i] != ':' {
			continue
		}
		if !isEditionGroup(s[i+1:]) && !isSeparatorFormatSuffix(s[i+1:]) {
			return s, false
		}
		base := strings.TrimSpace(s[:i])
		if base == "" {
			return s, false
		}
		return base, true
	}
	return s, false
}

// isSeparatorFormatSuffix reports whether a separator suffix is EXACTLY one
// format word from separatorFormatTokens ("single" or "ep", case-insensitive,
// ignoring surrounding punctuation/whitespace). "Album - Single" collapses;
// "Album - Live" and "Album - Single Edit" do not.
func isSeparatorFormatSuffix(s string) bool {
	tokens := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return len(tokens) == 1 && separatorFormatTokens[tokens[0]]
}

// isEditionGroup reports whether every whitespace/punctuation-separated token
// is an edition marker or a bare year, and at least one is an edition marker.
// A lone performance/format word ("live", "single") or a bare year fails, so
// "Album (Live)", "Album (Single)" and "Album (2020)" are not collapsed.
func isEditionGroup(s string) bool {
	tokens := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(tokens) == 0 {
		return false
	}
	marker := false
	for _, t := range tokens {
		switch {
		case titleVersionTokens[t]:
			marker = true
		case isYearToken(t):
		default:
			return false
		}
	}
	return marker
}

// isYearToken reports whether t is a plausible 4-digit year (1900-2099), the
// only non-marker token allowed inside a delimited edition group so
// "(2020 Remaster)" collapses.
func isYearToken(t string) bool {
	if len(t) != 4 {
		return false
	}
	for _, r := range t {
		if r < '0' || r > '9' {
			return false
		}
	}
	n := int(t[0]-'0')*1000 + int(t[1]-'0')*100 + int(t[2]-'0')*10 + int(t[3]-'0')
	return n >= 1900 && n <= 2099
}
