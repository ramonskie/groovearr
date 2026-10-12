package catalog

import (
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

// wantTrack is the subset of MergedTrack asserted by the merge table. LocalID
// is 0 when the entry must not be downloaded (Local must be nil).
type wantTrack struct {
	title      string
	disc       int
	number     int
	downloaded bool
	localID    int64
}

func TestMergeTracks(t *testing.T) {
	tests := []struct {
		name         string
		materialized []AlbumTrack
		local        []domain.Track
		want         []wantTrack
	}{
		{
			name: "full match on every track",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha", ArtistName: "X", DurationMs: 1000, ISRC: "ISRC-1"},
				{DiscNumber: 1, TrackNumber: 2, Title: "Beta", ArtistName: "X", DurationMs: 2000},
			},
			local: []domain.Track{
				{ID: 10, DiscNumber: 1, TrackNumber: 1, Title: "Alpha", ISRC: "ISRC-1"},
				{ID: 11, DiscNumber: 1, TrackNumber: 2, Title: "Beta"},
			},
			want: []wantTrack{
				{title: "Alpha", disc: 1, number: 1, downloaded: true, localID: 10},
				{title: "Beta", disc: 1, number: 2, downloaded: true, localID: 11},
			},
		},
		{
			name: "partial match marks only the matched entry",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
				{DiscNumber: 1, TrackNumber: 2, Title: "Beta"},
				{DiscNumber: 1, TrackNumber: 3, Title: "Gamma"},
			},
			local: []domain.Track{
				{ID: 20, DiscNumber: 1, TrackNumber: 2, Title: "Beta"},
			},
			want: []wantTrack{
				{title: "Alpha", disc: 1, number: 1, downloaded: false},
				{title: "Beta", disc: 1, number: 2, downloaded: true, localID: 20},
				{title: "Gamma", disc: 1, number: 3, downloaded: false},
			},
		},
		{
			name: "zero local tracks leaves every entry undownloaded",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
				{DiscNumber: 1, TrackNumber: 2, Title: "Beta"},
			},
			local: nil,
			want: []wantTrack{
				{title: "Alpha", disc: 1, number: 1, downloaded: false},
				{title: "Beta", disc: 1, number: 2, downloaded: false},
			},
		},
		{
			name:         "zero provider tracks returns empty non-nil slice",
			materialized: nil,
			local: []domain.Track{
				{ID: 30, DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
				{ID: 31, DiscNumber: 1, TrackNumber: 2, Title: "Beta"},
			},
			want: []wantTrack{},
		},
		{
			name:         "empty (non-nil) provider tracks returns empty non-nil slice",
			materialized: []AlbumTrack{},
			local:        []domain.Track{{ID: 40, Title: "Alpha"}},
			want:         []wantTrack{},
		},
		{
			name: "ISRC preferred over title",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha", ISRC: "ISRC-A"},
			},
			local: []domain.Track{
				{ID: 1, DiscNumber: 1, TrackNumber: 1, Title: "Alpha", ISRC: "ISRC-OTHER"},
				{ID: 2, DiscNumber: 1, TrackNumber: 1, Title: "Beta", ISRC: "ISRC-A"},
			},
			want: []wantTrack{
				{title: "Alpha", disc: 1, number: 1, downloaded: true, localID: 2},
			},
		},
		{
			name: "unknown ISRC and mismatched number fall back to normalized title",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 9, Title: "Gamma", ISRC: "ISRC-NONE"},
			},
			local: []domain.Track{
				{ID: 5, DiscNumber: 1, TrackNumber: 2, Title: "Gamma", ISRC: "ISRC-XYZ"},
			},
			want: []wantTrack{
				{title: "Gamma", disc: 1, number: 9, downloaded: true, localID: 5},
			},
		},
		{
			name: "disc and track number match despite different title",
			materialized: []AlbumTrack{
				{DiscNumber: 2, TrackNumber: 5, Title: "Weird Provider Name"},
			},
			local: []domain.Track{
				{ID: 7, DiscNumber: 2, TrackNumber: 5, Title: "Totally Different Tag"},
			},
			want: []wantTrack{
				{title: "Weird Provider Name", disc: 2, number: 5, downloaded: true, localID: 7},
			},
		},
		{
			name: "zero disc number is normalized to one for disc-track matching",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 5, Title: "Zed"},
			},
			local: []domain.Track{
				{ID: 8, DiscNumber: 0, TrackNumber: 5, Title: "Other"},
			},
			want: []wantTrack{
				{title: "Zed", disc: 1, number: 5, downloaded: true, localID: 8},
			},
		},
		{
			name: "title match is case and whitespace insensitive",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 1, Title: "  Hello   World "},
			},
			local: []domain.Track{
				{ID: 3, DiscNumber: 1, TrackNumber: 99, Title: "hello world"},
			},
			want: []wantTrack{
				{title: "  Hello   World ", disc: 1, number: 1, downloaded: true, localID: 3},
			},
		},
		{
			name: "duplicate local tracks first one wins",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
			},
			local: []domain.Track{
				{ID: 1, DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
				{ID: 2, DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
			},
			want: []wantTrack{
				{title: "Alpha", disc: 1, number: 1, downloaded: true, localID: 1},
			},
		},
		{
			name: "a local track is not reused across materialized entries",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
			},
			local: []domain.Track{
				{ID: 1, DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
			},
			want: []wantTrack{
				{title: "Alpha", disc: 1, number: 1, downloaded: true, localID: 1},
				{title: "Alpha", disc: 1, number: 1, downloaded: false},
			},
		},
		{
			name: "duplicate materialized entries consume duplicate locals in order",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
				{DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
			},
			local: []domain.Track{
				{ID: 1, DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
				{ID: 2, DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
			},
			want: []wantTrack{
				{title: "Alpha", disc: 1, number: 1, downloaded: true, localID: 1},
				{title: "Alpha", disc: 1, number: 1, downloaded: true, localID: 2},
			},
		},
		{
			name: "output preserves provider order when matched out of order",
			materialized: []AlbumTrack{
				{DiscNumber: 1, TrackNumber: 2, Title: "Second"},
				{DiscNumber: 1, TrackNumber: 1, Title: "First"},
			},
			local: []domain.Track{
				{ID: 51, DiscNumber: 1, TrackNumber: 1, Title: "First"},
				{ID: 52, DiscNumber: 1, TrackNumber: 2, Title: "Second"},
			},
			want: []wantTrack{
				{title: "Second", disc: 1, number: 2, downloaded: true, localID: 52},
				{title: "First", disc: 1, number: 1, downloaded: true, localID: 51},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := MergeTracks(tt.materialized, tt.local)

			// Assert
			if got == nil {
				t.Fatalf("MergeTracks returned nil; want non-nil slice")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tt.want))
			}
			for i, w := range tt.want {
				g := got[i]
				if g.Title != w.title {
					t.Errorf("got[%d].Title = %q, want %q", i, g.Title, w.title)
				}
				if g.DiscNumber != w.disc {
					t.Errorf("got[%d].DiscNumber = %d, want %d", i, g.DiscNumber, w.disc)
				}
				if g.TrackNumber != w.number {
					t.Errorf("got[%d].TrackNumber = %d, want %d", i, g.TrackNumber, w.number)
				}
				if g.Downloaded != w.downloaded {
					t.Errorf("got[%d].Downloaded = %v, want %v", i, g.Downloaded, w.downloaded)
				}
				if !w.downloaded {
					if g.Local != nil {
						t.Errorf("got[%d].Local = %+v, want nil", i, g.Local)
					}
					continue
				}
				if g.Local == nil {
					t.Errorf("got[%d].Local = nil, want track %d", i, w.localID)
					continue
				}
				if g.Local.ID != w.localID {
					t.Errorf("got[%d].Local.ID = %d, want %d", i, g.Local.ID, w.localID)
				}
			}
		})
	}
}

