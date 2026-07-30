package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/providers/musicbrainz"
)

// testServer creates an httptest server for Prowlarr + Torznab endpoints.
type testServer struct {
	srv      *httptest.Server
	handlers map[string]http.HandlerFunc
}

func newProwlarrServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{
		handlers: make(map[string]http.HandlerFunc),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if h, ok := ts.handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		// Torznab endpoints: /{indexerID}/api
		for path, h := range ts.handlers {
			if len(path) > 0 && path[0] != '/' && r.URL.Path == "/"+path+"/api" {
				h(w, r)
				return
			}
		}
		http.NotFound(w, r)
	})
	ts.srv = httptest.NewServer(mux)
	return ts
}

func (ts *testServer) close() { ts.srv.Close() }

func (ts *testServer) newPlugin(t *testing.T) *Plugin {
	t.Helper()
	mb := musicbrainz.NewAPIClient(musicbrainz.MusicBrainzConfig{}, nil)
	mb.SetMinInterval(0)
	p, err := newPlugin(Config{
		URL:        ts.srv.URL,
		APIKey:     "test-key",
		IndexerTag: "groovearr",
		Categories: []int{3040},
	}, mb, nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	return p
}

// ─── Indexer listing tests ───────────────────────────────────────────

func TestListIndexers(t *testing.T) {
	ts := newProwlarrServer(t)
	defer ts.close()

	// Serve indexers and tags endpoints on the same server.
	// The torznabClient only uses baseURL, so all calls go to the test server.
	ts.handlers["/api/v1/indexer"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		indexers := []prowlarrIndexer{
			{ID: 1, Name: "RuTracker", Tags: []int{10}},
			{ID: 2, Name: "Nyaa", Tags: []int{20}},
		}
		json.NewEncoder(w).Encode(indexers)
	}

	ts.handlers["/api/v1/tag"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrTag{
			{ID: 10, Label: "groovearr"},
			{ID: 20, Label: "anime"},
		})
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	indexers, err := p.findRuTrackerIndexers(ctx)
	if err != nil {
		t.Fatalf("findRuTrackerIndexers: %v", err)
	}
	if len(indexers) != 1 {
		t.Fatalf("expected 1 indexer with tag groovearr, got %d", len(indexers))
	}
	if indexers[0].Name != "RuTracker" {
		t.Errorf("expected RuTracker, got %s", indexers[0].Name)
	}
}
func TestListIndexersNoMatchingTag(t *testing.T) {
	ts := newProwlarrServer(t)
	defer ts.close()

	ts.handlers["/api/v1/indexer"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrIndexer{
			{ID: 1, Name: "Nyaa", Tags: []int{20}},
		})
	}

	ts.handlers["/api/v1/tag"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrTag{
			{ID: 20, Label: "anime"},
			{ID: 10, Label: "groovearr"},
		})
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	indexers, err := p.findRuTrackerIndexers(ctx)
	if err != nil {
		t.Fatalf("findRuTrackerIndexers: %v", err)
	}
	// When no indexer matches the tag, all indexers are returned as fallback.
	if len(indexers) != 1 {
		t.Fatalf("expected 1 indexer (fallback to all), got %d", len(indexers))
	}
}

// ─── Torznab search tests ────────────────────────────────────────────

