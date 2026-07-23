package soulseek

import "testing"

func TestParseFilename_Tier1_TrackArtistTitle(t *testing.T) {
	artist, title, num := parseFilename("01 - Queen - Bohemian Rhapsody.flac")
	if artist != "Queen" {
		t.Errorf("artist = %q, want Queen", artist)
	}
	if title != "Bohemian Rhapsody" {
		t.Errorf("title = %q, want Bohemian Rhapsody", title)
	}
	if num != 1 {
		t.Errorf("num = %d, want 1", num)
	}
}

func TestParseFilename_Tier1_DotSeparator(t *testing.T) {
	artist, title, num := parseFilename("05. The Beatles - Hey Jude.mp3")
	if artist != "The Beatles" {
		t.Errorf("artist = %q", artist)
	}
	if title != "Hey Jude" {
		t.Errorf("title = %q", title)
	}
	if num != 5 {
		t.Errorf("num = %d, want 5", num)
	}
}

func TestParseFilename_Tier2_ArtistTitle(t *testing.T) {
	artist, title, num := parseFilename("Daft Punk - Get Lucky.flac")
	if artist != "Daft Punk" {
		t.Errorf("artist = %q", artist)
	}
	if title != "Get Lucky" {
		t.Errorf("title = %q", title)
	}
	if num != 0 {
		t.Errorf("num = %d, want 0", num)
	}
}

func TestParseFilename_Tier3_TrackTitle(t *testing.T) {
	_, title, num := parseFilename("04 - Stairway to Heaven.flac")
	if title != "Stairway to Heaven" {
		t.Errorf("title = %q, want Stairway to Heaven", title)
	}
	if num != 4 {
		t.Errorf("num = %d, want 4", num)
	}
}

func TestParseFilename_NoArtist_OnlyTitle(t *testing.T) {
	// Filename without " - " separator and no numeric prefix.
	artist, title, num := parseFilename("Bohemian Rhapsody.mp3")
	if artist != "" {
		t.Errorf("artist = %q, want empty", artist)
	}
	if title != "" {
		t.Errorf("title = %q, want empty (no pattern matches)", title)
	}
	if num != 0 {
		t.Errorf("num = %d, want 0", num)
	}
}

func TestParseFilename_PathContextIgnored(t *testing.T) {
	// Only basename matters for parsing. Path context is for matching engine.
	// Basename = "01 - Bohemian Rhapsody" → Tier 3 (digit prefix, no artist).
	_, title, num := parseFilename("Queen/Greatest Hits/01 - Bohemian Rhapsody.flac")
	if title != "Bohemian Rhapsody" {
		t.Errorf("title = %q, want Bohemian Rhapsody", title)
	}
	if num != 1 {
		t.Errorf("num = %d, want 1", num)
	}
}

func TestParseFilename_EmDash(t *testing.T) {
	// Em dash (U+2014) is not ASCII hyphen — no pattern matches, no split.
	_, title, _ := parseFilename("Metallica \u2014 Enter Sandman.flac")
	if title != "" {
		t.Errorf("title = %q, want empty (em dash not a separator)", title)
	}
}

func TestParseFilename_WindowsBackslash(t *testing.T) {
	// Basename = "01 - Killer Queen" → Tier 3 (no artist). Matching engine handles path.
	_, title, num := parseFilename("Music\\Queen\\01 - Killer Queen.mp3")
	if title != "Killer Queen" {
		t.Errorf("title = %q, want Killer Queen", title)
	}
	if num != 1 {
		t.Errorf("num = %d, want 1", num)
	}
}

func TestParseFilename_FeaturingWithoutArtist(t *testing.T) {
	// "02 - Song Name (feat. Guest).mp3" — Tier 3 match, no artist parsed.
	_, title, num := parseFilename("02 - Blinding Lights (feat. The Weeknd).mp3")
	if title != "Blinding Lights (feat. The Weeknd)" {
		t.Errorf("title = %q", title)
	}
	if num != 2 {
		t.Errorf("num = %d, want 2", num)
	}
}

