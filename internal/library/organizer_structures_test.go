package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

// writeAudio creates a fake audio file at root/rel and returns its absolute path.
func writeAudio(t *testing.T, root, rel string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// organizeFixture wires a single track into a mock store and runs one Organize call.
func organizeFixture(t *testing.T, folderTemplate, compilationTemplate, root string, track domain.Track, artist, album string, year int, dryRun bool) (OrganizeResult, *mockStore) {
	t.Helper()
	store := &mockStore{
		artists: map[string]int64{},
		albums:  map[string]int64{},
		tracks:  []domain.Track{track},
	}
	org := NewOrganizer(folderTemplate, compilationTemplate, root, store, testLogger())
	res, err := org.Organize(t.Context(), &store.tracks[0], artist, album, year, "", dryRun)
	if err != nil {
		t.Fatalf("organize: %v", err)
	}
	return res, store
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("expected %q to exist: %v", p, err)
	}
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err == nil {
		t.Errorf("expected %q to not exist", p)
	}
}

// TestOrganizerLibraryStructures replays the real folder layouts found in the
// SMB library (smb://ramons-cloud.local/data/media/music): flat artist folders
// (10cc, 2Pac, 50 Cent), download-style filenames, nested artist/album folders
// (Aaliyah, 4 Tune Fairytales), and compilations (Various Artists).
func TestOrganizerLibraryStructures(t *testing.T) {
	defaultTemplate := "{artist}/{album} ({year})/{track:02d} - {title}"

	t.Run("flat artist folder organizes into nested layout", func(t *testing.T) {
		// Real shape: /music/10cc/01 24 Hours.mp3
		root := t.TempDir()
		oldPath := writeAudio(t, root, "10cc/01 24 Hours.mp3")
		// cover + portrait in the flat folder travel along.
		writeAudio(t, root, "10cc/cover.jpg")
		writeAudio(t, root, "10cc/artist.jpg")

		res, store := organizeFixture(t, defaultTemplate, testCompilationTemplate, root,
			domain.Track{ID: 1, Title: "24 Hours", TrackNumber: 1, FilePath: oldPath},
			"10cc", "24 Hours", 1974, false)

		want := filepath.Join(root, "10cc", "24 Hours (1974)", "01 - 24 Hours.mp3")
		if !res.Moved {
			t.Fatalf("expected move, got skipped=%q", res.Skipped)
		}
		mustExist(t, want)
		mustNotExist(t, oldPath)
		mustExist(t, filepath.Join(root, "10cc", "24 Hours (1974)", "cover.jpg"))
		mustExist(t, filepath.Join(root, "10cc", "artist.jpg"))
		if store.tracks[0].FilePath != want {
			t.Errorf("DB path = %q, want %q", store.tracks[0].FilePath, want)
		}
	})

	t.Run("messy download filename organizes by DB metadata", func(t *testing.T) {
		// Real shape: /music/10cc/01-10cc-the_wall_street_shuffle-d87e11fe.mp3
		// The filename is garbage; the DB holds the real metadata.
		root := t.TempDir()
		oldPath := writeAudio(t, root, "10cc/01-10cc-the_wall_street_shuffle-d87e11fe.mp3")

		res, _ := organizeFixture(t, defaultTemplate, testCompilationTemplate, root,
			domain.Track{ID: 2, Title: "The Wall Street Shuffle", TrackNumber: 1, FilePath: oldPath},
			"10cc", "Sheet Music", 1974, false)

		want := filepath.Join(root, "10cc", "Sheet Music (1974)", "01 - The Wall Street Shuffle.mp3")
		if !res.Moved {
			t.Fatalf("expected move, got skipped=%q", res.Skipped)
		}
		mustExist(t, want)
		mustNotExist(t, oldPath)
	})

	t.Run("nested folder without year gains year from template", func(t *testing.T) {
		// Real shape: /music/Aaliyah/Romeo Must Die (Original Soundtrack)/01 - Aaliyah- Try Again.flac
		root := t.TempDir()
		oldPath := writeAudio(t, root, "Aaliyah/Romeo Must Die (Original Soundtrack)/01 - Aaliyah- Try Again.flac")

		res, _ := organizeFixture(t, defaultTemplate, testCompilationTemplate, root,
			domain.Track{ID: 3, Title: "Try Again", TrackNumber: 1, FilePath: oldPath},
			"Aaliyah", "Romeo Must Die (Original Soundtrack)", 2001, false)

		want := filepath.Join(root, "Aaliyah", "Romeo Must Die (Original Soundtrack) (2001)", "01 - Try Again.flac")
		if !res.Moved {
			t.Fatalf("expected move, got skipped=%q", res.Skipped)
		}
		mustExist(t, want)
	})

	t.Run("nested folder already in template layout stays in place", func(t *testing.T) {
		// Aaliyah once organized: /music/Aaliyah/Romeo Must Die (Original Soundtrack) (2001)/01 - Try Again.flac
		root := t.TempDir()
		inPlace := writeAudio(t, root, "Aaliyah/Romeo Must Die (Original Soundtrack) (2001)/01 - Try Again.flac")

		res, _ := organizeFixture(t, defaultTemplate, testCompilationTemplate, root,
			domain.Track{ID: 4, Title: "Try Again", TrackNumber: 1, FilePath: inPlace},
			"Aaliyah", "Romeo Must Die (Original Soundtrack)", 2001, false)

		if res.Moved || res.WouldMove {
			t.Errorf("in-place track should not move (res=%+v)", res)
		}
		if res.Skipped != "in place" {
			t.Errorf("Skipped = %q, want %q", res.Skipped, "in place")
		}
	})

	t.Run("compilation track stays under Various Artists", func(t *testing.T) {
		// Real shape: /music/Various Artists/Best of 90s/01 - 2Pac - California Love.flac
		// "Best of 90s" is a VA compilation — the track must NOT be scattered
		// into a fake 2Pac album. The compilation template keeps it under the
		// VA album with the performer in the filename.
		root := t.TempDir()
		oldPath := writeAudio(t, root, "Various Artists/Best of 90s/01 - 2Pac - California Love.flac")

		res, _ := organizeFixture(t, defaultTemplate, testCompilationTemplate, root,
			domain.Track{ID: 5, Title: "California Love", TrackNumber: 1, FilePath: oldPath},
			"2Pac", "Best of 90s", 1995, false)

		want := filepath.Join(root, "Various Artists", "Best of 90s (1995)", "01. 2Pac - California Love.flac")
		if !res.Moved {
			t.Fatalf("expected move, got skipped=%q", res.Skipped)
		}
		mustExist(t, want)
		mustNotExist(t, oldPath)
		// It must not land in a (bogus) 2Pac album folder.
		mustNotExist(t, filepath.Join(root, "2Pac", "Best of 90s (1995)"))
	})

	t.Run("compilation by AlbumType even outside a VA folder", func(t *testing.T) {
		// A download-imported compilation carries AlbumType=compilation even if
		// it sits in a regular artist-shaped folder.
		root := t.TempDir()
		oldPath := writeAudio(t, root, "Now That's What I Call Music 90/01 - Various - Track.flac")
		store := &mockStore{
			artists: map[string]int64{},
			albums:  map[string]int64{},
			tracks:  []domain.Track{{ID: 9, Title: "Track", TrackNumber: 1, FilePath: oldPath}},
		}
		org := NewOrganizer(defaultTemplate, testCompilationTemplate, root, store, testLogger())
		res, err := org.Organize(t.Context(), &store.tracks[0], "Various", "Now That's What I Call Music 90", 1990, string(domain.AlbumTypeCompilation), false)
		if err != nil {
			t.Fatal(err)
		}

		want := filepath.Join(root, "Various Artists", "Now That's What I Call Music 90 (1990)", "01. Various - Track.flac")
		if !res.Moved {
			t.Fatalf("expected move, got skipped=%q", res.Skipped)
		}
		mustExist(t, want)
		mustNotExist(t, oldPath)
	})

	t.Run("multi-disc album folds Disc N into the album folder", func(t *testing.T) {
		// Source: /music/The Beatles/White Album/Disc 2/01 - Revolution.flac
		// Disc 2 detection requires a sibling disc dir.
		root := t.TempDir()
		writeAudio(t, root, "The Beatles/White Album/Disc 1/01 - Back in the USSR.flac")
		oldPath := writeAudio(t, root, "The Beatles/White Album/Disc 2/01 - Revolution.flac")

		res, _ := organizeFixture(t, defaultTemplate, testCompilationTemplate, root,
			domain.Track{ID: 6, Title: "Revolution", TrackNumber: 1, DiscNumber: 2, FilePath: oldPath},
			"The Beatles", "White Album", 1968, false)

		want := filepath.Join(root, "The Beatles", "White Album (1968)", "01 - Revolution.flac")
		if !res.Moved {
			t.Fatalf("expected move, got skipped=%q", res.Skipped)
		}
		mustExist(t, want)
		mustNotExist(t, oldPath)
	})

	t.Run("track loose in the library root organizes too", func(t *testing.T) {
		root := t.TempDir()
		oldPath := writeAudio(t, root, "01 - Track.flac")

		res, _ := organizeFixture(t, defaultTemplate, testCompilationTemplate, root,
			domain.Track{ID: 7, Title: "Track", TrackNumber: 1, FilePath: oldPath},
			"Artist", "Album", 2020, false)

		want := filepath.Join(root, "Artist", "Album (2020)", "01 - Track.flac")
		if !res.Moved {
			t.Fatalf("expected move, got skipped=%q", res.Skipped)
		}
		mustExist(t, want)
	})

	t.Run("track outside the library root is skipped", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "01 - Track.flac")
		os.WriteFile(outside, []byte("audio"), 0o644)

		res, _ := organizeFixture(t, defaultTemplate, testCompilationTemplate, root,
			domain.Track{ID: 8, Title: "Track", TrackNumber: 1, FilePath: outside},
			"Artist", "Album", 2020, false)

		if res.Moved {
			t.Error("track outside library root must not be moved")
		}
		if res.Skipped != "outside library" {
			t.Errorf("Skipped = %q, want %q", res.Skipped, "outside library")
		}
		mustExist(t, outside)
	})
}

