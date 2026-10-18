package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/metadata"
)

const isrcRecordingJSON = `{
	"id":"rec-1",
	"title":"Anywhere You Go (DJ Antoine vs Mad Mark 2K12 radio edit)",
	"length":214076,
	"isrcs":["CH4881102606"],
	"artist-credit":[{"name":"DJ Antoine","joinphrase":""}],
	"releases":[{
		"id":"rel-1",
		"title":"WOW",
		"status":"Official",
		"date":"2010-11-05",
		"release-group":{"id":"rg-1","title":"WOW","primary-type":"Album"},
		"media":[{"position":1,"track-count":17,"tracks":[{"id":"track-1","number":"4","position":4,"length":214076,"title":"Anywhere You Go (DJ Antoine vs Mad Mark 2K12 radio edit)"}]}]
	}]
}`

func TestLookupTrackByISRCUsesRecordingReleaseContext(t *testing.T) {
	var calls int
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/isrc/CH4881102606":
			writeJSON(w, `{"isrc":"CH4881102606","recordings":[{"id":"rec-1","title":"Anywhere You Go (DJ Antoine vs Mad Mark 2K12 radio edit)","length":214076,"isrcs":["CH4881102606"],"artist-credit":[{"name":"DJ Antoine","joinphrase":""}]}]}`)
		case "/recording/rec-1":
			writeJSON(w, isrcRecordingJSON)
		case "/release-group/rg-1":
			if got := r.URL.Query().Get("inc"); got != "releases+media" {
				t.Errorf("release-group inc = %q, want releases+media", got)
			}
			writeJSON(w, `{"releases":[{"id":"rel-1","title":"WOW","status":"Official","date":"2010-11-05","media":[{"track-count":17}]}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	id, err := client.LookupTrackByISRC(context.Background(), "CH-488-11-02606")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id == nil {
		t.Fatal("LookupTrackByISRC returned nil")
	}
	if calls != 3 {
		t.Errorf("server calls = %d, want ISRC + recording + release-group lookups", calls)
	}
	if id.ProviderName != pluginName || id.ProviderTrackID != "rec-1" || id.ProviderAlbumID != "rg-1" {
		t.Errorf("provider identity = %+v, want musicbrainz/rec-1 album rg-1", id)
	}
	if id.Title != "Anywhere You Go (DJ Antoine vs Mad Mark 2K12 radio edit)" || id.ArtistName != "DJ Antoine" {
		t.Errorf("track identity = %+v", id)
	}
	if id.AlbumTitle != "WOW" || id.AlbumType != "album" || id.AlbumTrackCount != 17 {
		t.Errorf("album context = %+v, want WOW/album/17 tracks", id)
	}
	if id.TrackNumber != 4 || id.DiscNumber != 1 || id.DurationMs != 214076 || id.ISRC != "CH4881102606" {
		t.Errorf("track context = %+v", id)
	}
}

func TestGetAlbumTracksMapsMalformedMBIDToNotFound(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid mbid", http.StatusBadRequest)
	})
	defer cleanup()

	_, err := client.GetAlbumTracks(context.Background(), "not-a-uuid")
	if !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("err = %v, want discovery.ErrNotFound", err)
	}
}

func TestLookupTrackByISRCParksAmbiguousRecordings(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/isrc/CH4881102606":
			writeJSON(w, `{"isrc":"CH4881102606","recordings":[
				{"id":"rec-1","title":"Song","length":214076,"isrcs":["CH4881102606"],"artist-credit":[{"name":"DJ Antoine","joinphrase":""}]},
				{"id":"rec-2","title":"Song","length":214076,"isrcs":["CH4881102606"],"artist-credit":[{"name":"DJ Antoine","joinphrase":""}]}
			]}`)
		case "/recording/rec-1":
			writeJSON(w, isrcRecordingJSON)
		case "/recording/rec-2":
			writeJSON(w, `{"id":"rec-2","title":"Song","length":214076,"isrcs":["CH4881102606"],"artist-credit":[{"name":"DJ Antoine","joinphrase":""}],"releases":[{"id":"rel-2","title":"Other","status":"Official","date":"2010-11-05","release-group":{"id":"rg-2","title":"Other","primary-type":"Album"},"media":[{"position":1,"track-count":2,"tracks":[{"id":"track-2","number":"1","position":1,"length":214076,"title":"Song"}]}]}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	id, err := client.LookupTrackByISRC(context.Background(), "CH4881102606")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil for an ISRC clash", id)
	}
}

func TestLookupTrackByISRCMapsRateLimit(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer cleanup()

	id, err := client.LookupTrackByISRC(context.Background(), "CH4881102606")
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("LookupTrackByISRC error = %v, want metadata.ErrRateLimited", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil on a rate limit", id)
	}
}

func TestLookupTrackByISRCRejectsMismatchedISRC(t *testing.T) {
	var calls int
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, `{"isrc":"CH4881102606","recordings":[{"id":"rec-1","title":"Other","length":1,"isrcs":["OTHER"]}]}`)
	})
	defer cleanup()

	id, err := client.LookupTrackByISRC(context.Background(), "CH4881102606")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil for a mismatched ISRC", id)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want only the ISRC lookup", calls)
	}
}

