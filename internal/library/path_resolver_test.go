package library

import "testing"

func TestPathResolver_LidarrDefault(t *testing.T) {
	r := NewPathResolver("{artist}/{album}/{track:00} - {title}")
	got := r.Resolve(ResolveArgs{
		Artist:   "Nirvana",
		Album:    "Nevermind",
		TrackNum: 1,
		Title:    "Smells Like Teen Spirit",
		Ext:      "flac",
	})
	want := "Nirvana/Nevermind/01 - Smells Like Teen Spirit"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPathResolver_WithYear(t *testing.T) {
	r := NewPathResolver("{artist}/{album} ({year})/{track:02d} - {title}")
	got := r.Resolve(ResolveArgs{
		Artist:   "Daft Punk",
		Album:    "Random Access Memories",
		Year:     2013,
		TrackNum: 5,
		Title:    "Get Lucky",
		Ext:      "flac",
	})
	want := "Daft Punk/Random Access Memories (2013)/05 - Get Lucky"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPathResolver_NoYear(t *testing.T) {
	r := NewPathResolver("{artist}/{album} ({year})/{track:00} - {title}")
	got := r.Resolve(ResolveArgs{
		Artist:   "Artist",
		Album:    "Single",
		TrackNum: 1,
		Title:    "Song",
		Ext:      "mp3",
	})
	want := "Artist/Single/01 - Song"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPathResolver_MultiDisc(t *testing.T) {
	r := NewPathResolver("{artist}/{album}/{disc:00}-{track:00} - {title}")
	got := r.Resolve(ResolveArgs{
		Artist:   "Pink Floyd",
		Album:    "The Wall",
		DiscNum:  2,
		TrackNum: 3,
		Title:    "Another Brick in the Wall",
		Ext:      "flac",
	})
	want := "Pink Floyd/The Wall/02-03 - Another Brick in the Wall"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPathResolver_SingleDiscOmitted(t *testing.T) {
	r := NewPathResolver("{artist}/{album}/{disc:00}-{track:00} - {title}")
	got := r.Resolve(ResolveArgs{
		Artist:   "Beatles",
		Album:    "Abbey Road",
		DiscNum:  1, // single disc — should omit disc prefix
		TrackNum: 1,
		Title:    "Come Together",
		Ext:      "mp3",
	})
	want := "Beatles/Abbey Road/01 - Come Together"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPathResolver_SanitizeSpecialChars(t *testing.T) {
	r := NewPathResolver("{artist}/{album}/{track:00} - {title}")
	got := r.Resolve(ResolveArgs{
		Artist:   `AC/DC`,
		Album:    `Back in Black`,
		TrackNum: 1,
		Title:    `Hells Bells`,
		Ext:      "flac",
	})
	want := "AC-DC/Back in Black/01 - Hells Bells"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPathResolver_EmptyDefault(t *testing.T) {
	r := NewPathResolver("")
	got := r.Resolve(ResolveArgs{
		Artist:   "Artist",
		Album:    "Album",
		TrackNum: 1,
		Title:    "Track",
		Ext:      "mp3",
	})
	want := "Artist/Album/01 - Track"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPathResolver_AlbumType(t *testing.T) {
	r := NewPathResolver("{album_type}/{artist}/{album}/{track:00} - {title}")
	got := r.Resolve(ResolveArgs{
		Artist:    "Artist",
		Album:     "Greatest Hits",
		AlbumType: "Compilation",
		TrackNum:  1,
		Title:     "Hit Song",
		Ext:       "flac",
	})
	want := "Compilation/Artist/Greatest Hits/01 - Hit Song"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestParsePath_MultiDisc(t *testing.T) {
	track, artist, album := parsePath("Pink Floyd/The Wall/CD2/05 - Another Brick.flac")
	if artist != "Pink Floyd" {
		t.Errorf("artist = %q, want Pink Floyd", artist)
	}
	if album != "The Wall" {
		t.Errorf("album = %q, want The Wall", album)
	}
	if track != "Another Brick" {
		t.Errorf("track = %q, want Another Brick", track)
	}
}

func TestParsePath_Disc1(t *testing.T) {
	// CD1 should also be filtered.
	track, artist, album := parsePath("Beatles/Abbey Road/CD1/01 - Come Together.mp3")
	if artist != "Beatles" {
		t.Errorf("artist = %q, want Beatles", artist)
	}
	if album != "Abbey Road" {
		t.Errorf("album = %q, want Abbey Road", album)
	}
	if track != "Come Together" {
		t.Errorf("track = %q, want Come Together", track)
	}
}