// TestOrganizerCommonTemplates verifies the folder templates used by the most
// common music-collection layouts. Each case starts from the same flat source
// file and asserts the exact target path the template produces.
func TestOrganizerCommonTemplates(t *testing.T) {
	// Source fixture for every template: a flat track with complete metadata.
	baseTrack := func(t *testing.T, root string) (domain.Track, string) {
		oldPath := writeAudio(t, root, "Daft Punk/02 Get Lucky.flac")
		return domain.Track{ID: 1, Title: "Get Lucky", TrackNumber: 2, FilePath: oldPath}, oldPath
	}

	tests := []struct {
		name     string
		template string
		wantRel  string
	}{
		{
			name:     "groovearr default artist/album-year/track",
			template: "{artist}/{album} ({year})/{track:02d} - {title}",
			wantRel:  "Daft Punk/Random Access Memories (2013)/02 - Get Lucky.flac",
		},
		{
			name:     "lidarr default",
			template: "{artist}/{album}/{track:00} - {title}",
			wantRel:  "Daft Punk/Random Access Memories/02 - Get Lucky.flac",
		},
		{
			name:     "numbered dot",
			template: "{artist}/{album}/{track:02d}. {title}",
			wantRel:  "Daft Punk/Random Access Memories/02. Get Lucky.flac",
		},
		{
			name:     "artist-dash-album",
			template: "{artist} - {album}/{track:02d} {title}",
			wantRel:  "Daft Punk - Random Access Memories/02 Get Lucky.flac",
		},
		{
			name:     "plain no track number",
			template: "{artist}/{album}/{title}",
			wantRel:  "Daft Punk/Random Access Memories/Get Lucky.flac",
		},
		{
			name:     "musicbrainz picard style with disc",
			template: "{artist}/{album} ({year})/{disc}-{track:00} {title}",
			wantRel:  "Daft Punk/Random Access Memories (2013)/02 Get Lucky.flac",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			track, oldPath := baseTrack(t, root)
			want := filepath.Join(root, tt.wantRel)

			res, store := organizeFixture(t, tt.template, testCompilationTemplate, root, track, "Daft Punk", "Random Access Memories", 2013, false)
			if !res.Moved {
				t.Fatalf("expected move, got skipped=%q", res.Skipped)
			}
			if res.To != want {
				t.Errorf("target = %q, want %q", res.To, want)
			}
			mustExist(t, want)
			mustNotExist(t, oldPath)
			if store.tracks[0].FilePath != want {
				t.Errorf("DB path = %q, want %q", store.tracks[0].FilePath, want)
			}
		})
	}

	// The disc token disappears for single-disc tracks even in disc-aware
	// templates.
	t.Run("disc token omitted for single-disc", func(t *testing.T) {
		root := t.TempDir()
		track, oldPath := baseTrack(t, root)
		track.DiscNumber = 0 // single disc
		want := filepath.Join(root, "Daft Punk", "Random Access Memories (2013)", "02 Get Lucky.flac")

		res, _ := organizeFixture(t, "{artist}/{album} ({year})/{disc}-{track:00} {title}", testCompilationTemplate, root, track, "Daft Punk", "Random Access Memories", 2013, false)
		if !res.Moved {
			t.Fatalf("expected move, got skipped=%q", res.Skipped)
		}
		mustExist(t, want)
		mustNotExist(t, oldPath)
	})

	// Same templates, but the source is already in template layout → in place.
	t.Run("in-place detection across templates", func(t *testing.T) {
		inPlaceCases := []struct {
			template string
			rel      string
		}{
			{"{artist}/{album} ({year})/{track:02d} - {title}", "Daft Punk/Random Access Memories (2013)/02 - Get Lucky.flac"},
			{"{artist}/{album}/{track:00} - {title}", "Daft Punk/Random Access Memories/02 - Get Lucky.flac"},
			{"{artist}/{album}/{title}", "Daft Punk/Random Access Memories/Get Lucky.flac"},
		}
		for _, c := range inPlaceCases {
			root := t.TempDir()
			inPlace := writeAudio(t, root, c.rel)
			track := domain.Track{ID: 1, Title: "Get Lucky", TrackNumber: 2, FilePath: inPlace}
			res, _ := organizeFixture(t, c.template, testCompilationTemplate, root, track, "Daft Punk", "Random Access Memories", 2013, false)
			if res.Moved || res.WouldMove {
				t.Errorf("template %q: in-place track should not move (res=%+v)", c.template, res)
			}
			if res.Skipped != "in place" {
				t.Errorf("template %q: Skipped = %q, want %q", c.template, res.Skipped, "in place")
			}
		}
	})
}