func TestGetTrackIdentityMapsRecording(t *testing.T) {
	const recordingMBID = "11111111-1111-1111-1111-111111111111"
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/recording/" + recordingMBID:
			writeJSON(w, isrcRecordingJSON)
		case "/release-group/rg-1":
			if got := r.URL.Query().Get("inc"); got != "releases+media" {
				t.Errorf("release-group inc = %q, want releases+media", got)
			}
			writeJSON(w, `{"releases":[{"id":"rel-1","title":"WOW","status":"Official","date":"2010-11-05","media":[{"track-count":17}]}]}`)
		default:
			t.Errorf("path = %s, want recording or release-group", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	id, err := client.GetTrackIdentity(context.Background(), recordingMBID)
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v", err)
	}
	if id == nil || id.ProviderTrackID != "rec-1" || id.ProviderAlbumID != "rg-1" {
		t.Fatalf("identity = %+v, want recording with release group", id)
	}
	if id.AlbumTrackCount != 17 || id.TrackNumber != 4 {
		t.Errorf("track context = %+v, want a 17-track release and track 4", id)
	}
}

func TestGetTrackIdentityUsesRepresentativeReleaseTrackCount(t *testing.T) {
	const recordingMBID = "11111111-1111-1111-1111-111111111111"
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/recording/" + recordingMBID:
			writeJSON(w, isrcRecordingJSON)
		case "/release-group/rg-1":
			writeJSON(w, `{"releases":[
				{"id":"rel-bootleg","title":"WOW","status":"Bootleg","date":"1990-01-01","media":[{"track-count":2}]},
				{"id":"rel-official","title":"WOW","status":"Official","date":"2010-11-05","media":[{"track-count":10},{"track-count":7}]}
			]}`)
		default:
			t.Errorf("path = %s, want recording or release-group", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	id, err := client.GetTrackIdentity(context.Background(), recordingMBID)
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v", err)
	}
	if id == nil {
		t.Fatal("GetTrackIdentity returned nil")
	}
	if id.AlbumTrackCount != 17 {
		t.Fatalf("AlbumTrackCount = %d, want 17 from the official multi-disc release", id.AlbumTrackCount)
	}
}

