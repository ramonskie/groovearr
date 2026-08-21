package tidal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// newTestStreamClient builds a streamClient wired to a test HTTP server via
// the shared apiClient test harness (baseURL is injectable through newAPI).
func newTestStreamClient(t *testing.T, handler http.HandlerFunc) (*streamClient, *httptest.Server) {
	t.Helper()
	api, srv := newTestAPI(t, handler)
	return newStreamClient(api), srv
}

// streamBTSJSON returns a playbackinfopostpaywall-style stream JSON payload
// with a base64-encoded BTS manifest.
func streamBTSJSON(trackID int, quality AudioQuality, manifest string) string {
	return `{
		"trackId": ` + strconv.Itoa(trackID) + `,
		"assetPresentation": "FULL",
		"audioMode": "STEREO",
		"audioQuality": "` + string(quality) + `",
		"manifestMimeType": "application/vnd.tidal.bts",
		"manifestHash": "hash",
		"manifest": "` + b64(manifest) + `"
	}`
}

// ─── Quality precedence ─────────────────────────────────────────────────

func TestTrackQualityPrecedence(t *testing.T) {
	tests := []struct {
		q    AudioQuality
		want int
	}{
		{Low, 0},
		{High, 1},
		{Lossless, 2},
		{HiResLossless, 3},
		{AudioQuality("UNKNOWN"), 0}, // unknown defaults to lowest
	}
	for _, tt := range tests {
		if got := qualityPrecedence(tt.q); got != tt.want {
			t.Errorf("qualityPrecedence(%q) = %d, want %d", tt.q, got, tt.want)
		}
	}

	if !(qualityPrecedence(Low) < qualityPrecedence(High) &&
		qualityPrecedence(High) < qualityPrecedence(Lossless) &&
		qualityPrecedence(Lossless) < qualityPrecedence(HiResLossless)) {
		t.Error("quality precedence is not strictly ordered Low < High < Lossless < HiResLossless")
	}
}

func TestTrackBestQuality(t *testing.T) {
	tests := []struct {
		name string
		tags []Tag
		want AudioQuality
	}{
		{
			name: "no tags defaults to low",
			tags: nil,
			want: Low,
		},
		{
			name: "lossless tag",
			tags: []Tag{TagLossless},
			want: Lossless,
		},
		{
			name: "hires lossless tag",
			tags: []Tag{TagHiResLossless},
			want: HiResLossless,
		},
		{
			name: "last tag wins when both present",
			tags: []Tag{TagLossless, TagHiResLossless},
			want: HiResLossless,
		},
		{
			name: "last tag wins when hires listed first",
			tags: []Tag{TagHiResLossless, TagLossless},
			want: Lossless,
		},
		{
			name: "unknown tag falls back to hires default",
			tags: []Tag{"WEIRD"},
			want: HiResLossless,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			track := Track{MediaMetadata: struct {
				Tags []Tag `json:"tags"`
			}{Tags: tt.tags}}
			if got := track.BestQuality(); got != tt.want {
				t.Errorf("BestQuality() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTrackTagQuality(t *testing.T) {
	tests := []struct {
		tag  Tag
		want AudioQuality
	}{
		{TagLossless, Lossless},
		{TagHiResLossless, HiResLossless},
		{Tag(""), HiResLossless}, // default
	}
	for _, tt := range tests {
		if got := tt.tag.Quality(); got != tt.want {
			t.Errorf("Tag(%q).Quality() = %q, want %q", tt.tag, got, tt.want)
		}
	}
}

// ─── streamClient.GetTrack ──────────────────────────────────────────────

func TestStreamClientGetTrack(t *testing.T) {
	sc, _ := newTestStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tracks/42" {
			t.Errorf("path = %s, want /tracks/42", r.URL.Path)
		}
		if r.URL.Query().Get("countryCode") != "US" {
			t.Errorf("countryCode = %q, want US", r.URL.Query().Get("countryCode"))
		}
		_, _ = io.WriteString(w, `{
			"id": 42,
			"title": "Native Track",
			"duration": 210,
			"audioQuality": "LOSSLESS",
			"mediaMetadata": {"tags": ["LOSSLESS"]},
			"artist": {"id": 1, "name": "Mock Artist"}
		}`)
	})

	track, err := sc.GetTrack(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTrack failed: %v", err)
	}
	if track.ID != 42 || track.Title != "Native Track" || track.Duration != 210 {
		t.Errorf("track = %+v", track)
	}
	if track.Artist.Name != "Mock Artist" {
		t.Errorf("artist = %+v", track.Artist)
	}
	if got := track.BestQuality(); got != Lossless {
		t.Errorf("BestQuality() = %q, want LOSSLESS", got)
	}
}

func TestStreamClientGetTrackError(t *testing.T) {
	sc, _ := newTestStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	_, err := sc.GetTrack(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %q, want HTTP 500 mention", err)
	}
}

// ─── streamClient.GetTrackStream ────────────────────────────────────────

func TestStreamClientGetTrackStreamQueryParams(t *testing.T) {
	sc, _ := newTestStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tracks/9/playbackinfopostpaywall" {
			t.Errorf("path = %s, want /tracks/9/playbackinfopostpaywall", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("audioquality"); got != "HI_RES_LOSSLESS" {
			t.Errorf("audioquality = %q, want HI_RES_LOSSLESS", got)
		}
		if got := q.Get("playbackmode"); got != "STREAM" {
			t.Errorf("playbackmode = %q, want STREAM", got)
		}
		if got := q.Get("assetpresentation"); got != "FULL" {
			t.Errorf("assetpresentation = %q, want FULL", got)
		}
		if got := q.Get("immersiveaudio"); got != "false" {
			t.Errorf("immersiveaudio = %q, want false", got)
		}
		_, _ = io.WriteString(w, streamBTSJSON(9, HiResLossless, btsManifestJSON))
	})

	stream, err := sc.GetTrackStream(context.Background(), 9, HiResLossless)
	if err != nil {
		t.Fatalf("GetTrackStream failed: %v", err)
	}
	if stream.TrackID != 9 {
		t.Errorf("TrackID = %d, want 9", stream.TrackID)
	}
	if stream.AudioQuality != HiResLossless {
		t.Errorf("AudioQuality = %q, want HI_RES_LOSSLESS", stream.AudioQuality)
	}
	wantURLs := []string{"https://example.com/seg/0.flac", "https://example.com/seg/1.flac"}
	if got := stream.Manifest.GetURLs(); !reflect.DeepEqual(got, wantURLs) {
		t.Errorf("GetURLs() = %v, want %v", got, wantURLs)
	}
}

