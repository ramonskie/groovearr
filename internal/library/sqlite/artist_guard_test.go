package sqlite

import (
	"context"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

func TestStore_ImportTrack_MultiArtistFoldsToPrimary(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Seed the main artist, then import tracks tagged with featured artists.
	if _, err := store.UpsertArtist(ctx, &domain.Artist{Name: "2Pac"}); err != nil {
		t.Fatal(err)
	}

	trackIDs := make([]int64, 0, 2)
	for _, artistName := range []string{
		"2Pac feat. Anthony Hamilton",
		"2Pac feat. Jazze Pha, T.I. & Johntá Austin",
	} {
		id, err := store.ImportTrack(ctx, &domain.Track{Title: "track"}, artistName, "Album", 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		trackIDs = append(trackIDs, id)
	}

	// No new artist may exist for the multi-artist strings.
	for _, name := range []string{"2Pac feat. Anthony Hamilton", "2Pac feat. Jazze Pha, T.I. & Johntá Austin"} {
		got, err := store.GetArtistByName(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("expected no artist %q, got id %d", name, got.ID)
		}
	}

	// Both tracks resolve to the seeded "2Pac".
	main, err := store.GetArtistByName(ctx, "2Pac")
	if err != nil {
		t.Fatal(err)
	}
	if main == nil {
		t.Fatal("main artist 2Pac missing")
	}
	for _, id := range trackIDs {
		tr, err := store.GetTrack(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if tr.ArtistID != main.ID {
			t.Fatalf("track %d artist_id = %d, want %d (2Pac)", id, tr.ArtistID, main.ID)
		}
	}
}

func TestStore_ImportTrack_FeatCreatesPrimaryWhenMissing(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// No main artist pre-seeded: a feat-marked track must create the primary.
	trackID, err := store.ImportTrack(ctx, &domain.Track{Title: "solo"}, "Beck Feat. Jay-Z & Pharrell Williams", "Album", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	main, err := store.GetArtistByName(ctx, "Beck")
	if err != nil {
		t.Fatal(err)
	}
	if main == nil {
		t.Fatal("expected primary artist Beck to be created")
	}
	tr, err := store.GetTrack(ctx, trackID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.ArtistID != main.ID {
		t.Fatalf("track artist_id = %d, want %d (Beck)", tr.ArtistID, main.ID)
	}

	if got, _ := store.GetArtistByName(ctx, "Beck Feat. Jay-Z & Pharrell Williams"); got != nil {
		t.Fatalf("feat-marked name must not create its own artist, got id %d", got.ID)
	}
}

func TestStore_ImportTrack_RealBandKeptWhenPrimaryUnknown(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// "Chaka Demus & Pliers" is a real duo. With no standalone "Chaka Demus"
	// in the library, the full band name must be preserved, not split.
	trackID, err := store.ImportTrack(ctx, &domain.Track{Title: "Tease Me"}, "Chaka Demus & Pliers", "Album", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	band, err := store.GetArtistByName(ctx, "Chaka Demus & Pliers")
	if err != nil {
		t.Fatal(err)
	}
	if band == nil {
		t.Fatal("expected full band name artist to exist")
	}
	tr, err := store.GetTrack(ctx, trackID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.ArtistID != band.ID {
		t.Fatalf("track artist_id = %d, want %d (band)", tr.ArtistID, band.ID)
	}

	if got, _ := store.GetArtistByName(ctx, "Chaka Demus"); got != nil {
		t.Fatalf("must not fabricate standalone %q, got id %d", "Chaka Demus", got.ID)
	}
}

func TestStore_ImportTrack_RealBandKeptWhenMemberExists(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// The reviewer's exact scenario: the library already has an artist named
	// "Simon" (Carly Simon). Importing a "Simon & Garfunkel" track must file it
	// under the full band name — "Simon & Garfunkel" is a real artist entity
	// known to MusicBrainz, not a collab to fold onto the lone "Simon" row.
	if _, err := store.UpsertArtist(ctx, &domain.Artist{Name: "Simon"}); err != nil {
		t.Fatal(err)
	}
	trackID, err := store.ImportTrack(ctx, &domain.Track{Title: "The Sound of Silence"}, "Simon & Garfunkel", "Bookends", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	band, err := store.GetArtistByName(ctx, "Simon & Garfunkel")
	if err != nil {
		t.Fatal(err)
	}
	if band == nil {
		t.Fatal("expected full band name artist to exist")
	}
	tr, err := store.GetTrack(ctx, trackID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.ArtistID != band.ID {
		t.Fatalf("track artist_id = %d, want %d (Simon & Garfunkel)", tr.ArtistID, band.ID)
	}

	// The standalone "Simon" row must remain untouched and NOT receive the track.
	solo, err := store.GetArtistByName(ctx, "Simon")
	if err != nil {
		t.Fatal(err)
	}
	if solo == nil {
		t.Fatal("standalone Simon artist missing")
	}
	if tr.ArtistID == solo.ID {
		t.Fatal("track must not be attributed to the standalone Simon artist")
	}
}

func TestStore_ImportTrack_FeatFoldsEvenWhenPrimaryMemberExists(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := New(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Even when a lone "2Pac" exists, an explicit feat marker is authoritative:
	// the track folds onto the primary artist, never creating a "2Pac feat. X"
	// row. This mirrors the original pollution bug the guard repairs.
	if _, err := store.UpsertArtist(ctx, &domain.Artist{Name: "2Pac"}); err != nil {
		t.Fatal(err)
	}
	trackID, err := store.ImportTrack(ctx, &domain.Track{Title: "How Do U Want It"}, "2Pac feat. K-Ci & JoJo", "All Eyez on Me", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	main, err := store.GetArtistByName(ctx, "2Pac")
	if err != nil {
		t.Fatal(err)
	}
	if main == nil {
		t.Fatal("2Pac missing")
	}
	tr, err := store.GetTrack(ctx, trackID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.ArtistID != main.ID {
		t.Fatalf("track artist_id = %d, want %d (2Pac)", tr.ArtistID, main.ID)
	}

	if got, _ := store.GetArtistByName(ctx, "2Pac feat. K-Ci & JoJo"); got != nil {
		t.Fatalf("feat-marked name must not create its own artist, got id %d", got.ID)
	}
}