func TestGetTrackIdentityPropagatesReleaseGroupRateLimit(t *testing.T) {
	const recordingMBID = "11111111-1111-1111-1111-111111111111"
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/recording/" + recordingMBID:
			writeJSON(w, isrcRecordingJSON)
		case "/release-group/rg-1":
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			t.Errorf("path = %s, want recording or release-group", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	id, err := client.GetTrackIdentity(context.Background(), recordingMBID)
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("GetTrackIdentity error = %v, want metadata.ErrRateLimited", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil on a rate limit", id)
	}
}

func TestGetTrackIdentityKeepsIdentityWhenReleaseGroupFails(t *testing.T) {
	const recordingMBID = "11111111-1111-1111-1111-111111111111"
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/recording/" + recordingMBID:
			writeJSON(w, isrcRecordingJSON)
		case "/release-group/rg-1":
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
		default:
			t.Errorf("path = %s, want recording or release-group", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	id, err := client.GetTrackIdentity(context.Background(), recordingMBID)
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v", err)
	}
	if id == nil || id.ProviderAlbumID != "rg-1" {
		t.Fatalf("identity = %+v, want the recording identity without a count", id)
	}
	if id.AlbumTrackCount != 0 {
		t.Fatalf("AlbumTrackCount = %d, want 0 when the release-group lookup fails", id.AlbumTrackCount)
	}
}

func TestGetTrackIdentityLeavesCountUnknownWhenMediaIncomplete(t *testing.T) {
	const recordingMBID = "11111111-1111-1111-1111-111111111111"
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/recording/" + recordingMBID:
			writeJSON(w, isrcRecordingJSON)
		case "/release-group/rg-1":
			writeJSON(w, `{"releases":[{"id":"rel-1","status":"Official","date":"2010-11-05","media":[{"track-count":10},{}]}]}`)
		default:
			t.Errorf("path = %s, want recording or release-group", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	id, err := client.GetTrackIdentity(context.Background(), recordingMBID)
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v", err)
	}
	if id == nil {
		t.Fatal("GetTrackIdentity returned nil")
	}
	if id.AlbumTrackCount != 0 {
		t.Fatalf("AlbumTrackCount = %d, want 0 for incomplete media counts", id.AlbumTrackCount)
	}
}

func TestGetTrackIdentityPropagatesReleaseGroupCancellation(t *testing.T) {
	const recordingMBID = "11111111-1111-1111-1111-111111111111"
	var cancel context.CancelFunc
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/recording/" + recordingMBID:
			writeJSON(w, isrcRecordingJSON)
		case "/release-group/rg-1":
			cancel()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		default:
			t.Errorf("path = %s, want recording or release-group", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	defer stop()

	id, err := client.GetTrackIdentity(ctx, recordingMBID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetTrackIdentity error = %v, want context.Canceled", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil after cancellation", id)
	}
}

func TestGetTrackIdentityNotFound(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	defer cleanup()

	id, err := client.GetTrackIdentity(context.Background(), "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("GetTrackIdentity: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil", id)
	}
}

func TestGetTrackIdentityRejectsMalformedMBID(t *testing.T) {
	client, cleanup := newDiscoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL.Path)
	})
	defer cleanup()

	id, err := client.GetTrackIdentity(context.Background(), "not-a-uuid")
	if !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("err = %v, want discovery.ErrNotFound", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil", id)
	}
}

func TestRecordingIdentityPrefersOfficialRelease(t *testing.T) {
	identity := recordingIdentity(&RecordingInfo{
		ID:     "rec-1",
		Title:  "Song",
		Length: 200000,
		ISRCs:  []string{"US1"},
		Releases: []RecordingRelease{
			{
				ID:     "bootleg",
				Status: "Bootleg",
				Date:   "1990-01-01",
				ReleaseGroup: &RecordingReleaseGroup{
					ID: "rg-bootleg", Title: "Bootleg", PrimaryType: "Album",
				},
				Media: []RecordingMedium{{Position: 1, TrackCount: 2, Tracks: []RecordingTrack{{Position: 1}}}},
			},
			{
				ID:     "official",
				Status: "Official",
				Date:   "2000-01-01",
				ReleaseGroup: &RecordingReleaseGroup{
					ID: "rg-official", Title: "Official", PrimaryType: "Album",
				},
				Media: []RecordingMedium{{Position: 1, TrackCount: 12, Tracks: []RecordingTrack{{Position: 7}}}},
			},
		},
	}, "US1")
	if identity == nil {
		t.Fatal("recordingIdentity returned nil")
	}
	if identity.ProviderAlbumID != "rg-official" || identity.TrackNumber != 7 {
		t.Errorf("identity = %+v, want the official release context", identity)
	}
}

func TestRecordingIdentityRequiresReleaseGroup(t *testing.T) {
	identity := recordingIdentity(&RecordingInfo{
		ID:    "rec-1",
		Title: "Song",
		Releases: []RecordingRelease{{
			ID:     "rel-1",
			Status: "Official",
			Media:  []RecordingMedium{{Position: 1, TrackCount: 1, Tracks: []RecordingTrack{{Position: 1}}}},
		}},
	}, "")
	if identity != nil {
		t.Fatalf("identity = %+v, want nil without a release group", identity)
	}
}

func TestMusicBrainzIdentityIsReviewOnly(t *testing.T) {
	client := &Client{}
	if !client.IdentityReviewOnly() {
		t.Error("MusicBrainz identity must be review-only")
	}
}
