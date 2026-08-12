package tidal

import (
	"encoding/json"
	"testing"

	"github.com/ramonskie/groovearr/internal/quality"
)

// ─── TidalQualityToAudioQuality ─────────────────────────────────────────

func TestTidalQualityToAudioQuality(t *testing.T) {
	tests := []struct {
		name          string
		tidalQuality  string
		wantFormat    string
		wantBitrate   int
		wantSampleRate int
		wantBitDepth  int
	}{
		{
			name:          "LOSSLESS maps to FLAC",
			tidalQuality:  "LOSSLESS",
			wantFormat:    "flac",
			wantBitrate:   900,
			wantSampleRate: 44100,
			wantBitDepth:  16,
		},
		{
			name:         "HIGH maps to AAC 320",
			tidalQuality: "HIGH",
			wantFormat:   "aac",
			wantBitrate:  320,
		},
		{
			name:         "LOW maps to AAC 96",
			tidalQuality: "LOW",
			wantFormat:   "aac",
			wantBitrate:  96,
		},
		{
			name:          "HI_RES_LOSSLESS maps to FLAC 24-bit",
			tidalQuality:  "HI_RES_LOSSLESS",
			wantFormat:    "flac",
			wantBitrate:   3000,
			wantSampleRate: 192000,
			wantBitDepth:  24,
		},
		{
			name:         "unknown quality returns aac zero bitrate",
			tidalQuality: "UNKNOWN",
			wantFormat:   "aac",
			wantBitrate:  0,
		},
		{
			name:         "empty string returns aac zero bitrate",
			tidalQuality: "",
			wantFormat:   "aac",
			wantBitrate:  0,
		},
		{
			name:         "invalid value returns aac zero bitrate",
			tidalQuality: "flac",
			wantFormat:   "aac",
			wantBitrate:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aq := TidalQualityToAudioQuality(tt.tidalQuality)
			if aq.Format != tt.wantFormat {
				t.Errorf("Format = %q, want %q", aq.Format, tt.wantFormat)
			}
			if aq.Bitrate != tt.wantBitrate {
				t.Errorf("Bitrate = %d, want %d", aq.Bitrate, tt.wantBitrate)
			}
			if tt.wantSampleRate > 0 && aq.SampleRate != tt.wantSampleRate {
				t.Errorf("SampleRate = %d, want %d", aq.SampleRate, tt.wantSampleRate)
			}
			if tt.wantBitDepth > 0 && aq.BitDepth != tt.wantBitDepth {
				t.Errorf("BitDepth = %d, want %d", aq.BitDepth, tt.wantBitDepth)
			}
		})
	}
}

// ─── TierScore Validation ───────────────────────────────────────────────

func TestTidalQualityTierScore(t *testing.T) {
	// FLAC lossless should have TierScore >= 100.
	flacAQ := TidalQualityToAudioQuality("LOSSLESS")
	if flacAQ.TierScore() < 100 {
		t.Errorf("LOSSLESS TierScore = %.1f, expected >=100", flacAQ.TierScore())
	}

	// Hi-res should score higher than standard lossless.
	hiResAQ := TidalQualityToAudioQuality("HI_RES_LOSSLESS")
	if hiResAQ.TierScore() <= flacAQ.TierScore() {
		t.Errorf("HI_RES_LOSSLESS TierScore = %.1f should be > LOSSLESS = %.1f",
			hiResAQ.TierScore(), flacAQ.TierScore())
	}

	// Lossy: higher bitrate = higher score.
	highAQ := TidalQualityToAudioQuality("HIGH")
	lowAQ := TidalQualityToAudioQuality("LOW")
	if highAQ.TierScore() <= lowAQ.TierScore() {
		t.Errorf("HIGH TierScore = %.1f should be > LOW = %.1f",
			highAQ.TierScore(), lowAQ.TierScore())
	}
}

// ─── MatchesTarget ──────────────────────────────────────────────────────

