package spotify

import (
	"context"
	"net/http"
	"testing"
)

func TestPlugin_GetTrackIdentity(t *testing.T) {
	p, server := newDevPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tracks/sp1" {
			t.Errorf("path = %q, want /v1/tracks/sp1", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "sp1",
			"name": "Song",
			"duration_ms": 200000,
			"track_number": 3,
			"disc_number": 1,
			"external_ids": {"isrc": "US1"},
			"artists": [{"id": "a1", "name": "Artist"}],
			"album": {"id": "al1", "name": "Album", "album_type": "album", "total_tracks": 12}
		}`))
	})
	defer server.Close()

	id, err := p.GetTrackIdentity(context.Background(), "sp1")
	if err != nil || id == nil {
		t.Fatalf("GetTrackIdentity = (%v, %v), want identity", id, err)
	}
	if id.ProviderName != "spotify" || id.ProviderTrackID != "sp1" || id.ProviderAlbumID != "al1" {
		t.Fatalf("identity = %+v", id)
	}
	if id.Title != "Song" || id.ArtistName != "Artist" || id.AlbumTitle != "Album" {
		t.Fatalf("identity text = %+v", id)
	}
	if id.AlbumTrackCount != 12 || id.TrackNumber != 3 || id.DiscNumber != 1 || id.DurationMs != 200000 || id.ISRC != "US1" {
		t.Fatalf("identity numbers = %+v", id)
	}
}

func TestPlugin_LookupTrackByISRC(t *testing.T) {
	p, server := newDevPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" {
			t.Errorf("path = %q, want /v1/search", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "isrc:US1" {
			t.Errorf("q = %q, want isrc:US1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tracks": {"items": [{
				"id": "sp1",
				"name": "Song",
				"duration_ms": 200000,
				"track_number": 1,
				"disc_number": 1,
				"external_ids": {"isrc": "US1"},
				"artists": [{"id": "a1", "name": "Artist"}],
				"album": {"id": "al1", "name": "Song", "album_type": "single", "total_tracks": 1}
			}]}
		}`))
	})
	defer server.Close()

	id, err := p.LookupTrackByISRC(context.Background(), "US1")
	if err != nil || id == nil {
		t.Fatalf("LookupTrackByISRC = (%v, %v), want identity", id, err)
	}
	if id.ProviderTrackID != "sp1" || id.ISRC != "US1" || id.AlbumTrackCount != 1 {
		t.Fatalf("identity = %+v", id)
	}
}

func TestPlugin_LookupTrackByISRC_RejectsMismatchedISRC(t *testing.T) {
	p, server := newDevPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tracks": {"items": [{
				"id": "sp1", "name": "Song", "duration_ms": 200000,
				"external_ids": {"isrc": "XX9"},
				"artists": [{"id": "a1", "name": "Artist"}],
				"album": {"id": "al1", "name": "Song", "album_type": "single", "total_tracks": 1}
			}]}
		}`))
	})
	defer server.Close()

	id, err := p.LookupTrackByISRC(context.Background(), "US1")
	if err != nil {
		t.Fatalf("LookupTrackByISRC: %v", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil for a mismatched ISRC", id)
	}
}

func TestPlugin_GetTrackIdentity_NotFound(t *testing.T) {
	p, server := newDevPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"status":404,"message":"Resource not found"}}`))
	})
	defer server.Close()

	id, err := p.GetTrackIdentity(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetTrackIdentity err = %v, want nil for a miss", err)
	}
	if id != nil {
		t.Fatalf("identity = %+v, want nil", id)
	}
}
