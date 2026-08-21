package tidal

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// b64 returns the standard base64 encoding of s, matching how Tidal embeds
// manifests inside stream JSON (base64-encoded payload).
func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// ─── DASH segment URL generation ────────────────────────────────────────

func TestManifestGenerateDASHURLs(t *testing.T) {
	tests := []struct {
		name     string
		template string
		timeline []dashSegment
		want     []string
	}{
		{
			name:     "single segment without repeat",
			template: "https://cdn.example/audio-$Number$.m4s",
			timeline: []dashSegment{{T: 0, D: 2000}},
			want:     []string{"https://cdn.example/audio-0.m4s"},
		},
		{
			name:     "repeat expands to r+1 segments",
			template: "https://cdn.example/audio-$Number$.m4s",
			timeline: []dashSegment{{T: 0, D: 2000, R: 2}},
			want: []string{
				"https://cdn.example/audio-0.m4s",
				"https://cdn.example/audio-1.m4s",
				"https://cdn.example/audio-2.m4s",
			},
		},
		{
			name:     "multiple timeline entries continue numbering",
			template: "https://cdn.example/audio-$Number$.m4s",
			timeline: []dashSegment{
				{T: 0, D: 2000, R: 1},
				{T: 4000, D: 2000, R: 0},
				{T: 6000, D: 1000, R: 3},
			},
			want: []string{
				"https://cdn.example/audio-0.m4s",
				"https://cdn.example/audio-1.m4s",
				"https://cdn.example/audio-2.m4s",
				"https://cdn.example/audio-3.m4s",
				"https://cdn.example/audio-4.m4s",
				"https://cdn.example/audio-5.m4s",
				"https://cdn.example/audio-6.m4s",
			},
		},
		{
			name:     "zero repeat entry yields exactly one segment",
			template: "https://cdn.example/audio-$Number$.m4s",
			timeline: []dashSegment{{T: 0, D: 2000, R: 0}},
			want:     []string{"https://cdn.example/audio-0.m4s"},
		},
		{
			name:     "template without placeholder repeats unchanged",
			template: "https://cdn.example/live.m4s",
			timeline: []dashSegment{{D: 1000, R: 1}},
			want: []string{
				"https://cdn.example/live.m4s",
				"https://cdn.example/live.m4s",
			},
		},
		{
			name:     "only first placeholder is substituted",
			template: "https://cdn.example/$Number$/$Number$.m4s",
			timeline: []dashSegment{{D: 1000}},
			want:     []string{"https://cdn.example/0/$Number$.m4s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateDASHURLs(tt.template, tt.timeline)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("generateDASHURLs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestManifestGenerateDASHURLsEmptyTimeline(t *testing.T) {
	got := generateDASHURLs("https://cdn.example/audio-$Number$.m4s", nil)
	if len(got) != 0 {
		t.Errorf("generateDASHURLs(nil timeline) = %v, want no URLs", got)
	}
}

// ─── Stream.UnmarshalJSON ───────────────────────────────────────────────

const btsManifestJSON = `{
	"mimeType": "application/vnd.tidal.bts",
	"codecs": "flac",
	"encryptionType": "NONE",
	"urls": [
		"https://example.com/seg/0.flac",
		"https://example.com/seg/1.flac"
	]
}`

const dashManifestXML = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" profiles="urn:mpeg:dash:profile:isoff-on-demand:2011">
	<Period>
		<AdaptationSet mimeType="audio/mp4" segmentAlignment="true">
			<Representation id="audio" codecs="mp4a.40.2" bandwidth="128000">
				<SegmentTemplate media="https://example.com/audio-$Number$.m4s" timescale="1000"/>
				<SegmentTimeline>
					<S t="0" d="2000" r="1"/>
					<S t="4000" d="2000" r="0"/>
				</SegmentTimeline>
			</Representation>
		</AdaptationSet>
	</Period>
</MPD>`

func TestStreamUnmarshalJSON_BTS(t *testing.T) {
	raw := `{
		"trackId": 123,
		"assetPresentation": "FULL",
		"audioMode": "STEREO",
		"audioQuality": "LOSSLESS",
		"manifestMimeType": "application/vnd.tidal.bts",
		"manifestHash": "deadbeef",
		"manifest": "` + b64(btsManifestJSON) + `",
		"albumReplayGain": -8.5,
		"trackReplayGain": -7.2,
		"bitDepth": 16,
		"sampleRate": 44100
	}`

	var s Stream
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if s.TrackID != 123 {
		t.Errorf("TrackID = %d, want 123", s.TrackID)
	}
	if s.AssetPresentation != "FULL" {
		t.Errorf("AssetPresentation = %q, want FULL", s.AssetPresentation)
	}
	if s.AudioMode != Stereo {
		t.Errorf("AudioMode = %q, want STEREO", s.AudioMode)
	}
	if s.AudioQuality != Lossless {
		t.Errorf("AudioQuality = %q, want LOSSLESS", s.AudioQuality)
	}
	if s.ManifestMimeType != MimeTypeBTS {
		t.Errorf("ManifestMimeType = %q, want %q", s.ManifestMimeType, MimeTypeBTS)
	}
	if s.ManifestHash != "deadbeef" {
		t.Errorf("ManifestHash = %q, want deadbeef", s.ManifestHash)
	}

	wantURLs := []string{"https://example.com/seg/0.flac", "https://example.com/seg/1.flac"}
	if got := s.Manifest.GetURLs(); !reflect.DeepEqual(got, wantURLs) {
		t.Errorf("GetURLs() = %v, want %v", got, wantURLs)
	}
	if got := s.Manifest.GetCodecs(); got != "flac" {
		t.Errorf("GetCodecs() = %q, want flac", got)
	}

	if s.Info == nil {
		t.Fatal("Info = nil, want stream info populated")
	}
	if s.Info.BitDepth != 16 || s.Info.SampleRate != 44100 {
		t.Errorf("Info bit depth/sample rate = %d/%d, want 16/44100", s.Info.BitDepth, s.Info.SampleRate)
	}
	if s.Info.TrackReplayGain != -7.2 || s.Info.AlbumReplayGain != -8.5 {
		t.Errorf("Info replay gain = %v/%v, want -7.2/-8.5", s.Info.TrackReplayGain, s.Info.AlbumReplayGain)
	}
}

func TestStreamUnmarshalJSON_DASH(t *testing.T) {
	raw := `{
		"trackId": 456,
		"audioQuality": "HI_RES_LOSSLESS",
		"manifestMimeType": "application/dash+xml",
		"manifest": "` + b64(dashManifestXML) + `"
	}`

	var s Stream
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if s.TrackID != 456 {
		t.Errorf("TrackID = %d, want 456", s.TrackID)
	}
	if s.AudioQuality != HiResLossless {
		t.Errorf("AudioQuality = %q, want HI_RES_LOSSLESS", s.AudioQuality)
	}
	if s.ManifestMimeType != MimeTypeDASH {
		t.Errorf("ManifestMimeType = %q, want %q", s.ManifestMimeType, MimeTypeDASH)
	}

	wantURLs := []string{
		"https://example.com/audio-0.m4s",
		"https://example.com/audio-1.m4s",
		"https://example.com/audio-2.m4s",
	}
	if got := s.Manifest.GetURLs(); !reflect.DeepEqual(got, wantURLs) {
		t.Errorf("GetURLs() = %v, want %v", got, wantURLs)
	}
	if got := s.Manifest.GetCodecs(); got != "mp4a.40.2" {
		t.Errorf("GetCodecs() = %q, want mp4a.40.2", got)
	}
}

func TestStreamUnmarshalJSON_UnsupportedMimeType(t *testing.T) {
	raw := `{
		"trackId": 1,
		"manifestMimeType": "video/mp4",
		"manifest": "` + b64(`{"urls":["https://example.com/v.mp4"]}`) + `"
	}`

	var s Stream
	err := json.Unmarshal([]byte(raw), &s)
	if err == nil {
		t.Fatal("expected error for unsupported manifest mime type")
	}
	if !strings.Contains(err.Error(), "unsupported manifest type") {
		t.Errorf("err = %q, want unsupported manifest type", err)
	}
}

func TestStreamUnmarshalJSON_EmptyManifest(t *testing.T) {
	raw := `{"trackId": 7, "manifestMimeType": "application/vnd.tidal.bts", "manifest": ""}`

	var s Stream
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if s.Manifest != nil {
		t.Errorf("Manifest = %v, want nil for empty manifest", s.Manifest)
	}
}

func TestStreamUnmarshalJSON_InvalidBase64(t *testing.T) {
	raw := `{"trackId": 7, "manifestMimeType": "application/vnd.tidal.bts", "manifest": "!!!not-base64!!!"}`

	var s Stream
	err := json.Unmarshal([]byte(raw), &s)
	if err == nil {
		t.Fatal("expected error for invalid base64 manifest")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Errorf("err = %q, want base64 decode error", err)
	}
}

func TestStreamUnmarshalJSON_InvalidBTS(t *testing.T) {
	// Valid base64, but the decoded payload is not JSON.
	raw := `{"trackId": 7, "manifestMimeType": "application/vnd.tidal.bts", "manifest": "` + b64("not-json") + `"}`

	var s Stream
	err := json.Unmarshal([]byte(raw), &s)
	if err == nil {
		t.Fatal("expected error for malformed BTS manifest")
	}
	if !strings.Contains(err.Error(), "parse BTS manifest") {
		t.Errorf("err = %q, want BTS parse error", err)
	}
}

func TestStreamUnmarshalJSON_InvalidDASH(t *testing.T) {
	// Valid base64, but the decoded payload is malformed XML.
	raw := `{"trackId": 7, "manifestMimeType": "application/dash+xml", "manifest": "` + b64("<MPD><Period></MPD>") + `"}`

	var s Stream
	err := json.Unmarshal([]byte(raw), &s)
	if err == nil {
		t.Fatal("expected error for malformed DASH manifest")
	}
	if !strings.Contains(err.Error(), "parse DASH manifest") {
		t.Errorf("err = %q, want DASH parse error", err)
	}
}

func TestStreamUnmarshalJSON_NoInfoWhenReplayGainAndBitDepthZero(t *testing.T) {
	// Info is only populated when at least one numeric info field is non-zero;
	// a lone sampleRate does not qualify.
	raw := `{
		"trackId": 8,
		"manifestMimeType": "application/vnd.tidal.bts",
		"manifest": "` + b64(btsManifestJSON) + `",
		"sampleRate": 44100
	}`

	var s Stream
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if s.Info != nil {
		t.Errorf("Info = %+v, want nil when only sampleRate set", s.Info)
	}
}

func TestStreamUnmarshalJSON_InvalidStreamJSON(t *testing.T) {
	var s Stream
	if err := json.Unmarshal([]byte("not-json"), &s); err == nil {
		t.Fatal("expected error for invalid stream JSON")
	}
}
