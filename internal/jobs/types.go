package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
)

// albumTrackGroup is one album's tracks, dispatched as a unit so concurrent
// workers always enrich different albums (see Enrich).
type albumTrackGroup struct {
	AlbumID int64
	Tracks  []*domain.Track
}

// groupTracksByAlbum partitions tracks into per-album groups preserving
// first-seen (rowid) order. Same-album tracks stay together so a single
// worker can own the album without an inter-worker semaphore.
func groupTracksByAlbum(tracks []domain.Track) []albumTrackGroup {
	var order []int64
	byAlbum := make(map[int64][]*domain.Track)
	for i := range tracks {
		t := &tracks[i]
		if _, ok := byAlbum[t.AlbumID]; !ok {
			order = append(order, t.AlbumID)
		}
		byAlbum[t.AlbumID] = append(byAlbum[t.AlbumID], t)
	}
	groups := make([]albumTrackGroup, 0, len(order))
	for _, id := range order {
		groups = append(groups, albumTrackGroup{AlbumID: id, Tracks: byAlbum[id]})
	}
	return groups
}

// EnrichOutcome describes how a single track finished in the bulk enrichment
// job. Timeouts are reported separately so a slow provider pass is visible
// instead of being lumped into generic failures.
type EnrichOutcome string

const (
	EnrichOutcomeCompleted EnrichOutcome = "completed"
	EnrichOutcomeFailed    EnrichOutcome = "failed"
	EnrichOutcomeTimeout   EnrichOutcome = "timeout"
	EnrichOutcomeCancelled EnrichOutcome = "cancelled"
)

// enrichOutcomeFor maps a per-track enrichment error to the activity outcome.
// Deadline-exceeded tracks (per-track provider timeout) are reported as
// timeouts rather than generic failures.
func enrichOutcomeFor(err error) EnrichOutcome {
	switch {
	case err == nil:
		return EnrichOutcomeCompleted
	case errors.Is(err, context.DeadlineExceeded):
		return EnrichOutcomeTimeout
	default:
		return EnrichOutcomeFailed
	}
}

// EnrichActivity is one per-track event from the enrichment job, kept in a
// rolling buffer so the API/UI can show what the job is doing while it runs.
type EnrichActivity struct {
	At         time.Time     `json:"at"`
	TrackID    int64         `json:"track_id"`
	AlbumID    int64         `json:"album_id"`
	Title      string        `json:"title"`
	Outcome    EnrichOutcome `json:"outcome"`
	DurationMs int64         `json:"duration_ms"`
}

// EnrichActivityMax bounds the rolling buffer so a 33k-track job can't grow it
// without limit.
const EnrichActivityMax = 500

// OrganizeReport is the persisted result of the last organize job (dry run or
// repair), letting the UI show what would/was moved after the fact.
type OrganizeReport struct {
	Mode      string          `json:"mode"` // "dry run" | "repair" | "repair (artist)"
	RanAt     time.Time       `json:"ran_at"`
	Summary   OrganizeSummary `json:"summary"`
	Entries   []OrganizeEntry `json:"entries"`
	Truncated bool            `json:"truncated"`
}

type OrganizeSummary struct {
	Moved     int `json:"moved"`
	WouldMove int `json:"would_move"`
	InPlace   int `json:"in_place"`
	Skipped   int `json:"skipped"`
	Errors    int `json:"errors"`
}

type OrganizeEntry struct {
	TrackID int64  `json:"track_id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Reason  string `json:"reason"`
}

// maxOrganizeReportEntries bounds the persisted entry list so a huge library
// dry-run can't exhaust memory; the summary always keeps the full counts.
const maxOrganizeReportEntries = 20000

// maxOrganizeErrorEntries caps how many error rows are persisted so a run with
// many failing tracks doesn't crowd out the moved/would-move rows the report is
// meant to surface.
const maxOrganizeErrorEntries = 100
