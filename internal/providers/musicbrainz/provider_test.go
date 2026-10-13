package musicbrainz

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newResolverClient builds a Client whose API layer points at a mock server.
// responses maps an exact request path to a canned JSON body; unknown paths
// return 404 (which apiGet treats as "not found", not an error).
func newResolverClient(t *testing.T, responses map[string]string) (*Client, func()) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))

	api := &APIClient{
		cfg:         MusicBrainzConfig{},
		httpClient:  srv.Client(),
		userAgent:   "groovearr-test",
		baseURL:     srv.URL,
		log:         testLogger(),
		minInterval: 0,
	}
	return &Client{cfg: MusicBrainzConfig{}, api: api, log: testLogger()}, srv.Close
}

func TestResolveTracksForCount_PicksClosestTrackCount(t *testing.T) {
	client, cleanup := newResolverClient(t, map[string]string{
		"/release-group/": `{"release-groups":[{"id":"rg-1","title":"Album","primary-type":"Album"}]}`,
		"/release/": `{"releases":[
			{"id":"rel-small","title":"Album","track-count":8},
			{"id":"rel-big","title":"Album","disambiguation":"Deluxe Edition","track-count":12}
		]}`,
		"/release/rel-big": `{"id":"rel-big","title":"Album","media":[{"tracks":[
			{"number":"1","title":"One","length":180000,"recording":{"id":"r1","title":"One","artist-credit":[{"name":"The Artist","joinphrase":""}]}},
			{"number":"2","title":"Two","length":200000,"recording":{"id":"r2","title":"Two","artist-credit":[]}}
		]}]}`,
	})
	defer cleanup()

	tracks, mbid, err := client.ResolveTracksForCount(context.Background(), "The Artist", "Album", 12, "")
	if err != nil {
		t.Fatalf("ResolveTracksForCount: %v", err)
	}
	if mbid != "rel-big" {
		t.Errorf("mbid = %q, want rel-big", mbid)
	}
	if len(tracks) != 2 {
		t.Fatalf("track count = %d, want 2", len(tracks))
	}
	if tracks[0].Title != "One" || tracks[0].TrackNumber != 1 || tracks[0].DurationMs != 180000 {
		t.Errorf("track 0 = %+v, want title One / #1 / 180000ms", tracks[0])
	}
	if tracks[0].AlbumTitle != "Album" {
		t.Errorf("track 0 album = %q, want Album", tracks[0].AlbumTitle)
	}
	// Per-track credit present on track 0, absent on track 1 → album-artist fallback.
	if tracks[0].ArtistName != "The Artist" {
		t.Errorf("track 0 artist = %q, want The Artist", tracks[0].ArtistName)
	}
	if tracks[1].ArtistName != "The Artist" {
		t.Errorf("track 1 artist fallback = %q, want The Artist", tracks[1].ArtistName)
	}
}

func TestResolveTracksForCount_NoFileCountUsesFirstRelease(t *testing.T) {
	client, cleanup := newResolverClient(t, map[string]string{
		"/release-group/":     `{"release-groups":[{"id":"rg-1","title":"Album","primary-type":"Album"}]}`,
		"/release-group/rg-1": `{"releases":[{"id":"rel-first","title":"Album"},{"id":"rel-second","title":"Album"}]}`,
		"/release/rel-first": `{"id":"rel-first","title":"Album","media":[{"tracks":[
			{"number":"1","title":"One","length":1000,"recording":{"id":"r1","title":"One","artist-credit":[{"name":"A","joinphrase":""}]}}
		]}]}`,
	})
	defer cleanup()

	tracks, mbid, err := client.ResolveTracksForCount(context.Background(), "A", "Album", 0, "")
	if err != nil {
		t.Fatalf("ResolveTracksForCount: %v", err)
	}
	if mbid != "rel-first" {
		t.Errorf("mbid = %q, want rel-first", mbid)
	}
	if len(tracks) != 1 {
		t.Fatalf("track count = %d, want 1", len(tracks))
	}
}

