package matching

import "testing"

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Bare and case/punctuation folding.
		{"Échame La Culpa", "échame la culpa"},
		{"Hello World", "hello world"},
		{"Spider-Man", "spider man"}, // hyphen collapses to a space, never to "spider"
		// Delimited edition groups collapse only when a real marker is present.
		{"Album (Deluxe Edition)", "album"},
		{"Album [Deluxe]", "album"},
		{"Album - Remastered", "album"},
		{"Album (2020 Remaster)", "album"},
		{"Album (Deluxe Edition) [Remastered]", "album"},
		// A bare trailing word is never stripped.
		{"New Edition", "new edition"},
		{"New", "new"},
		// Featuring credits are dropped.
		{"Song feat. Artist", "song"},
		{"Song ft. Someone", "song"},
		{"Song (featuring Guest)", "song"},
		// Empty / punctuation-only inputs never yield a non-empty key.
		{"", ""},
		{"!!!", ""},
	}
	for _, tt := range tests {
		if got := NormalizeTitle(tt.input); got != tt.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestNormalizeTitleSeparatorSingleEP covers the added tolerance: a
// separator-only " - Single" / " - EP" suffix collapses, but the same word as a
// bracket group or an unseparated trailing word does NOT.
func TestNormalizeTitleSeparatorSingleEP(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"dash single", "Album - Single", "album"},
		{"colon single", "Album: Single", "album"},
		{"dash ep", "Album - EP", "album"},
		{"colon ep uppercase", "Album: EP", "album"},
		{"dash single case-insensitive", "Album - sInGlE", "album"},
		// The tolerance is separator-path-only.
		{"bracket single not stripped", "Album (Single)", "album single"},
		{"bracket ep not stripped", "Album (EP)", "album ep"},
		{"bare trailing single not stripped", "Album Single", "album single"},
		{"multi-word suffix not all-single/ep", "Album - Single Edit", "album single edit"},
	}
	for _, tt := range tests {
		if got := NormalizeTitle(tt.input); got != tt.want {
			t.Errorf("%s: NormalizeTitle(%q) = %q, want %q", tt.name, tt.input, got, tt.want)
		}
	}
}

