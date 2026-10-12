package config

import (
	"testing"
	"time"
)

func TestCatalogConfigIsEnabled(t *testing.T) {
	tests := []struct {
		name string
		c    CatalogConfig
		want bool
	}{
		{name: "nil defaults enabled", c: CatalogConfig{}, want: true},
		{name: "explicit true", c: CatalogConfig{Enabled: boolPtr(true)}, want: true},
		{name: "explicit false", c: CatalogConfig{Enabled: boolPtr(false)}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.IsEnabled(); got != tc.want {
				t.Fatalf("IsEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCatalogConfigAlbumStaleAfter(t *testing.T) {
	tests := []struct {
		name string
		days int
		want time.Duration
	}{
		{name: "zero defaults to 30d", days: 0, want: DefaultAlbumRefreshDays * 24 * time.Hour},
		{name: "negative defaults to 30d", days: -1, want: DefaultAlbumRefreshDays * 24 * time.Hour},
		{name: "custom days", days: 7, want: 7 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (CatalogConfig{AlbumRefreshDays: tc.days}).AlbumStaleAfter(); got != tc.want {
				t.Fatalf("AlbumStaleAfter = %v, want %v", got, tc.want)
			}
		})
	}
}