func TestParseFilename_DoubleDigitTrack(t *testing.T) {
	artist, title, num := parseFilename("12 - Pink Floyd - Comfortably Numb.flac")
	if artist != "Pink Floyd" {
		t.Errorf("artist = %q", artist)
	}
	if title != "Comfortably Numb" {
		t.Errorf("title = %q", title)
	}
	if num != 12 {
		t.Errorf("num = %d, want 12", num)
	}
}

func TestParseFilename_JapaneseCharacters(t *testing.T) {
	artist, title, _ := parseFilename("Hikaru Utada - 初恋.flac")
	if artist != "Hikaru Utada" {
		t.Errorf("artist = %q", artist)
	}
	if title != "初恋" {
		t.Errorf("title = %q, want 初恋", title)
	}
}

// Real-world Soulseek path patterns — table-driven.
func TestParseFilename_RealWorldPaths(t *testing.T) {
	tests := []struct {
		filename     string
		wantArtist   string
		wantTitle    string
		wantTrackNum int
	}{
		// Standard Artist/Album/Track structure.
		{"Queen/Greatest Hits/01 - Bohemian Rhapsody.flac", "", "Bohemian Rhapsody", 1},
		{"Pink Floyd/The Wall/05 - Another Brick in the Wall.mp3", "", "Another Brick in the Wall", 5},

		// Flat "Artist - Title" with no path.
		{"Daft Punk - Around the World.flac", "Daft Punk", "Around the World", 0},
		{"Radiohead - Creep.mp3", "Radiohead", "Creep", 0},

		// Track number but no artist in basename — matching engine handles artist from path.
		{"Nirvana/Nevermind/01 - Smells Like Teen Spirit.flac", "", "Smells Like Teen Spirit", 1},
		{"Fleetwood Mac/Rumours/03 - Dreams.mp3", "", "Dreams", 3},

		// Album dir named "Artist - Album (Year)", basename has no artist.
		{"David Bowie - Hunky Dory (1971)/04 - Life on Mars.flac", "", "Life on Mars", 4},

		// Basename = "01 - Hooked on a Feeling" → Tier 3 (digit prefix, no artist).
		// "Guardians of the Galaxy" is a directory, not in basename.
		// Matching engine handles artist from path.
		{"Various Artists/Guardians of the Galaxy/01 - Hooked on a Feeling.mp3", "", "Hooked on a Feeling", 1},

		// Compilation path with artist in filename: correct detection.
		{"compilation/OST/01 - Queen - Don't Stop Me Now.flac", "Queen", "Don't Stop Me Now", 1},

		// Deep path — basename has no artist, matching engine handles path.
		{"Music/Lossless/Flac/Eminem/The Eminem Show/02 - Without Me.flac", "", "Without Me", 2},

		// No artist anywhere in path — Tier 3 only.
		{"Unknown Album/07 - Mystery Track.mp3", "", "Mystery Track", 7},

		// Em dash not recognized as separator — no pattern matches.
		{"Adele \u2013 Someone Like You.flac", "", "", 0},

		// Tier 3 catches digit-prefix before Tier 2.
		{"3 - Song.mp3", "", "Song", 3},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			artist, title, num := parseFilename(tt.filename)
			if artist != tt.wantArtist {
				t.Errorf("artist = %q, want %q", artist, tt.wantArtist)
			}
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
			if num != tt.wantTrackNum {
				t.Errorf("num = %d, want %d", num, tt.wantTrackNum)
			}
		})
	}
}

func TestParseYearFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"Queen/Greatest Hits (1973)/track.flac", "1973"},
		{"2020 - Album/track.mp3", "2020"},
		{"No year here/track.flac", ""},
		{"Album (1999) Remastered/track.flac", "1999"},
	}
	for _, tt := range tests {
		got := parseYearFromPath(tt.path)
		if got != tt.want {
			t.Errorf("parseYearFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
