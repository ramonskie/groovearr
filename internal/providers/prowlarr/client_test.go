package prowlarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
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
	p, err := newPlugin(Config{
		URL:        ts.srv.URL,
		APIKey:     "test-key",
		IndexerTag: "groovearr",
		Categories: []int{3040},
	}, nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	p.SetReleaseResolver(&stubResolver{})
	return p
}

// ─── Indexer listing tests ───────────────────────────────────────────

func TestListIndexers(t *testing.T) {
	ts := newProwlarrServer(t)
	defer ts.close()

	// Serve indexers and tags endpoints on the same server.
	// The torznabClient only uses baseURL, so all calls go to the test server.
	ts.handlers["/api/v1/indexer"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" {
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

// ─── ResolveTracks tests (injected discovery.ReleaseResolver stub) ───

func TestResolveTracksMapsResolverTracks(t *testing.T) {
	p, err := newPlugin(Config{URL: "http://localhost:9696", APIKey: "key", IndexerTag: "groovearr"}, nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	stub := &stubResolver{
		tracks: []discovery.TrackInfo{
			{ArtistName: "Metallica", Title: "Battery", TrackNumber: 1, DurationMs: 312000},
			// Empty ArtistName must fall back to the album artist.
			{Title: "Master of Puppets", TrackNumber: 2, DurationMs: 515000},
		},
		mbid: "rel-mbid-456",
	}
	p.SetReleaseResolver(stub)

	release := domain.AlbumRelease{Artist: "Metallica", Album: "Master of Puppets"}
	tracks, mbid, err := p.ResolveTracksForCount(context.Background(), release, 2, "Metallica - Master of Puppets [FLAC]")
	if err != nil {
		t.Fatalf("ResolveTracksForCount: %v", err)
	}
	if mbid != "rel-mbid-456" {
		t.Errorf("mbid = %q, want rel-mbid-456", mbid)
	}
	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(tracks))
	}
	if tracks[0].Title != "Battery" || tracks[0].Artist != "Metallica" || tracks[0].TrackNumber != 1 || tracks[0].Duration != 312 {
		t.Errorf("track 0 = %+v, want Battery/Metallica/#1/312", tracks[0])
	}
	if tracks[1].Artist != "Metallica" {
		t.Errorf("track 1 artist = %q, want fallback to album artist Metallica", tracks[1].Artist)
	}
	if tracks[1].Title != "Master of Puppets" || tracks[1].TrackNumber != 2 || tracks[1].Duration != 515 {
		t.Errorf("track 1 = %+v, want Master of Puppets/#2/515", tracks[1])
	}

	// Hints must be passed through to the resolver unchanged.
	if stub.calls != 1 {
		t.Errorf("resolver calls = %d, want 1", stub.calls)
	}
	if stub.gotArtist != "Metallica" || stub.gotAlbum != "Master of Puppets" {
		t.Errorf("resolver artist/album = %q/%q, want Metallica/Master of Puppets", stub.gotArtist, stub.gotAlbum)
	}
	if stub.gotFileCount != 2 || stub.gotTorrentTitle != "Metallica - Master of Puppets [FLAC]" {
		t.Errorf("resolver hints = fileCount %d, torrentTitle %q", stub.gotFileCount, stub.gotTorrentTitle)
	}
}

func TestResolveTracksNoCountPassesZeroHints(t *testing.T) {
	p, err := newPlugin(Config{URL: "http://localhost:9696", APIKey: "key"}, nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	stub := &stubResolver{tracks: []discovery.TrackInfo{{Title: "One", TrackNumber: 1, DurationMs: 1000}}}
	p.SetReleaseResolver(stub)

	if _, err := p.ResolveTracks(context.Background(), domain.AlbumRelease{Artist: "A", Album: "B"}); err != nil {
		t.Fatalf("ResolveTracks: %v", err)
	}
	if stub.gotFileCount != 0 || stub.gotTorrentTitle != "" {
		t.Errorf("no-count call should pass empty hints, got fileCount %d, torrentTitle %q", stub.gotFileCount, stub.gotTorrentTitle)
	}
}

func TestResolveTracksNilResolver(t *testing.T) {
	p, err := newPlugin(Config{URL: "http://localhost:9696", APIKey: "key"}, nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	// No SetReleaseResolver call — a missing wiring must fail loudly.
	_, _, err = p.ResolveTracksForCount(context.Background(), domain.AlbumRelease{Artist: "A", Album: "B"}, 0, "")
	if err == nil {
		t.Fatal("expected error when resolver is unset")
	}
	if !strings.Contains(err.Error(), "release resolver") {
		t.Errorf("error = %v, want mention of release resolver", err)
	}
}

func TestResolveTracksResolverErrorWrapped(t *testing.T) {
	p, err := newPlugin(Config{URL: "http://localhost:9696", APIKey: "key"}, nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	p.SetReleaseResolver(&stubResolver{err: errors.New("boom")})

	_, _, err = p.ResolveTracksForCount(context.Background(), domain.AlbumRelease{Artist: "A", Album: "B"}, 0, "")
	if err == nil {
		t.Fatal("expected error from resolver")
	}
	if !strings.Contains(err.Error(), "resolve tracks") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want wrapped resolver failure", err)
	}
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
		name       string
		title      string
		wantArtist string
		wantAlbum  string
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
		{
			"by_artist suffix", `"Album Title" (by Artist Name) 2025, MP3, 320 kbps`,
			"Artist Name", "Album Title",
		},
		{
			"by_artist with / separator", `/ Stans / STANS (The Official Soundtrack) (by Eminem) 2025, MP3, 320 kbps`,
			"Eminem", "STANS (The Official Soundtrack)",
		},
		{
			"no separator with by_artist", `Album Name (by Artist) 2025, FLAC`,
			"Artist", "Album Name",
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