// TestMergeTracks_PreservesMaterializedFields verifies the expected-track
// fields come from the materialized row (not the matched local file) and that
// an unmatched entry carries the materialized fields with Local == nil.
func TestMergeTracks_PreservesMaterializedFields(t *testing.T) {
	// Arrange
	materialized := []AlbumTrack{
		{DiscNumber: 2, TrackNumber: 3, Title: "Matched", ArtistName: "Provider Artist", DurationMs: 12345, ISRC: "ISRC-1"},
		{DiscNumber: 2, TrackNumber: 4, Title: "Missing", ArtistName: "Provider Artist", DurationMs: 67890, ISRC: "ISRC-2"},
	}
	local := []domain.Track{
		// Same disc/track as the first expected row but different display fields;
		// the merge must keep the provider's fields.
		{ID: 99, DiscNumber: 2, TrackNumber: 3, Title: "Local Title", ArtistID: 7, Duration: 1},
	}

	// Act
	got := MergeTracks(materialized, local)

	// Assert
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	first := got[0]
	if first.Title != "Matched" || first.ArtistName != "Provider Artist" || first.DurationMs != 12345 || first.ISRC != "ISRC-1" {
		t.Errorf("got[0] = %+v, want materialized fields preserved", first)
	}
	if !first.Downloaded || first.Local == nil || first.Local.ID != 99 {
		t.Errorf("got[0] = %+v, want downloaded with Local.ID 99", first)
	}
	second := got[1]
	if second.Title != "Missing" || second.ArtistName != "Provider Artist" || second.DurationMs != 67890 || second.ISRC != "ISRC-2" {
		t.Errorf("got[1] = %+v, want materialized fields preserved", second)
	}
	if second.Downloaded || second.Local != nil {
		t.Errorf("got[1] = %+v, want not downloaded and Local nil", second)
	}
}