func TestTorznabSearch(t *testing.T) {
	ts := newProwlarrServer(t)
	defer ts.close()

	ts.handlers["/api/v1/indexer"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrIndexer{
			{ID: 1, Name: "RuTracker", Tags: []int{10}},
		})
	}

	ts.handlers["/api/v1/tag"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrTag{
			{ID: 10, Label: "groovearr"},
		})
	}

	// Torznab XML endpoint: /1/api
	ts.handlers["1"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>RuTracker</title>
    <item>
      <title>Metallica - Master of Puppets FLAC</title>
      <guid>12345</guid>
      <size>367001600</size>
      <link>https://rutracker.org/forum/viewtopic.php?t=12345</link>
      <torznab:attr name="seeders" value="45"/>
      <torznab:attr name="peers" value="12"/>
      <torznab:attr name="infohash" value="8C212779B4ABDE7C6BC608063A0D008B7E40CE32"/>
      <torznab:attr name="artist" value="Metallica"/>
      <torznab:attr name="album" value="Master of Puppets"/>
      <torznab:attr name="year" value="1986"/>
    </item>
  </channel>
</rss>`)
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	releases, err := p.SearchAlbum(ctx, "Metallica Master of Puppets")
	if err != nil {
		t.Fatalf("SearchAlbum: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}
	r := releases[0]
	if r.Artist != "Metallica" {
		t.Errorf("Artist = %q, want Metallica", r.Artist)
	}
	if r.Album != "Master of Puppets" {
		t.Errorf("Album = %q, want Master of Puppets", r.Album)
	}
	if r.Year != 1986 {
		t.Errorf("Year = %d, want 1986", r.Year)
	}
	if r.Size != 367001600 {
		t.Errorf("Size = %d, want 367001600", r.Size)
	}
	if r.Seeders != 45 {
		t.Errorf("Seeders = %d, want 45", r.Seeders)
	}
	if r.MagnetURI == "" {
		t.Error("MagnetURI should not be empty")
	}
}

func TestSearchAlbumNoResults(t *testing.T) {
	ts := newProwlarrServer(t)
	defer ts.close()

	ts.handlers["/api/v1/indexer"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrIndexer{
			{ID: 1, Name: "RuTracker", Tags: []int{10}},
		})
	}

	ts.handlers["/api/v1/tag"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrTag{
			{ID: 10, Label: "groovearr"},
		})
	}

	ts.handlers["1"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><rss><channel></channel></rss>`)
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	releases, err := p.SearchAlbum(ctx, "nonexistent album")
	if err != nil {
		t.Fatalf("SearchAlbum: %v", err)
	}
	if len(releases) != 0 {
		t.Fatalf("expected 0 releases for no results, got %d", len(releases))
	}
}

// ─── ResolveTracks tests (MusicBrainz via mock) ──────────────────────