func TestTidalQualityMatchesTarget(t *testing.T) {
	t.Run("FLAC matches FLAC target", func(t *testing.T) {
		flacTarget := quality.QualityTarget{Label: "FLAC", Format: "flac"}
		aq := TidalQualityToAudioQuality("LOSSLESS")
		if !aq.MatchesTarget(flacTarget) {
			t.Error("FLAC should match FLAC target")
		}
	})

	t.Run("AAC 320 matches AAC target with min 320", func(t *testing.T) {
		aacTarget := quality.QualityTarget{Label: "AAC 320", Format: "aac", MinBitrate: 320}
		aq := TidalQualityToAudioQuality("HIGH")
		if !aq.MatchesTarget(aacTarget) {
			t.Error("HIGH (AAC 320) should match AAC 320 target")
		}
	})

	t.Run("AAC 96 does NOT match AAC target with min 320", func(t *testing.T) {
		aacTarget := quality.QualityTarget{Label: "AAC 320", Format: "aac", MinBitrate: 320}
		aq := TidalQualityToAudioQuality("LOW")
		if aq.MatchesTarget(aacTarget) {
			t.Error("LOW (AAC 96) should NOT match AAC 320 target")
		}
	})

	t.Run("LOSSLESS does NOT match AAC target", func(t *testing.T) {
		aacTarget := quality.QualityTarget{Label: "AAC", Format: "aac"}
		aq := TidalQualityToAudioQuality("LOSSLESS")
		if aq.MatchesTarget(aacTarget) {
			t.Error("FLAC should NOT match AAC target")
		}
	})
}

// ─── ImageURL ───────────────────────────────────────────────────────────

func TestImageURL(t *testing.T) {
	tests := []struct {
		name      string
		coverUUID string
		width     int
		height    int
		want      string
	}{
		{
			name:      "standard UUID",
			coverUUID: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			width:     640,
			height:    640,
			want:      "https://resources.tidal.com/images/a1b2c3d4/e5f6/7890/abcd/ef1234567890/640x640.jpg",
		},
		{
			name:      "different dimensions",
			coverUUID: "00000000-1111-2222-3333-444444444444",
			width:     320,
			height:    320,
			want:      "https://resources.tidal.com/images/00000000/1111/2222/3333/444444444444/320x320.jpg",
		},
		{
			name:      "zero dimensions",
			coverUUID: "some-uuid",
			width:     0,
			height:    0,
			want:      "https://resources.tidal.com/images/some/uuid/0x0.jpg",
		},
		{
			name:      "no dashes in UUID",
			coverUUID: "simple",
			width:     1280,
			height:    1280,
			want:      "https://resources.tidal.com/images/simple/1280x1280.jpg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ImageURL(tt.coverUUID, tt.width, tt.height)
			if got != tt.want {
				t.Errorf("ImageURL(%q, %d, %d) = %q, want %q",
					tt.coverUUID, tt.width, tt.height, got, tt.want)
			}
		})
	}
}

// ─── Response Type Unmarshaling ─────────────────────────────────────────

func TestPlaylistTrackItemUnmarshal(t *testing.T) {
	t.Run("full track item", func(t *testing.T) {
		raw := `{
			"id": 12345,
			"title": "Test Track",
			"duration": 245,
			"isrc": "US-ABC-12-34567",
			"artist": {"id": 1, "name": "Test Artist"},
			"album": {"id": 10, "title": "Test Album"},
			"audioQuality": "LOSSLESS",
			"trackNumber": 5,
			"volumeNumber": 1,
			"dateAdded": "2024-01-01T00:00:00Z",
			"index": 3
		}`
		var item PlaylistTrackItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if item.ID != 12345 {
			t.Errorf("ID = %d, want 12345", item.ID)
		}
		if item.Title != "Test Track" {
			t.Errorf("Title = %q, want Test Track", item.Title)
		}
		if item.Artist.Name != "Test Artist" {
			t.Errorf("Artist = %q, want Test Artist", item.Artist.Name)
		}
		if item.Album.Title != "Test Album" {
			t.Errorf("Album = %q, want Test Album", item.Album.Title)
		}
		if item.ISRC != "US-ABC-12-34567" {
			t.Errorf("ISRC = %q, want US-ABC-12-34567", item.ISRC)
		}
		if item.Index != 3 {
			t.Errorf("Index = %d, want 3", item.Index)
		}
	})

	t.Run("minimal track item", func(t *testing.T) {
		raw := `{"id":1,"title":"Min","duration":100,"audioQuality":"LOW","artist":{"id":0,"name":""},"album":{"id":0,"title":""}}`
		var item PlaylistTrackItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if item.ID != 1 {
			t.Errorf("ID = %d, want 1", item.ID)
		}
	})
}

