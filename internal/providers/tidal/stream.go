// Package tidal implements the Tidal music streaming plugin.
//
// This file ports the upstream Tidal client's track metadata and stream
// retrieval surface (tracks.go, stream.go, audio.go, tag.go) to the native
// plugin client.
//
// Ported from the binozo Tidal client library v0.1.0 (Apache-2.0).
// The upstream client does NO decryption and NO urlPost/signing — plain GET works.
package tidal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
)

// ─── Audio quality ─────────────────────────────────────────────────────

// AudioMode represents the audio channel configuration of a stream.
type AudioMode string

const (
	// Stereo is standard two-channel audio.
	Stereo AudioMode = "STEREO"
	// DolbyAtmos is immersive spatial audio.
	DolbyAtmos AudioMode = "DOLBY_ATMOS"
)

// AudioQuality represents the audio quality level of a stream.
type AudioQuality string

const (
	// Low is a low-bitrate stream.
	Low AudioQuality = "LOW"
	// High is a high-bitrate AAC stream.
	High AudioQuality = "HIGH"
	// Lossless is a CD-quality FLAC stream (16-bit/44.1 kHz).
	Lossless AudioQuality = "LOSSLESS"
	// HiResLossless is a high-resolution FLAC stream (up to 24-bit/192 kHz).
	HiResLossless AudioQuality = "HI_RES_LOSSLESS"
)

// qualityPrecedence returns a numeric precedence for quality comparison.
// It is the native-AudioQuality counterpart of the legacy qualityLevel helper
// previously in client.go; client.go now adopts this function and drops the
// legacy variant.
func qualityPrecedence(q AudioQuality) int {
	switch q {
	case Low:
		return 0
	case High:
		return 1
	case Lossless:
		return 2
	case HiResLossless:
		return 3
	default:
		return 0
	}
}

// ─── Track model ───────────────────────────────────────────────────────

// Tag represents a quality tag attached to a track's media metadata.
type Tag string

const (
	// TagLossless indicates CD-quality lossless audio.
	TagLossless Tag = "LOSSLESS"
	// TagHiResLossless indicates high-resolution lossless audio.
	TagHiResLossless Tag = "HIRES_LOSSLESS"
)

// Quality maps the tag to its corresponding AudioQuality value.
func (t Tag) Quality() AudioQuality {
	switch t {
	case TagLossless:
		return Lossless
	case TagHiResLossless:
		return HiResLossless
	default:
		return HiResLossless
	}
}

// Artist represents a Tidal artist with their basic profile information.
type Artist struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Handle  *string `json:"handle"`
	Type    string  `json:"type"`
	Picture string  `json:"picture"`
}

// Track represents a Tidal track with its metadata.
type Track struct {
	ID                     int      `json:"id"`
	Title                  string   `json:"title"`
	Duration               int      `json:"duration"`
	ReplayGain             float64  `json:"replayGain"`
	Peak                   float64  `json:"peak"`
	AllowStreaming         bool     `json:"allowStreaming"`
	StreamReady            bool     `json:"streamReady"`
	PayToStream            bool     `json:"payToStream"`
	AdSupportedStreamReady bool     `json:"adSupportedStreamReady"`
	DjReady                bool     `json:"djReady"`
	StemReady              bool     `json:"stemReady"`
	StreamStartDate        string   `json:"streamStartDate"`
	PremiumStreamingOnly   bool     `json:"premiumStreamingOnly"`
	TrackNumber            int      `json:"trackNumber"`
	VolumeNumber           int      `json:"volumeNumber"`
	Version                *string  `json:"version"`
	Popularity             int      `json:"popularity"`
	Copyright              string   `json:"copyright"`
	Bpm                    int      `json:"bpm"`
	Key                    string   `json:"key"`
	KeyScale               string   `json:"keyScale"`
	URL                    string   `json:"url"`
	Isrc                   string   `json:"isrc"`
	Editable               bool     `json:"editable"`
	Explicit               bool     `json:"explicit"`
	AudioQuality           string   `json:"audioQuality"`
	AudioModes             []string `json:"audioModes"`
	MediaMetadata          struct {
		Tags []Tag `json:"tags"`
	} `json:"mediaMetadata"`
	Upload      bool     `json:"upload"`
	AccessType  string   `json:"accessType"`
	Spotlighted bool     `json:"spotlighted"`
	Ai          bool     `json:"ai"`
	Artist      Artist   `json:"artist"`
	Artists     []Artist `json:"artists"`
	Album       struct {
		ID           int     `json:"id"`
		Title        string  `json:"title"`
		Cover        string  `json:"cover"`
		VibrantColor *string `json:"vibrantColor"`
		VideoCover   any     `json:"videoCover"`
	} `json:"album"`
	Mixes struct {
		TRACKMIX string `json:"TRACK_MIX"`
	} `json:"mixes"`
}

