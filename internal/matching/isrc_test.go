package matching

import "testing"

func TestNormalizeISRC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "unhyphenated", in: "USRC17607839", want: "USRC17607839"},
		{name: "hyphenated", in: "US-RC1-76-07839", want: "USRC17607839"},
		{name: "spaces and case", in: " us-rc1-76-07839 ", want: "USRC17607839"},
		{name: "empty", in: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeISRC(tc.in); got != tc.want {
				t.Fatalf("NormalizeISRC(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