func TestResolveTracksForCount_TieBrokenByTorrentTitle(t *testing.T) {
	client, cleanup := newResolverClient(t, map[string]string{
		"/release-group/": `{"release-groups":[{"id":"rg-1","title":"Album","primary-type":"Album"}]}`,
		"/release/": `{"releases":[
			{"id":"rel-plain","title":"Album","track-count":10},
			{"id":"rel-deluxe","title":"Album","disambiguation":"Deluxe Edition","track-count":10}
		]}`,
		"/release/rel-deluxe": `{"id":"rel-deluxe","title":"Album","media":[{"tracks":[
			{"number":"1","title":"One","length":1000,"recording":{"id":"r1","title":"One","artist-credit":[{"name":"A","joinphrase":""}]}}
		]}]}`,
	})
	defer cleanup()

	_, mbid, err := client.ResolveTracksForCount(context.Background(), "A", "Album", 10, "Album Deluxe Edition 2020")
	if err != nil {
		t.Fatalf("ResolveTracksForCount: %v", err)
	}
	if mbid != "rel-deluxe" {
		t.Errorf("mbid = %q, want rel-deluxe (tie broken by torrent title)", mbid)
	}
}

func TestResolveTracksForCount_NoReleaseGroup(t *testing.T) {
	client, cleanup := newResolverClient(t, map[string]string{
		"/release-group/": `{"release-groups":[]}`,
	})
	defer cleanup()

	if _, _, err := client.ResolveTracksForCount(context.Background(), "A", "Album", 5, ""); err == nil {
		t.Fatal("expected error when no release group matches, got nil")
	}
}

func TestResolveTracksForCount_NoTracks(t *testing.T) {
	client, cleanup := newResolverClient(t, map[string]string{
		"/release-group/":     `{"release-groups":[{"id":"rg-1","title":"Album","primary-type":"Album"}]}`,
		"/release-group/rg-1": `{"releases":[{"id":"rel-empty","title":"Album"}]}`,
		"/release/rel-empty":  `{"id":"rel-empty","title":"Album","media":[]}`,
	})
	defer cleanup()

	if _, _, err := client.ResolveTracksForCount(context.Background(), "A", "Album", 0, ""); err == nil {
		t.Fatal("expected error when the release has no tracks, got nil")
	}
}

func TestResolveTracksForCount_RequiresArtistAndAlbum(t *testing.T) {
	client, cleanup := newResolverClient(t, map[string]string{})
	defer cleanup()

	if _, _, err := client.ResolveTracksForCount(context.Background(), "", "Album", 5, ""); err == nil {
		t.Error("expected error for empty artist")
	}
	if _, _, err := client.ResolveTracksForCount(context.Background(), "A", "", 5, ""); err == nil {
		t.Error("expected error for empty album")
	}
}