func TestStreamClientGetTrackStreamError(t *testing.T) {
	sc, _ := newTestStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	})

	_, err := sc.GetTrackStream(context.Background(), 9, Lossless)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %q, want HTTP 403 mention", err)
	}
}

// ─── streamClient.GetSession ────────────────────────────────────────────

func TestStreamClientGetSession(t *testing.T) {
	sc, _ := newTestStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" {
			t.Errorf("path = %s, want /sessions", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{
			"sessionId": "sess-1",
			"userId": 7,
			"countryCode": "GB",
			"channelId": 3,
			"partnerId": 2,
			"client": {"id": 1, "name": "web", "authorizedForOffline": true}
		}`)
	})

	session, err := sc.GetSession(context.Background())
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if session.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", session.SessionID)
	}
	if session.UserID != 7 {
		t.Errorf("UserID = %d, want 7", session.UserID)
	}
	if session.CountryCode != "GB" {
		t.Errorf("CountryCode = %q, want GB", session.CountryCode)
	}
	if session.Client.ID != 1 || !session.Client.AuthorizedForOffline {
		t.Errorf("client = %+v", session.Client)
	}
}

func TestStreamClientGetSessionError(t *testing.T) {
	sc, _ := newTestStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})

	_, err := sc.GetSession(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// ─── streamClient.DownloadTrackStream ───────────────────────────────────

func TestStreamClientDownloadTrackStreamEmptyManifest(t *testing.T) {
	sc, _ := newTestStreamClient(t, http.NotFoundHandler().ServeHTTP)
	_, err := sc.DownloadTrackStream(context.Background(), Stream{})
	if err == nil {
		t.Fatal("expected error for nil manifest")
	}
	if !strings.Contains(err.Error(), "manifest is empty") {
		t.Errorf("err = %q, want empty manifest error", err)
	}
}

func TestStreamClientDownloadTrackStreamNoURLs(t *testing.T) {
	sc, _ := newTestStreamClient(t, http.NotFoundHandler().ServeHTTP)
	_, err := sc.DownloadTrackStream(context.Background(), Stream{Manifest: BTSManifest{}})
	if err == nil {
		t.Fatal("expected error for empty URL list")
	}
	if !strings.Contains(err.Error(), "no download URLs") {
		t.Errorf("err = %q, want no download URLs error", err)
	}
}

func TestStreamClientDownloadTrackStreamConcatenates(t *testing.T) {
	const seg1 = "seg-one-"
	const seg2 = "seg-two"

	segSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/seg/1":
			_, _ = io.WriteString(w, seg1)
		case "/seg/2":
			_, _ = io.WriteString(w, seg2)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(segSrv.Close)

	sc, _ := newTestStreamClient(t, http.NotFoundHandler().ServeHTTP)
	stream := Stream{
		Manifest: BTSManifest{URLs: []string{segSrv.URL + "/seg/1", segSrv.URL + "/seg/2"}},
	}

	rc, err := sc.DownloadTrackStream(context.Background(), stream)
	if err != nil {
		t.Fatalf("DownloadTrackStream failed: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(got) != seg1+seg2 {
		t.Errorf("data = %q, want %q", got, seg1+seg2)
	}
}
