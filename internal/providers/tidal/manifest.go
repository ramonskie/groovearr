// Package tidal implements the Tidal music streaming plugin.
//
// This file ports the upstream Tidal client's manifest handling (manifest.go)
// to the native plugin client.
//
// Ported from the binozo Tidal client library v0.1.0 (Apache-2.0).
// The upstream client does NO decryption and NO urlPost/signing — plain GET works.
package tidal

import (
	"strconv"
	"strings"
)

// MimeType identifies the format of a Tidal stream manifest.
type MimeType string

const (
	// MimeTypeBTS is the BTS (binary track stream) manifest format.
	MimeTypeBTS MimeType = "application/vnd.tidal.bts"
	// MimeTypeDASH is the DASH (MPEG-DASH) manifest format.
	MimeTypeDASH MimeType = "application/dash+xml"
)

// Manifest provides a common interface for accessing stream URLs and codec
// information from different manifest types.
type Manifest interface {
	GetURLs() []string
	GetCodecs() string
}

// BTSManifest represents a BTS-format stream manifest containing direct URLs.
type BTSManifest struct {
	MimeType       string   `json:"mimeType"`
	Codecs         string   `json:"codecs"`
	EncryptionType string   `json:"encryptionType"`
	URLs           []string `json:"urls"`
}

// GetURLs returns the download URLs for the stream segments.
func (b BTSManifest) GetURLs() []string {
	return b.URLs
}

// GetCodecs returns the codec identifier for the stream.
func (b BTSManifest) GetCodecs() string {
	return b.Codecs
}

// DASHManifest represents a DASH-format stream manifest with resolved segment URLs.
type DASHManifest struct {
	Codecs string
	URLs   []string
}

// GetURLs returns the download URLs for the stream segments.
func (d DASHManifest) GetURLs() []string {
	return d.URLs
}

// GetCodecs returns the codec identifier for the stream.
func (d DASHManifest) GetCodecs() string {
	return d.Codecs
}

// dashSegment describes one DASH SegmentTimeline entry: a run of segments of
// duration d, repeated r+1 times.
type dashSegment struct {
	T int `xml:"t,attr"`
	D int `xml:"d,attr"`
	R int `xml:"r,attr"`
}

// generateDASHURLs expands a SegmentTemplate media URL against a
// SegmentTimeline into concrete segment URLs. Each timeline entry covers
// r+1 segments; the template's "$Number$" placeholder is replaced with the
// zero-based segment index.
func generateDASHURLs(template string, timeline []dashSegment) []string {
	var urls []string
	segNum := 0

	for _, s := range timeline {
		repeats := s.R + 1
		for i := 0; i < repeats; i++ {
			url := strings.Replace(template, "$Number$", strconv.Itoa(segNum), 1)
			urls = append(urls, url)
			segNum++
		}
	}
	return urls
}