func TestResolveTracksForCount_PopulatesProviderIDAndDiscNumber(t *testing.T) {
	// Contract parity with GetAlbumTracks: the resolver must populate the same
	// TrackInfo fields (ProviderID + DiscNumber), not just the core ones.
	client, cleanup := newResolverClient(t, map[string]string{
		"/release-group/": `{"release-groups":[{"id":"rg-1","title":"Album","primary-type":"Album"}]}`,
		"/release/":       `{"releases":[{"id":"rel-1","title":"Album","track-count":2}]}`,
		"/release/rel-1": `{"id":"rel-1","title":"Album","media":[
			{"position":1,"tracks":[
				{"number":"1","title":"One","length":1000,"recording":{"id":"r1","title":"One","artist-credit":[{"name":"A","joinphrase":""}]}}
			]},
			{"position":2,"tracks":[
				{"number":"1","title":"Two","length":2000,"recording":{"id":"r2","title":"Two","artist-credit":[{"name":"A","joinphrase":""}]}}
			]}
		]}`,
	})
	defer cleanup()

	tracks, _, err := client.ResolveTracksForCount(context.Background(), "A", "Album", 2, "")
	if err != nil {
		t.Fatalf("ResolveTracksForCount: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("track count = %d, want 2", len(tracks))
	}
	if tracks[0].ProviderID != "1" || tracks[1].ProviderID != "1" {
		t.Errorf("ProviderIDs = %q,%q, want \"1\",\"1\"", tracks[0].ProviderID, tracks[1].ProviderID)
	}
	if tracks[0].DiscNumber != 1 || tracks[1].DiscNumber != 2 {
		t.Errorf("disc numbers = %d,%d, want 1,2", tracks[0].DiscNumber, tracks[1].DiscNumber)
	}
}

func TestPickBestMatchingRelease(t *testing.T) {
	releases := []ReleaseGroupRelease{
		{ID: "a", Title: "Album", TrackCount: 10},
		{ID: "b", Title: "Album", Disambiguation: "Deluxe Edition", TrackCount: 12},
		{ID: "c", Title: "Album", TrackCount: 9},
	}

	tests := []struct {
		name         string
		targetCount  int
		torrentTitle string
		want         string
	}{
		{"exact count wins", 10, "", "a"},
		{"tie keeps first candidate", 11, "", "a"},
		{"closest above", 8, "", "c"},
		{"torrent title breaks tie", 11, "Album Deluxe Edition", "b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickBestMatchingRelease(releases, tt.targetCount, tt.torrentTitle)
			if got.ID != tt.want {
				t.Errorf("pickBestMatchingRelease(%d, %q) = %q, want %q", tt.targetCount, tt.torrentTitle, got.ID, tt.want)
			}
		})
	}

	if got := pickBestMatchingRelease(nil, 5, "anything"); got.ID != "" {
		t.Errorf("empty input = %+v, want zero value", got)
	}
}

func TestRepresentativeRelease_PrefersOfficialThenEarliest(t *testing.T) {
	// MusicBrainz does not guarantee order: a bootleg dated earlier than the
	// official original is listed first, yet the official release must win.
	releases := []ReleaseGroupRelease{
		{ID: "bootleg", Status: "Bootleg", Date: "1990-01-01", PrimaryType: "Album"},
		{ID: "official-late", Status: "Official", Date: "2011-06-01", PrimaryType: "Album"},
		{ID: "official-early", Status: "Official", Date: "1991-09-24", PrimaryType: "Album"},
		{ID: "promo", Status: "Promotion", Date: "1989-01-01", PrimaryType: "Album"},
	}
	if got := representativeRelease(releases); got.ID != "official-early" {
		t.Errorf("representativeRelease = %q, want official-early (official, then earliest date)", got.ID)
	}
}

func TestRepresentativeRelease_NoOfficialFallsBackToEarliestDate(t *testing.T) {
	releases := []ReleaseGroupRelease{
		{ID: "late", Date: "2000-05-01", PrimaryType: "Album"},
		{ID: "undated", PrimaryType: "Album"},
		{ID: "early", Date: "1990-01-01", PrimaryType: "Album"},
	}
	if got := representativeRelease(releases); got.ID != "early" {
		t.Errorf("representativeRelease = %q, want early (earliest dated release)", got.ID)
	}
}

func TestRepresentativeRelease_SkipsEmptyIDsAndEmptyInput(t *testing.T) {
	releases := []ReleaseGroupRelease{
		{ID: "", Status: "Official", Date: "1980-01-01"},
		{ID: "valid", Status: "Bootleg", Date: "2000-01-01"},
	}
	if got := representativeRelease(releases); got.ID != "valid" {
		t.Errorf("representativeRelease = %q, want valid", got.ID)
	}
	if got := representativeRelease(nil); got.ID != "" {
		t.Errorf("representativeRelease(nil) = %q, want zero value", got.ID)
	}
}
