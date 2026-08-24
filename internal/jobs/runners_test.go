package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
)

func TestEnrichOutcomeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want EnrichOutcome
	}{
		{"completed", nil, EnrichOutcomeCompleted},
		{"deadline", context.DeadlineExceeded, EnrichOutcomeTimeout},
		{"wrapped deadline", errors.Join(context.DeadlineExceeded, errors.New("x")), EnrichOutcomeTimeout},
		{"generic", errors.New("boom"), EnrichOutcomeFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := enrichOutcomeFor(c.err); got != c.want {
				t.Fatalf("enrichOutcomeFor(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

func TestEnrichActivityRingBuffer(t *testing.T) {
	r := NewRunners(RunnerDeps{Log: testLogger(), Config: func() config.Config { return config.Config{} }})
	for i := 0; i < EnrichActivityMax+50; i++ {
		r.RecordEnrichActivity(EnrichActivity{
			At:      time.Now().UTC(),
			TrackID: int64(i),
		})
	}
	activity := r.EnrichActivity()
	n := len(activity)
	if n != EnrichActivityMax {
		t.Fatalf("ring buffer: want %d entries, got %d", EnrichActivityMax, n)
	}
	if activity[0].TrackID != 50 {
		t.Fatalf("ring buffer should have dropped the oldest 50, first track = %d", activity[0].TrackID)
	}
	if activity[n-1].TrackID != EnrichActivityMax+49 {
		t.Fatalf("ring buffer should keep the newest entry, last track = %d", activity[n-1].TrackID)
	}
}
