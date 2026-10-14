package discovery

import "testing"

// TestAlbumQueryQuery locks the search-phrase precedence providers rely on:
// structured Artist+Title wins; otherwise the bare Title (an album with no or
// unknown artist must still search, not send an empty query); otherwise the
// free-text Text. Artist wins over Text when both are set.
func TestAlbumQueryQuery(t *testing.T) {
	tests := []struct {
		name string
		q    AlbumQuery
		want string
	}{
		{"artist and title", AlbumQuery{Artist: "Daft Punk", Title: "Discovery"}, "Daft Punk Discovery"},
		{"title only", AlbumQuery{Title: "Discovery"}, "Discovery"},
		{"text only", AlbumQuery{Text: "daft punk discovery"}, "daft punk discovery"},
		{"artist wins over text", AlbumQuery{Artist: "A", Title: "B", Text: "x"}, "A B"},
		{"artist with blank title", AlbumQuery{Artist: "Daft Punk", Title: "   "}, "Daft Punk"},
		{"collapses extra whitespace", AlbumQuery{Artist: "  Daft   Punk ", Title: " Discovery "}, "Daft Punk Discovery"},
		{"title only trimmed", AlbumQuery{Title: "  Discovery  "}, "Discovery"},
		{"all empty", AlbumQuery{}, ""},
		{"all blank", AlbumQuery{Artist: "  ", Title: " ", Text: "  "}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.q.Query(); got != tt.want {
				t.Errorf("AlbumQuery%+v.Query() = %q, want %q", tt.q, got, tt.want)
			}
		})
	}
}
