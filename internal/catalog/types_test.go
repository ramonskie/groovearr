package catalog

import "testing"

func TestTrackOriginValid(t *testing.T) {
	cases := []struct {
		name string
		in   TrackOrigin
		want bool
	}{
		{"library", OriginLibrary, true},
		{"provider", OriginProvider, true},
		{"empty", TrackOrigin(""), false},
		{"unknown", TrackOrigin("bogus"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Valid(); got != tc.want {
				t.Fatalf("TrackOrigin(%q).Valid() = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestRefreshStatusValid(t *testing.T) {
	cases := []struct {
		name string
		in   RefreshStatus
		want bool
	}{
		{"pending", StatusPending, true},
		{"fresh", StatusFresh, true},
		{"stale", StatusStale, true},
		{"unmatched", StatusUnmatched, true},
		{"failed", StatusFailed, true},
		{"empty", RefreshStatus(""), false},
		{"unknown", RefreshStatus("bogus"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Valid(); got != tc.want {
				t.Fatalf("RefreshStatus(%q).Valid() = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestRefreshStatusIsTerminal(t *testing.T) {
	cases := []struct {
		name string
		in   RefreshStatus
		want bool
	}{
		{"pending", StatusPending, false},
		{"fresh", StatusFresh, false},
		{"stale", StatusStale, false},
		{"unmatched", StatusUnmatched, true},
		{"failed", StatusFailed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.IsTerminal(); got != tc.want {
				t.Fatalf("RefreshStatus(%q).IsTerminal() = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestEntityTypeValid(t *testing.T) {
	cases := []struct {
		name string
		in   EntityType
		want bool
	}{
		{"album", EntityAlbum, true},
		{"artist", EntityArtist, true},
		{"empty", EntityType(""), false},
		{"unknown", EntityType("track"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Valid(); got != tc.want {
				t.Fatalf("EntityType(%q).Valid() = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPriorityOrdering(t *testing.T) {
	if !(PriorityHigh > PriorityDefault && PriorityDefault > PriorityLow) {
		t.Fatalf("priority ordering broken: high=%d default=%d low=%d",
			PriorityHigh, PriorityDefault, PriorityLow)
	}
}