// BestQuality returns the highest available AudioQuality based on the track's
// media metadata tags. If no tags are present, it defaults to Low.
func (t *Track) BestQuality() AudioQuality {
	if len(t.MediaMetadata.Tags) == 0 {
		return Low
	}

	return t.MediaMetadata.Tags[len(t.MediaMetadata.Tags)-1].Quality()
}

// ─── Stream model ──────────────────────────────────────────────────────

// Stream represents a playable Tidal audio stream, including the manifest
// needed to download the actual audio data.
type Stream struct {
	TrackID           int          `json:"trackId"`
	AssetPresentation string       `json:"assetPresentation"`
	AudioMode         AudioMode    `json:"audioMode"`
	AudioQuality      AudioQuality `json:"audioQuality"`
	ManifestMimeType  MimeType     `json:"manifestMimeType"`
	ManifestHash      string       `json:"manifestHash"`
	Manifest          Manifest
	Info              *StreamInfo
}

// StreamInfo contains audio quality metadata such as replay gain, bit depth,
// and sample rate.
type StreamInfo struct {
	AlbumReplayGain    float64 `json:"albumReplayGain"`
	AlbumPeakAmplitude float64 `json:"albumPeakAmplitude"`
	TrackReplayGain    float64 `json:"trackReplayGain"`
	TrackPeakAmplitude float64 `json:"trackPeakAmplitude"`
	BitDepth           int     `json:"bitDepth"`
	SampleRate         int     `json:"sampleRate"`
}

// UnmarshalJSON decodes the base64-encoded manifest into a BTS (JSON urls)
// or DASH (XML SegmentTemplate+SegmentTimeline → URL list) manifest depending
// on the manifest mime type.
func (s *Stream) UnmarshalJSON(data []byte) error {
	type rawStream struct {
		TrackID            int          `json:"trackId"`
		AssetPresentation  string       `json:"assetPresentation"`
		AudioMode          AudioMode    `json:"audioMode"`
		AudioQuality       AudioQuality `json:"audioQuality"`
		ManifestMimeType   MimeType     `json:"manifestMimeType"`
		ManifestHash       string       `json:"manifestHash"`
		Manifest           string       `json:"manifest"`
		AlbumReplayGain    float64      `json:"albumReplayGain"`
		AlbumPeakAmplitude float64      `json:"albumPeakAmplitude"`
		TrackReplayGain    float64      `json:"trackReplayGain"`
		TrackPeakAmplitude float64      `json:"trackPeakAmplitude"`
		BitDepth           int          `json:"bitDepth"`
		SampleRate         int          `json:"sampleRate"`
	}

	var raw rawStream
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	s.TrackID = raw.TrackID
	s.AssetPresentation = raw.AssetPresentation
	s.AudioMode = raw.AudioMode
	s.AudioQuality = raw.AudioQuality
	s.ManifestMimeType = raw.ManifestMimeType
	s.ManifestHash = raw.ManifestHash

	if raw.Manifest == "" {
		s.Manifest = nil
		return nil
	}

	decoded, err := base64.StdEncoding.DecodeString(raw.Manifest)
	if err != nil {
		return fmt.Errorf("tidal: failed to base64-decode manifest: %w", err)
	}

	switch raw.ManifestMimeType {
	case MimeTypeBTS:
		var m BTSManifest
		if err := json.Unmarshal(decoded, &m); err != nil {
			return fmt.Errorf("tidal: failed to parse BTS manifest: %w", err)
		}
		s.Manifest = m

	case MimeTypeDASH:
		var mpd struct {
			XMLName xml.Name `xml:"MPD"`
			Period  struct {
				AdaptationSet struct {
					Representation struct {
						Codecs          string `xml:"codecs,attr"`
						SegmentTemplate struct {
							Media string `xml:"media,attr"`
						} `xml:"SegmentTemplate"`
						SegmentTimeline struct {
							S []dashSegment `xml:"S"`
						} `xml:"SegmentTimeline"`
					} `xml:"Representation"`
				} `xml:"AdaptationSet"`
			} `xml:"Period"`
		}

		if err = xml.Unmarshal(decoded, &mpd); err != nil {
			return fmt.Errorf("tidal: failed to parse DASH manifest: %w", err)
		}

		rep := mpd.Period.AdaptationSet.Representation
		urls := generateDASHURLs(rep.SegmentTemplate.Media, rep.SegmentTimeline.S)

		s.Manifest = DASHManifest{
			Codecs: rep.Codecs,
			URLs:   urls,
		}

	default:
		return fmt.Errorf("tidal: unsupported manifest type: %s", raw.ManifestMimeType)
	}

	if raw.AlbumReplayGain != 0 || raw.TrackReplayGain != 0 || raw.BitDepth != 0 {
		s.Info = &StreamInfo{
			AlbumReplayGain:    raw.AlbumReplayGain,
			AlbumPeakAmplitude: raw.AlbumPeakAmplitude,
			TrackReplayGain:    raw.TrackReplayGain,
			TrackPeakAmplitude: raw.TrackPeakAmplitude,
			BitDepth:           raw.BitDepth,
			SampleRate:         raw.SampleRate,
		}
	}

	return nil
}

