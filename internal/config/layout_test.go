package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPathIsUnder(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		root     string
		expected bool
	}{
		{"exact match", "/downloads", "/downloads", true},
		{"direct child", "/downloads/slskd", "/downloads", true},
		{"nested", "/downloads/a/b/c", "/downloads", true},
		{"trailing slash on root", "/downloads/slskd", "/downloads/", true},
		{"trailing slash on path", "/downloads/", "/downloads", true},
		{"sibling", "/downloads", "/music", false},
		{"sibling sharing prefix is not under", "/downloads2", "/downloads", false},
		{"parent is not under child", "/downloads", "/downloads/slskd", false},
		{"case-insensitive exact", "/DOWNLOADS", "/downloads", true},
		{"case-insensitive child", "/Downloads/music", "/downloads", true},
		{"case-insensitive mixed", "/downloads/Slskd", "/DOWNLOADS", true},
		{"empty path", "", "/downloads", false},
		{"empty root", "/downloads", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PathIsUnder(tc.path, tc.root); got != tc.expected {
				t.Errorf("PathIsUnder(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.expected)
			}
		})
	}
}

func layoutErrors(errs []string) []string {
	var out []string
	for _, e := range errs {
		if strings.Contains(e, "library_path") || strings.Contains(e, "download directory") {
			out = append(out, e)
		}
	}
	return out
}

func TestValidateLayoutAcceptsSiblings(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Library.LibraryPath = "/music"
	cfg.Library.DownloadPath = "/downloads"
	// A provider path as a sibling of the library is fine too.
	cfg.Sources["soulseek"] = json.RawMessage(`{"download_path":"/downloads/slskd"}`)

	if errs := layoutErrors(cfg.Validate()); len(errs) > 0 {
		t.Errorf("sibling layout should be valid, got: %v", errs)
	}
}

func TestValidateLayoutRefusesOverlap(t *testing.T) {
	cases := []struct {
		name  string
		lib   string
		dl    string
		field string // expected substring in the error message
	}{
		{"library under download root", "/downloads/music", "/downloads", "must not be inside"},
		{"library equals download root", "/downloads", "/downloads", "must not be inside"},
		{"download nested under library", "/music", "/music/downloads", "must not be inside"},
		{"case-insensitive overlap", "/MUSIC", "/music", "must not be inside"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Library.LibraryPath = tc.lib
			cfg.Library.DownloadPath = tc.dl
			found := false
			for _, e := range layoutErrors(cfg.Validate()) {
				if strings.Contains(e, tc.field) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected layout error for lib=%q dl=%q, got: %v", tc.lib, tc.dl, cfg.Validate())
			}
		})
	}
}

func TestValidateLayoutFlagsProviderOverlap(t *testing.T) {
	// A provider staging dir nested inside the library path is caught even
	// though the general download root is a clean sibling.
	cfg := DefaultConfig()
	cfg.Library.LibraryPath = "/music"
	cfg.Library.DownloadPath = "/downloads"
	cfg.Sources["soulseek"] = json.RawMessage(`{"download_path":"/music/slskd"}`)

	found := false
	for _, e := range layoutErrors(cfg.Validate()) {
		if strings.Contains(e, "/music/slskd") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected provider-overlap layout error, got: %v", cfg.Validate())
	}
}

func TestValidateLayoutIgnoresProviderOutsideLibrary(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Library.LibraryPath = "/music"
	cfg.Library.DownloadPath = "/downloads"
	cfg.Sources["soulseek"] = json.RawMessage(`{"download_path":"/downloads/slskd"}`)
	cfg.Sources["qbittorrent"] = json.RawMessage(`{"download_path":"/torrents"}`)

	if errs := layoutErrors(cfg.Validate()); len(errs) > 0 {
		t.Errorf("provider paths outside library should be valid, got: %v", errs)
	}
}
