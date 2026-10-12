package config

import (
	"encoding/json"
	"testing"
)

func intP(v int) *int    { return &v }
func boolP(v bool) *bool { return &v }

// Clone must isolate every reference type Merge writes through: a trial merge
// into a clone must not mutate the original's map, slices, or pointers.
func TestCloneIsolatesMergeTarget(t *testing.T) {
	orig := Config{
		Sources:       map[string]json.RawMessage{"deezer": json.RawMessage(`{"arl":"secret"}`)},
		Logging:       &LoggingConfig{Level: "info", Compress: boolP(true), AccessLog: boolP(false)},
		MetadataOrder: []string{"deezer"},
		DownloadOrder: []string{"soulseek"},
		AlbumSources:  []string{"prowlarr"},
		Auth:          AuthConfig{LocalBypassSubnets: []string{"10.0.0.0/8"}},
	}
	orig.Library.PlaylistAutoSyncMins = intP(30)
	orig.Tracking.RefreshMins = intP(720)
	orig.Tracking.AutoSearchMissing = boolP(false)

	clone := Clone(orig)
	clone.Merge(&Config{
		Sources:       map[string]json.RawMessage{"deezer": json.RawMessage(`{"arl":"changed"}`)},
		Logging:       &LoggingConfig{Level: "debug", MaxSizeMB: 99, Compress: boolP(false), AccessLog: boolP(true)},
		MetadataOrder: []string{"musicbrainz"},
		DownloadOrder: []string{"deezer"},
		AlbumSources:  []string{"deezer"},
		Auth:          AuthConfig{LocalBypassSubnets: []string{"192.168.1.0/24"}},
		Library:       LibraryConfig{PlaylistAutoSyncMins: intP(60)},
		Tracking:      TrackingConfig{RefreshMins: intP(1), AutoSearchMissing: boolP(true)},
	})

	// The clone absorbed the merge.
	if clone.Logging.Level != "debug" || clone.Logging.MaxSizeMB != 99 {
		t.Fatalf("clone logging = %+v, want debug/99", clone.Logging)
	}
	if string(clone.Sources["deezer"]) != `{"arl":"changed"}` {
		t.Fatalf("clone source = %s", clone.Sources["deezer"])
	}

	// The original is untouched.
	if string(orig.Sources["deezer"]) != `{"arl":"secret"}` {
		t.Fatalf("original source mutated: %s", orig.Sources["deezer"])
	}
	if orig.Logging.Level != "info" || orig.Logging.MaxSizeMB != 0 {
		t.Fatalf("original logging mutated: %+v", orig.Logging)
	}
	// Merge reassigns the *bool fields on the clone; the original's pointees
	// must survive.
	if orig.Logging.Compress == nil || *orig.Logging.Compress != true || orig.Logging.AccessLog == nil || *orig.Logging.AccessLog != false {
		t.Fatalf("original logging pointers mutated: %+v", orig.Logging)
	}
	if clone.Logging.Compress == nil || *clone.Logging.Compress != false || clone.Logging.AccessLog == nil || *clone.Logging.AccessLog != true {
		t.Fatalf("clone logging pointers not merged: %+v", clone.Logging)
	}
	if orig.MetadataOrder[0] != "deezer" || orig.DownloadOrder[0] != "soulseek" || orig.AlbumSources[0] != "prowlarr" {
		t.Fatalf("original slices mutated: %v %v %v", orig.MetadataOrder, orig.DownloadOrder, orig.AlbumSources)
	}
	if orig.Auth.LocalBypassSubnets[0] != "10.0.0.0/8" {
		t.Fatalf("original bypass subnets mutated: %v", orig.Auth.LocalBypassSubnets)
	}
	if *orig.Library.PlaylistAutoSyncMins != 30 {
		t.Fatalf("original PlaylistAutoSyncMins mutated: %d", *orig.Library.PlaylistAutoSyncMins)
	}
	if *orig.Tracking.RefreshMins != 720 || *orig.Tracking.AutoSearchMissing != false {
		t.Fatalf("original tracking pointers mutated: %v %v", *orig.Tracking.RefreshMins, *orig.Tracking.AutoSearchMissing)
	}
}
