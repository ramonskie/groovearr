package deezer

import (
	"encoding/json"
	"testing"
)

func TestParsePlaylistItems(t *testing.T) {
	t.Run("parses valid playlist data", func(t *testing.T) {
		raw := []any{
			map[string]any{
				"PLAYLIST_ID": "123",
				"TITLE":       "My Playlist",
				"DESCRIPTION": "A test playlist",
				"NB_SONG":     float64(10),
			},
			map[string]any{
				"PLAYLIST_ID": "456",
				"TITLE":       "Another One",
				"NB_SONG":     float64(5),
			},
		}
		items := parsePlaylistItems(raw)
		if len(items) != 2 {
			t.Fatalf("expected 2 items, got %d", len(items))
		}
		if items[0].ID != "123" || items[0].Title != "My Playlist" {
			t.Errorf("first item: ID=%q Title=%q", items[0].ID, items[0].Title)
		}
		if items[0].TrackCount != 10 {
			t.Errorf("TrackCount = %d, want 10", items[0].TrackCount)
		}
		if items[1].ID != "456" || items[1].TrackCount != 5 {
			t.Errorf("second item: ID=%q TrackCount=%d", items[1].ID, items[1].TrackCount)
		}
	})

	t.Run("empty data returns empty", func(t *testing.T) {
		items := parsePlaylistItems(nil)
		if len(items) != 0 {
			t.Errorf("expected 0 items, got %d", len(items))
		}
	})

	t.Run("skips malformed items", func(t *testing.T) {
		raw := []any{
			map[string]any{"not_a_playlist": true},
			map[string]any{"PLAYLIST_ID": "789", "TITLE": "Good", "NB_SONG": float64(3)},
		}
		items := parsePlaylistItems(raw)
		if len(items) != 1 {
			t.Fatalf("expected 1 good item, got %d", len(items))
		}
		if items[0].ID != "789" {
			t.Errorf("got ID=%q", items[0].ID)
		}
	})
}

func TestDeezerTrackInfoParsing(t *testing.T) {
	t.Run("parses track from Deezer response", func(t *testing.T) {
		raw := `{"SNG_ID":"1234","SNG_TITLE":"Test Song","ART_NAME":"Test Artist","ALB_TITLE":"Test Album","DURATION":"245","TRACK_NUMBER":"5","ISRC":"US-ABC-12-34567"}`
		var info DeezerTrackInfo
		if err := json.Unmarshal([]byte(raw), &info); err != nil {
			t.Fatal(err)
		}
		if info.ID != "1234" || info.Title != "Test Song" {
			t.Errorf("ID=%q Title=%q", info.ID, info.Title)
		}
		if info.Artist != "Test Artist" || info.Album != "Test Album" {
			t.Errorf("Artist=%q Album=%q", info.Artist, info.Album)
		}
		if info.Duration != "245" || info.ISRC != "US-ABC-12-34567" {
			t.Errorf("Duration=%q ISRC=%q", info.Duration, info.ISRC)
		}
	})

	t.Run("parses track without ISRC", func(t *testing.T) {
		raw := `{"SNG_ID":"5678","SNG_TITLE":"No ISRC","ART_NAME":"Artist","DURATION":"180"}`
		var info DeezerTrackInfo
		if err := json.Unmarshal([]byte(raw), &info); err != nil {
			t.Fatal(err)
		}
		if info.ISRC != "" {
			t.Errorf("expected empty ISRC, got %q", info.ISRC)
		}
	})
}