// TestNormalizeTitleDoesNotCollapseLiveOrRemixes documents the chosen behavior
// for performance/format words that are deliberately NOT edition markers.
// "live" and "remixes" are not in the version-token set, so they are retained
// wherever they appear — including inside a bracket group (where the edition
// rules only strip a group composed of real markers/years). Collapsing them
// would bind a library album to a different provider release, so the test
// asserts they are NOT stripped rather than silently collapsing them.
func TestNormalizeTitleDoesNotCollapseLiveOrRemixes(t *testing.T) {
	album := NormalizeTitle("Album")
	tests := []struct {
		input string
		want  string
	}{
		{"Album (Live)", "album live"},
		{"Album (Remixes)", "album remixes"},
		{"Album - Live", "album live"},
		{"Album - Remixes", "album remixes"},
	}
	for _, tt := range tests {
		got := NormalizeTitle(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
		if got == album {
			t.Errorf("NormalizeTitle(%q) collapsed to %q but must not", tt.input, album)
		}
	}
}

// TestNormalizeTitleLuisFonsiSingleSuffix is the regression that motivated the
// separator tolerance: a "- Single" suffix must normalize the same as the bare
// library title.
func TestNormalizeTitleLuisFonsiSingleSuffix(t *testing.T) {
	withSuffix := NormalizeTitle("Échame la culpa - Single")
	bare := NormalizeTitle("Échame La Culpa")
	if withSuffix != bare {
		t.Fatalf("NormalizeTitle with suffix = %q, bare = %q; want equal", withSuffix, bare)
	}
}

// TestTrimEditionSuffix locks the provider-query helper: it strips ONLY a
// delimited edition/format suffix while preserving the title's case and
// punctuation, so a query term keeps "Don't Stop", "R&B" and "AC/DC" verbatim
// (unlike NormalizeTitle, which folds them into a comparison key).
func TestTrimEditionSuffix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no suffix, apostrophe preserved", "Don't Stop", "Don't Stop"},
		{"separator single stripped, apostrophe preserved", "Don't Stop - Single", "Don't Stop"},
		{"ampersand preserved", "R&B", "R&B"},
		{"slash preserved", "AC/DC", "AC/DC"},
		{"accent and case preserved", "Échame la culpa - Single", "Échame la culpa"},
		{"bracket edition stripped", "Album (Deluxe Edition)", "Album"},
		{"bracket edition unseparated", "Album [Deluxe]", "Album"},
		{"separator remaster stripped", "Album - Remastered", "Album"},
		{"separator ep stripped", "Album - EP", "Album"},
		// A dash inside a single word is not a separator, and a non-edition
		// bracket group is retained.
		{"hyphenated title untouched", "Spider-Man", "Spider-Man"},
		{"live group not an edition", "Album (Live)", "Album (Live)"},
		{"bare trailing word untouched", "New Edition", "New Edition"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TrimEditionSuffix(tt.input); got != tt.want {
				t.Errorf("TrimEditionSuffix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestArtistCompatible(t *testing.T) {
	tests := []struct {
		name      string
		want      string
		candidate string
		ok        bool
	}{
		{"exact", "Daft Punk", "Daft Punk", true},
		{"exact case/punctuation folded", "AC/DC", "AC DC", true},
		{"accents are not folded (faithful port)", "Sigur Rós", "sigur ros", false},
		{"collaboration symbol", "Artist A", "Artist A & Artist B", true},
		{"collaboration symbol reverse", "Artist A & Artist B", "Artist A", true},
		{"collaboration feat", "Artist A", "Artist A feat. Artist B", true},
		{"collaboration featuring", "Artist A", "Artist A featuring Artist B", true},
		{"article-only", "The Cure", "Cure", true},
		{"article-only reverse", "Cure", "The Cure", true},
		{"reorder two significant tokens", "Simon & Garfunkel", "Garfunkel & Simon", true},
		{"empty want is compatible", "", "Anyone", true},
		{"empty candidate is compatible", "Anyone", "", true},
		{"reject bare containment", "Air", "Air Supply", false},
		{"reject single generic token", "King", "King Crimson", false},
		{"reject single shared token", "Rock", "Rock Band", false},
		{"reject single shared token, different act", "The Band", "Band of Gypsys", false},
		{"reject stopword-only overlap", "The Cure", "The Beatles", false},
		{"reject stopword-only overlap", "Simon and Garfunkel", "Hall and Oates", false},
		{"reject different acts", "Daft Punk", "The Chemical Brothers", false},
		{"reject compact symbol prefix", "AC", "AC/DC", false},
	}
	for _, tt := range tests {
		if got := ArtistCompatible(tt.want, tt.candidate); got != tt.ok {
			t.Errorf("%s: ArtistCompatible(%q, %q) = %v, want %v", tt.name, tt.want, tt.candidate, got, tt.ok)
		}
	}
}

func TestBestMatch(t *testing.T) {
	tests := []struct {
		name       string
		wantArtist string
		wantTitle  string
		candidates []AlbumCandidate
		wantIdx    int
		wantOK     bool
	}{
		{
			name:       "luis fonsi single suffix",
			wantArtist: "Luis Fonsi",
			wantTitle:  "Échame la culpa - Single",
			candidates: []AlbumCandidate{{ArtistName: "Luis Fonsi", Title: "Échame La Culpa"}},
			wantIdx:    0,
			wantOK:     true,
		},
		{
			name:       "jamiroquai multi-word exact",
			wantArtist: "Jamiroquai",
			wantTitle:  "Seven Days in Sunny June",
			candidates: []AlbumCandidate{{ArtistName: "Jamiroquai", Title: "Seven Days in Sunny June"}},
			wantIdx:    0,
			wantOK:     true,
		},
		{
			name:       "four-word artist exact match",
			wantArtist: "Red Hot Chili Peppers",
			wantTitle:  "Desecration Smile",
			candidates: []AlbumCandidate{{ArtistName: "Red Hot Chili Peppers", Title: "Desecration Smile"}},
			wantIdx:    0,
			wantOK:     true,
		},
		{
			name:       "katy perry single release-group",
			wantArtist: "Katy Perry",
			wantTitle:  "Unconditionally",
			candidates: []AlbumCandidate{{ArtistName: "Katy Perry", Title: "Unconditionally"}},
			wantIdx:    0,
			wantOK:     true,
		},
		{
			name:       "provider title carries edition suffix",
			wantArtist: "Album Artist",
			wantTitle:  "Album",
			candidates: []AlbumCandidate{{ArtistName: "Album Artist", Title: "Album (Deluxe Edition)"}},
			wantIdx:    0,
			wantOK:     true,
		},
		{
			name:       "collaborating credit accepted",
			wantArtist: "Artist A",
			wantTitle:  "Track",
			candidates: []AlbumCandidate{{ArtistName: "Artist A & Artist B", Title: "Track"}},
			wantIdx:    0,
			wantOK:     true,
		},
		{
			name:       "title match by unrelated artist is a clash",
			wantArtist: "Katy Perry",
			wantTitle:  "Unconditionally",
			candidates: []AlbumCandidate{{ArtistName: "Some Other Artist", Title: "Unconditionally"}},
			wantOK:     false,
		},
		{
			name:       "second candidate wins after a clash",
			wantArtist: "Luis Fonsi",
			wantTitle:  "Échame La Culpa",
			candidates: []AlbumCandidate{
				{ArtistName: "Some Other Artist", Title: "Échame La Culpa"},
				{ArtistName: "Luis Fonsi", Title: "Échame La Culpa"},
			},
			wantIdx: 1,
			wantOK:  true,
		},
		{
			name:       "omitted artist is a fallback",
			wantArtist: "Luis Fonsi",
			wantTitle:  "Échame La Culpa",
			candidates: []AlbumCandidate{{ArtistName: "", Title: "Échame La Culpa"}},
			wantIdx:    0,
			wantOK:     true,
		},
		{
			name:       "known artist preferred over earlier omitted artist",
			wantArtist: "Luis Fonsi",
			wantTitle:  "Échame La Culpa",
			candidates: []AlbumCandidate{
				{ArtistName: "", Title: "Échame La Culpa"},
				{ArtistName: "Luis Fonsi", Title: "Échame La Culpa"},
			},
			wantIdx: 1,
			wantOK:  true,
		},
		{
			name:       "empty library artist takes first title match",
			wantArtist: "",
			wantTitle:  "Unconditionally",
			candidates: []AlbumCandidate{{ArtistName: "Some Other Artist", Title: "Unconditionally"}},
			wantIdx:    0,
			wantOK:     true,
		},
		{
			name:       "empty wanted title never matches",
			wantArtist: "Katy Perry",
			wantTitle:  "",
			candidates: []AlbumCandidate{{ArtistName: "Katy Perry", Title: "Unconditionally"}},
			wantOK:     false,
		},
		{
			name:       "no candidates",
			wantArtist: "Katy Perry",
			wantTitle:  "Unconditionally",
			candidates: nil,
			wantOK:     false,
		},
	}
	for _, tt := range tests {
		idx, ok := BestMatch(tt.wantArtist, tt.wantTitle, tt.candidates)
		if ok != tt.wantOK {
			t.Errorf("%s: BestMatch returned ok=%v, want %v", tt.name, ok, tt.wantOK)
			continue
		}
		if ok && idx != tt.wantIdx {
			t.Errorf("%s: BestMatch returned idx=%d, want %d", tt.name, idx, tt.wantIdx)
		}
	}
}