// TestMergeTracksScoped_CrossAlbum covers the M9 fix: (disc, track) and title
// matches are scoped to the album's own tracks, so a same-position file that
// belongs to a different album is not reported as this album's download, while
// an ISRC match from another album still is (ISRC is globally unique).
func TestMergeTracksScoped_CrossAlbum(t *testing.T) {
	albumLocal := []domain.Track{
		// Same (disc, track) as the materialized row but a different song: must
		// NOT satisfy the row.
		{ID: 1, DiscNumber: 1, TrackNumber: 1, Title: "Other Album Song"},
		// Same ISRC as the materialized row: globally unique, MUST satisfy it.
		{ID: 3, DiscNumber: 1, TrackNumber: 1, Title: "Different Tag", ISRC: "ISRC-HIT"},
	}
	artistLocal := []domain.Track{
		// Belongs to another album of the same artist. Its (disc, track) matches
		// the materialized row but it must not be used for a position/title
		// match because it is not in albumLocal.
		{ID: 2, DiscNumber: 1, TrackNumber: 1, Title: "Also Other Album Song"},
	}

	t.Run("disc-track from another album does not mark downloaded", func(t *testing.T) {
		materialized := []AlbumTrack{
			{DiscNumber: 1, TrackNumber: 1, Title: "This Album Song"},
		}
		// Only the artist-wide track shares the position; albumLocal has none.
		got := MergeTracksScoped(materialized, nil, artistLocal)
		if got[0].Downloaded {
			t.Errorf("cross-album (disc,track) marked downloaded: %+v", got[0])
		}
	})

	t.Run("title from another album does not mark downloaded", func(t *testing.T) {
		materialized := []AlbumTrack{
			{DiscNumber: 1, TrackNumber: 9, Title: "Also Other Album Song"},
		}
		got := MergeTracksScoped(materialized, nil, artistLocal)
		if got[0].Downloaded {
			t.Errorf("cross-album title marked downloaded: %+v", got[0])
		}
	})

	t.Run("ISRC from another album does mark downloaded", func(t *testing.T) {
		materialized := []AlbumTrack{
			{DiscNumber: 1, TrackNumber: 1, Title: "This Album Song", ISRC: "ISRC-HIT"},
		}
		got := MergeTracksScoped(materialized, albumLocal, artistLocal)
		if !got[0].Downloaded || got[0].Local == nil || got[0].Local.ID != 3 {
			t.Errorf("cross-album ISRC did not mark downloaded: %+v", got[0])
		}
	})
}

// TestMergeTracksScoped_AlbumLocalStillMatches proves a genuine album-local
// track is matched by (disc, track) and by title when the artist pool is also
// present, and that a duplicate of the same track id in both pools is consumed
// only once (album copy wins).
func TestMergeTracksScoped_AlbumLocalStillMatches(t *testing.T) {
	albumLocal := []domain.Track{
		{ID: 10, DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
		{ID: 11, DiscNumber: 1, TrackNumber: 2, Title: "Beta"},
	}
	artistLocal := []domain.Track{
		// Duplicate id of the album track (must be deduped, album copy wins)
		// plus a distinct sibling that is not on this album.
		{ID: 10, DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
		{ID: 12, DiscNumber: 1, TrackNumber: 3, Title: "Gamma"},
	}
	materialized := []AlbumTrack{
		{DiscNumber: 1, TrackNumber: 1, Title: "Alpha"},
		{DiscNumber: 1, TrackNumber: 2, Title: "Beta"},
	}

	got := MergeTracksScoped(materialized, albumLocal, artistLocal)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if !got[0].Downloaded || got[0].Local == nil || got[0].Local.ID != 10 {
		t.Errorf("album track (disc,track) not matched: %+v", got[0])
	}
	if !got[1].Downloaded || got[1].Local == nil || got[1].Local.ID != 11 {
		t.Errorf("album track title/number not matched: %+v", got[1])
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "lowercases", in: "Hello World", want: "hello world"},
		{name: "trims", in: "  padded  ", want: "padded"},
		{name: "collapses internal whitespace", in: "a   b\tc", want: "a b c"},
		{name: "empty stays empty", in: "   ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalize(tt.in); got != tt.want {
				t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