func TestPlaylistInfoUnmarshal(t *testing.T) {
	raw := `{
		"uuid": "abc-123",
		"title": "My Playlist",
		"description": "A test",
		"numTracks": 42,
		"type": "PLAYLIST",
		"public": true
	}`
	var info PlaylistInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if info.UUID != "abc-123" {
		t.Errorf("UUID = %q, want abc-123", info.UUID)
	}
	if info.Title != "My Playlist" {
		t.Errorf("Title = %q, want My Playlist", info.Title)
	}
	if info.NumTracks != 42 {
		t.Errorf("NumTracks = %d, want 42", info.NumTracks)
	}
}

func TestPlaylistInfoUnmarshalV2Folders(t *testing.T) {
	raw := `{
		"trn": "trn:playlist:abc-123",
		"itemType": "PLAYLIST",
		"name": "Mock Playlist Alpha",
		"data": {
			"uuid": "abc-123",
			"title": "Mock Playlist Alpha",
			"numberOfTracks": 8,
			"description": "test playlist",
			"type": "USER"
		}
	}`
	var info PlaylistInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if info.Data == nil {
		t.Fatal("Data = nil, want nested playlist data")
	}
	if info.Data.UUID != "abc-123" {
		t.Errorf("Data.UUID = %q, want abc-123", info.Data.UUID)
	}
	if info.Data.Title != "Mock Playlist Alpha" {
		t.Errorf("Data.Title = %q, want Mock Playlist Alpha", info.Data.Title)
	}
	if info.Data.NumberOfTracks != 8 {
		t.Errorf("Data.NumberOfTracks = %d, want 8", info.Data.NumberOfTracks)
	}
}

func TestPrimaryArtistName(t *testing.T) {
	main := func(name string) ArtistSearchResult {
		return ArtistSearchResult{Name: name, Type: "MAIN"}
	}
	feat := func(name string) ArtistSearchResult {
		return ArtistSearchResult{Name: name, Type: "FEATURED"}
	}

	tests := []struct {
		name    string
		artist  ArtistSearchResult
		artists []ArtistSearchResult
		want    string
	}{
		{"singular artist wins", ArtistSearchResult{Name: "Mock Artist"}, []ArtistSearchResult{feat("Mock Feat Artist")}, "Mock Artist"},
		{"prefers MAIN over FEATURED", ArtistSearchResult{}, []ArtistSearchResult{feat("Mock Feat Artist"), main("Mock Main Artist")}, "Mock Main Artist"},
		{"falls back to first named", ArtistSearchResult{}, []ArtistSearchResult{feat("Mock Feat Artist")}, "Mock Feat Artist"},
		{"skips unnamed, uses MAIN", ArtistSearchResult{}, []ArtistSearchResult{{Name: "", Type: "MAIN"}, main("Mock Main Artist")}, "Mock Main Artist"},
		{"empty when no artists", ArtistSearchResult{}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := primaryArtistName(tt.artist, tt.artists); got != tt.want {
				t.Errorf("primaryArtistName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTrackInfoUnmarshal(t *testing.T) {
	raw := `{
		"id": 9876,
		"title": "Track Title",
		"duration": 200,
		"isrc": "AA-BBB-12-34567",
		"audioQuality": "HIGH",
		"artist": {"id": 99, "name": "Artist Name"},
		"album": {"id": 50, "title": "Album Name"},
		"trackNumber": 3,
		"explicit": true,
		"streamReady": true
	}`
	var info TrackInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if info.ID != 9876 {
		t.Errorf("ID = %d, want 9876", info.ID)
	}
	if info.Title != "Track Title" {
		t.Errorf("Title = %q, want Track Title", info.Title)
	}
	if info.ISRC != "AA-BBB-12-34567" {
		t.Errorf("ISRC = %q, want AA-BBB-12-34567", info.ISRC)
	}
	if !info.Explicit {
		t.Error("Explicit should be true")
	}
	if !info.StreamReady {
		t.Error("StreamReady should be true")
	}
	if info.Artist.Name != "Artist Name" {
		t.Errorf("Artist = %q, want Artist Name", info.Artist.Name)
	}
}