func TestResolveTracks(t *testing.T) {
	mbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case path == "/release-group/":
			// Search release group
			fmt.Fprint(w, `{"release-groups":[{"id":"rg-mbid-123","title":"Master of Puppets","primary-type":"Album"}]}`)

		case path == "/release-group/rg-mbid-123":
			// Lookup release group
			fmt.Fprint(w, `{"releases":[{"id":"rel-mbid-456","title":"Master of Puppets"}]}`)

		case path == "/release/rel-mbid-456":
			// Lookup release
			fmt.Fprint(w, `{
				"id":"rel-mbid-456",
				"title":"Master of Puppets",
				"date":"1986-03-03",
				"country":"US",
				"release-group":{"id":"rg-mbid-123"},
				"label-info":[{"label":{"name":"Elektra"}}],
				"media":[{"tracks":[
					{"number":"1","title":"Battery","length":312000,"recording":{"id":"r1","title":"Battery","artist-credit":[{"name":"Metallica","joinphrase":""}]}},
					{"number":"2","title":"Master of Puppets","length":515000,"recording":{"id":"r2","title":"Master of Puppets","artist-credit":[{"name":"Metallica","joinphrase":""}]}}
				]}],
				"genres":[{"name":"thrash metal"}]
			}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mbSrv.Close()

	mbClient := musicbrainz.NewAPIClient(musicbrainz.MusicBrainzConfig{}, nil)
	mbClient.SetBaseURL(mbSrv.URL)
	mbClient.SetHTTPClient(mbSrv.Client())
	mbClient.SetMinInterval(0)

	p, err := newPlugin(Config{
		URL:        "http://localhost:9696",
		APIKey:     "key",
		IndexerTag: "groovearr",
		Categories: []int{3040},
	}, mbClient, nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}

	ctx := context.Background()
	release := domain.AlbumRelease{
		Artist: "Metallica",
		Album:  "Master of Puppets",
	}

	tracks, err := p.ResolveTracks(ctx, release)
	if err != nil {
		t.Fatalf("ResolveTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(tracks))
	}
	if tracks[0].Title != "Battery" {
		t.Errorf("track 0 title = %q, want Battery", tracks[0].Title)
	}
	if tracks[0].Artist != "Metallica" {
		t.Errorf("track 0 artist = %q, want Metallica", tracks[0].Artist)
	}
	if tracks[0].TrackNumber != 1 {
		t.Errorf("track 0 number = %d, want 1", tracks[0].TrackNumber)
	}
	if tracks[0].Duration != 312 {
		t.Errorf("track 0 duration = %d, want 312", tracks[0].Duration)
	}

	if tracks[1].Title != "Master of Puppets" {
		t.Errorf("track 1 title = %q, want Master of Puppets", tracks[1].Title)
	}
	if tracks[1].TrackNumber != 2 {
		t.Errorf("track 1 number = %d, want 2", tracks[1].TrackNumber)
	}
	// Note: release.MBID/AlbumType/CoverURL are set internally but the
	// AlbumProvider interface pass release by value, so caller won't see
	// these mutations. The tracks themselves contain the resolved data.
}

// ─── CheckConnection tests ───────────────────────────────────────────

func TestCheckConnection(t *testing.T) {
	ts := newProwlarrServer(t)
	defer ts.close()

	ts.handlers["/api/v1/indexer"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrIndexer{
			{ID: 1, Name: "RuTracker", Tags: []int{10}},
		})
	}

	ts.handlers["/api/v1/tag"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]prowlarrTag{
			{ID: 10, Label: "groovearr"},
		})
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	if err := p.CheckConnection(ctx); err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if !p.Connected() {
		t.Error("expected Connected() = true")
	}
}

func TestParseTitle(t *testing.T) {
	tests := []struct {
		name           string
		title          string
		wantArtist     string
		wantAlbum      string
	}{
		{
			"simple", "Artist - Album",
			"Artist", "Album",
		},
		{
			"genre prefix", "(Thrash) Artist - Album",
			"Artist", "Album",
		},
		{
			"multi prefix", "(Thrash, Speed Metal) [CD] Artist - Album (2019)",
			"Artist", "Album",
		},
		{
			"lossless with catalog", "(Thrash, Speed Metal) [CD] Artist - Album [SHM-CD, UICR-1139, Japan]- 1986/2018, FLAC (image+.cue), lossless",
			"Artist", "Album",
		},
		{
			"vinyl rip", "(Rock) [LP] [1/5,64 MHz] Artist - Album - (US) - 1986, DSD 128 (tracks)",
			"Artist", "Album",
		},
		{
			"24/96 prefix", "[24/96] Artist - Album - 2020, FLAC",
			"Artist", "Album",
		},
		{
			"no space after bracket", "(Genre)[Format] Artist - Album",
			"Artist", "Album",
		},
		{
			"multi disc set", "Artist - Kill 'Em All; Ride The Lightning; Master Of Puppets - 2021, FLAC",
			"Artist", "Kill 'Em All; Ride The Lightning; Master Of Puppets",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArtist, gotAlbum := parseTitle(tt.title)
			if gotArtist != tt.wantArtist {
				t.Errorf("parseTitle(%q) artist = %q, want %q", tt.title, gotArtist, tt.wantArtist)
			}
			if gotAlbum != tt.wantAlbum {
				t.Errorf("parseTitle(%q) album = %q, want %q", tt.title, gotAlbum, tt.wantAlbum)
			}
		})
	}
}

func TestIsImageRelease(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		want        bool
	}{
		{"audio flac", "Artist - Album (FLAC)", "Lossless audio", false},
		{"mp3 320", "Artist - Album (MP3 320)", "", false},
		{"sacd-r iso", "[SACD-R][TR24] Artist - Album - 1986/2016", "SACD image", true},
		{"sacd iso bracket", "[SACD ISO] Artist - Album (2019)", "", true},
		{"iso disc", "Artist - Album.iso", "", true},
		{"iso bracket", "Artist - Album [ISO]", "", true},
		{"iso in parens", "Artist - Album (ISO)", "", true},
		{"iso image text", "Artist - Album (iso image)", "", true},
		{"dvd-audio", "Artist - Album [DVD-Audio]", "", true},
		{"dvd-a short", "Artist - Album (DVD-A)", "", true},
		{"dts cd", "Artist DTS CD 5.1", "", true},
		{"blu-ray", "Artist - Album (Blu-ray audio)", "", true},
		{"bd-a", "Artist - Album [BD-A] 24/96", "", true},
		{"sacd not iso", "Artist - Album (SACD)", "regular stereo SACD", false},
		{"russian disk image", "Artist - Album (образ диска)", "", true},
		{"legit sacd release", "Artist - Album (2019) [SACD] (FLAC)", "", false},
		{"flac image+cue", "Artist - Album - 2020, FLAC (image+.cue), lossless", "", true},
		{"flac image + .cue", "Artist - Album (2020) FLAC image + .cue", "", true},
		{"flac tracks", "Artist - Album - 2020, FLAC (tracks), lossless", "individual tracks", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isImageRelease(tt.title, tt.description)
			if got != tt.want {
				t.Errorf("isImageRelease(%q, %q) = %v, want %v", tt.title, tt.description, got, tt.want)
			}
		})
	}
}
