package domain

import "testing"

// TestMonitorModeValues pins the persisted wire values of MonitorMode. These
// strings round-trip through SQLite and JSON, so drift would silently corrupt
// stored tracking state.
func TestMonitorModeValues(t *testing.T) {
	tests := []struct {
		name string
		mode MonitorMode
		want string
	}{
		{"all", MonitorModeAll, "all"},
		{"future", MonitorModeFuture, "future"},
		{"none", MonitorModeNone, "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.mode); got != tt.want {
				t.Errorf("MonitorMode %s = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// TestAlbumStatusValues pins the persisted wire values of AlbumStatus.
func TestAlbumStatusValues(t *testing.T) {
	tests := []struct {
		name   string
		status AlbumStatus
		want   string
	}{
		{"wanted", AlbumStatusWanted, "wanted"},
		{"downloading", AlbumStatusDownloading, "downloading"},
		{"downloaded", AlbumStatusDownloaded, "downloaded"},
		{"ignored", AlbumStatusIgnored, "ignored"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.status); got != tt.want {
				t.Errorf("AlbumStatus %s = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
