package config

import (
	"encoding/json"
	"testing"
)

func TestProviderDownloadPaths(t *testing.T) {
	c := &Config{
		Library: LibraryConfig{DownloadPath: "/downloads"},
		Sources: map[string]json.RawMessage{
			"soulseek":    json.RawMessage(`{"download_path":"/downloads/slskd","slskd_download_path":"/downloads"}`),
			"qbittorrent": json.RawMessage(`{"download_path":"/downloads/qbittorrent","qbt_download_root":"/downloads"}`),
			"deezer":      json.RawMessage(`{"arl":"x"}`), // no download_path → not included
			"tidal":       json.RawMessage(`{"access_token":"x"}`),
		},
	}

	// Source map iteration is unordered — compare as a set.
	got := ProviderDownloadPaths(c)
	want := map[string]bool{"/downloads": true, "/downloads/slskd": true, "/downloads/qbittorrent": true}
	if len(got) != len(want) {
		t.Fatalf("ProviderDownloadPaths() = %v, want %d paths %v", got, len(want), want)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("ProviderDownloadPaths() = %v, want paths %v", got, want)
		}
	}
}

func TestProviderDownloadPaths_Nil(t *testing.T) {
	if got := ProviderDownloadPaths(nil); got != nil {
		t.Fatalf("ProviderDownloadPaths(nil) = %v, want nil", got)
	}
}
