package strutil

import "testing"

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Acda en de Munnik", "acdaendemunnik"},
		{"Acda en De Munnik", "acdaendemunnik"},
		{"Danny de Munk", "dannydemunk"},
		{"Édith Piaf", "edithpiaf"},
		{"AC/DC", "acdc"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeName(tt.in); got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
