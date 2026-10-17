package matching

import (
	"testing"
	"time"
)

func TestCandidateScore(t *testing.T) {
	tests := []struct {
		name    string
		signals CandidateSignals
		want    int
	}{
		{
			name:    "no evidence",
			signals: CandidateSignals{},
			want:    0,
		},
		{
			name:    "title only",
			signals: CandidateSignals{TitleMatches: true},
			want:    35,
		},
		{
			name:    "title and artist",
			signals: CandidateSignals{TitleMatches: true, ArtistMatches: true},
			want:    55,
		},
		{
			name:    "title artist and duration auto-applies",
			signals: CandidateSignals{TitleMatches: true, ArtistMatches: true, DurationKnown: true, DurationWithinTolerance: true},
			want:    75,
		},
		{
			name:    "isrc alone is review",
			signals: CandidateSignals{ISRCExact: true},
			want:    60,
		},
		{
			name:    "isrc plus title auto-applies",
			signals: CandidateSignals{ISRCExact: true, TitleMatches: true},
			want:    95,
		},
		{
			name:    "isrc title artist duration caps at 100",
			signals: CandidateSignals{ISRCExact: true, TitleMatches: true, ArtistMatches: true, DurationKnown: true, DurationWithinTolerance: true},
			want:    100,
		},
		{
			name:    "known duration conflict caps a strong id at review",
			signals: CandidateSignals{ISRCExact: true, TitleMatches: true, ArtistMatches: true, DurationKnown: true, DurationWithinTolerance: false},
			want:    74,
		},
		{
			name:    "provider track id plus title",
			signals: CandidateSignals{ProviderTrackIDExact: true, TitleMatches: true},
			want:    85,
		},
		{
			name:    "provider track id alone is review floor",
			signals: CandidateSignals{ProviderTrackIDExact: true},
			want:    50,
		},
		{
			name:    "provider track id with duration conflict caps at review",
			signals: CandidateSignals{ProviderTrackIDExact: true, TitleMatches: true, ArtistMatches: true, DurationKnown: true, DurationWithinTolerance: false},
			want:    74,
		},
		{
			name:    "album and count with title and artist is review",
			signals: CandidateSignals{TitleMatches: true, ArtistMatches: true, AlbumTitleMatches: true, TrackCountMatches: true},
			want:    65,
		},
		{
			name:    "album count and track number stay review",
			signals: CandidateSignals{TitleMatches: true, ArtistMatches: true, AlbumTitleMatches: true, TrackCountMatches: true, TrackNumberMatches: true},
			want:    70,
		},
		{
			name:    "album count and duration auto-applies",
			signals: CandidateSignals{TitleMatches: true, ArtistMatches: true, AlbumTitleMatches: true, TrackCountMatches: true, DurationKnown: true, DurationWithinTolerance: true},
			want:    85,
		},
		{
			name:    "track number alone is weak",
			signals: CandidateSignals{TrackNumberMatches: true},
			want:    5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CandidateScore(tt.signals); got != tt.want {
				t.Fatalf("CandidateScore(%+v) = %d, want %d", tt.signals, got, tt.want)
			}
		})
	}
}

func TestDurationWithinTolerance(t *testing.T) {
	const tol = 5 * time.Second
	tests := []struct {
		name      string
		localMs   int64
		candidate int64
		want      bool
	}{
		{"equal", 240000, 240000, true},
		{"exactly at tolerance", 240000, 245000, true},
		{"just over tolerance", 240000, 245001, false},
		{"negative delta within", 245000, 240000, true},
		{"unknown local", 0, 240000, false},
		{"unknown candidate", 240000, 0, false},
		{"negative local", -1, 240000, false},
		{"negative candidate", 240000, -1, false},
		{"both unknown", 0, 0, false},
		{"max duration equal", 1<<62 - 1, 1<<62 - 1, true},
		{"max duration far apart", 1<<62 - 1, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DurationWithinTolerance(tt.localMs, tt.candidate, tol); got != tt.want {
				t.Fatalf("DurationWithinTolerance(%d, %d, %v) = %v, want %v",
					tt.localMs, tt.candidate, tol, got, tt.want)
			}
		})
	}

	if !DurationWithinTolerance(1000, 1000, -time.Second) {
		t.Fatal("negative tolerance should clamp to zero, not reject equal durations")
	}
}