// ─── streamClient ──────────────────────────────────────────────────────

// streamClient provides track metadata, stream manifest, and session access
// to Tidal's API. It wraps apiClient so it reuses the shared auth token,
// country code, and rate limiting from api.go without modifying that file.
type streamClient struct {
	api *apiClient
}

// newStreamClient creates a stream client wrapping the given API client.
func newStreamClient(api *apiClient) *streamClient {
	return &streamClient{api: api}
}

// GetTrack retrieves metadata for a track by its ID.
func (s *streamClient) GetTrack(ctx context.Context, trackID uint64) (Track, error) {
	data, err := s.api.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("%s/tracks/%d", s.api.v1BaseURL, trackID), nil)
	if err != nil {
		return Track{}, fmt.Errorf("tidal get track %d: %w", trackID, err)
	}
	var track Track
	if err := json.Unmarshal(data, &track); err != nil {
		return Track{}, fmt.Errorf("tidal get track %d unmarshal: %w", trackID, err)
	}
	return track, nil
}

// GetTrackStream retrieves playback information for a track, including the
// stream manifest needed to download the audio data. The stream is requested
// in STREAM playback mode with a FULL asset presentation and no immersive
// (Dolby Atmos) audio — matching the plugin's behavior-compatible port.
func (s *streamClient) GetTrackStream(ctx context.Context, trackID uint64, quality AudioQuality) (Stream, error) {
	params := map[string]string{
		"audioquality":      string(quality),
		"playbackmode":      "STREAM",
		"assetpresentation": "FULL",
		"immersiveaudio":    "false",
	}
	data, err := s.api.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("%s/tracks/%d/playbackinfopostpaywall", s.api.v1BaseURL, trackID), params)
	if err != nil {
		return Stream{}, fmt.Errorf("tidal get track stream %d: %w", trackID, err)
	}
	var stream Stream
	if err := json.Unmarshal(data, &stream); err != nil {
		return Stream{}, fmt.Errorf("tidal get track stream %d unmarshal: %w", trackID, err)
	}
	return stream, nil
}

// DownloadTrackStream returns an io.ReadCloser that streams the audio data
// from the given Stream's manifest URLs. The reader prefetches segments
// concurrently for improved throughput. The caller must close the reader
// when done.
func (s *streamClient) DownloadTrackStream(ctx context.Context, stream Stream) (io.ReadCloser, error) {
	if stream.Manifest == nil {
		return nil, fmt.Errorf("tidal: stream manifest is empty")
	}
	urls := stream.Manifest.GetURLs()
	if len(urls) == 0 {
		return nil, fmt.Errorf("tidal: no download URLs in stream manifest")
	}

	// Segment downloads must NOT inherit the API client's request timeout:
	// a single FLAC segment can take longer than 30s over a slow connection.
	// Use a client with no overall timeout (matching upstream go-tiddl, which
	// set Timeout=0 "important for downloads"), preserving the transport.
	dlClient := &http.Client{Transport: s.api.httpClient.Transport}

	return newMultiStreamReader(ctx, dlClient, urls), nil
}
