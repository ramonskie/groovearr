package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/sse"
)

func TestOrganizeReportEndpoint(t *testing.T) {
	s := &Server{runners: jobs.NewRunners(jobs.RunnerDeps{Log: testAPILogger()})}

	// No report yet → null body.
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/organize/report", nil)
	rec := httptest.NewRecorder()
	s.handleOrganizeReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "null\n" && got != "null" {
		t.Errorf("empty report body = %q, want null", got)
	}

	// A stored report is returned as-is.
	rep := &jobs.OrganizeReport{
		Mode:  "dry run",
		RanAt: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
		Summary: jobs.OrganizeSummary{
			Moved: 0, WouldMove: 2, InPlace: 5, Skipped: 1, Errors: 0,
		},
		Entries: []jobs.OrganizeEntry{
			{TrackID: 7, From: "/music/a/01.flac", To: "/music/a/b/01.flac", Reason: "would move"},
		},
	}
	s.runners.SetOrganizeReport(rep)

	req = httptest.NewRequest(http.MethodGet, "/api/jobs/organize/report", nil)
	rec = httptest.NewRecorder()
	s.handleOrganizeReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got jobs.OrganizeReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if got.Mode != "dry run" || got.Summary.WouldMove != 2 || len(got.Entries) != 1 {
		t.Errorf("unexpected report round-trip: %+v", got)
	}
}

func waitJobState(t *testing.T, s *Server, want jobs.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j := s.jobs.Current()
		if j != nil && j.State == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not reach %s, last state: %+v", want, j)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunPersistedJobPersistsLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		runnerErr error
		wantState jobs.State
		wantError string
	}{
		{"success persists running then completed", nil, jobs.StateCompleted, ""},
		{"cancelled persists cancelled", context.Canceled, jobs.StateCancelled, ""},
		{"failure persists failed with error", errors.New("boom"), jobs.StateFailed, "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger := testAPILogger()
			s := &Server{
				jobStatePath: filepath.Join(t.TempDir(), jobStateFileName),
				log:          logger,
				jobs:         jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
				runners:      jobs.NewRunners(jobs.RunnerDeps{Log: logger}),
			}

			// Capture what the wrapper persisted at entry — before the terminal
			// write — to prove the running → terminal ordering, not just the
			// state left on disk at the end.
			var midRunState jobs.State
			runner := func(context.Context, func(jobs.Report)) error {
				if persisted := s.loadJobState(); persisted != nil {
					midRunState = persisted.State
				}
				return tc.runnerErr
			}

			if _, err := s.jobs.Start("test-job", s.runPersistedJob("test-job", runner)); err != nil {
				t.Fatalf("start job: %v", err)
			}
			waitJobState(t, s, tc.wantState)

			if midRunState != jobs.StateRunning {
				t.Errorf("mid-run persisted state = %q, want running", midRunState)
			}
			persisted := s.loadJobState()
			if persisted == nil {
				t.Fatal("no terminal state persisted")
			}
			if persisted.Type != "test-job" {
				t.Errorf("persisted type = %q, want test-job", persisted.Type)
			}
			if persisted.State != tc.wantState {
				t.Errorf("persisted state = %q, want %q", persisted.State, tc.wantState)
			}
			if persisted.Error != tc.wantError {
				t.Errorf("persisted error = %q, want %q", persisted.Error, tc.wantError)
			}
			if persisted.FinishedAt == nil {
				t.Error("terminal state missing FinishedAt")
			}
			if persisted.StartedAt == nil {
				t.Error("terminal state missing StartedAt")
			}
		})
	}
}

func activityResp(t *testing.T, s *Server) (job *jobs.Job, activity []jobs.EnrichActivity) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/activity", nil)
	rec := httptest.NewRecorder()
	s.handleJobActivity(rec, req)
	var resp struct {
		Job      *jobs.Job             `json:"job"`
		Activity []jobs.EnrichActivity `json:"activity"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode activity response: %v", err)
	}
	return resp.Job, resp.Activity
}

func TestHandleJobActivityScopedToEnrichRun(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := context.Background()

	// Enrich run: a stale entry recorded before the job started must be
	// filtered out; the in-run entry must be returned.
	s := &Server{
		jobs:    jobs.NewManager(sse.NewSSEHub(logger), base, logger),
		runners: jobs.NewRunners(jobs.RunnerDeps{Log: logger}),
	}
	s.runners.RecordEnrichActivity(jobs.EnrichActivity{At: time.Now().UTC().Add(-time.Hour), TrackID: 1, Title: "stale"})
	if _, err := s.jobs.Start("enrich", func(ctx context.Context, report func(jobs.Report)) error {
		s.runners.RecordEnrichActivity(jobs.EnrichActivity{At: time.Now().UTC(), TrackID: 2, Title: "current"})
		return nil
	}); err != nil {
		t.Fatalf("start enrich job: %v", err)
	}
	waitJobState(t, s, jobs.StateCompleted)

	job, activity := activityResp(t, s)
	if job == nil || job.Type != "enrich" {
		t.Fatalf("want enrich job, got %+v", job)
	}
	if len(activity) != 1 {
		t.Fatalf("want 1 in-run activity entry, got %d: %+v", len(activity), activity)
	}
	if activity[0].TrackID != 2 {
		t.Fatalf("want in-run entry track 2, got %d", activity[0].TrackID)
	}

	// A non-enrich job must not expose the enrich buffer at all.
	s2 := &Server{
		jobs:    jobs.NewManager(sse.NewSSEHub(logger), base, logger),
		runners: jobs.NewRunners(jobs.RunnerDeps{Log: logger}),
	}
	s2.runners.RecordEnrichActivity(jobs.EnrichActivity{At: time.Now().UTC(), TrackID: 9, Title: "unused"})
	if _, err := s2.jobs.Start("scan", func(ctx context.Context, report func(jobs.Report)) error {
		return nil
	}); err != nil {
		t.Fatalf("start scan job: %v", err)
	}
	waitJobState(t, s2, jobs.StateCompleted)

	job2, activity2 := activityResp(t, s2)
	if job2 == nil || job2.Type != "scan" {
		t.Fatalf("want scan job, got %+v", job2)
	}
	if len(activity2) != 0 {
		t.Fatalf("scan job must not expose enrich activity, got %d entries", len(activity2))
	}
}

func TestHandleJobActivityNoJob(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{
		jobs:    jobs.NewManager(sse.NewSSEHub(logger), context.Background(), logger),
		runners: jobs.NewRunners(jobs.RunnerDeps{Log: logger}),
	}
	// A seeded buffer is irrelevant when no job has ever run.
	s.runners.RecordEnrichActivity(jobs.EnrichActivity{At: time.Now().UTC(), TrackID: 7})
	job, activity := activityResp(t, s)
	if job != nil {
		t.Fatalf("want nil job, got %+v", job)
	}
	if len(activity) != 0 {
		t.Fatalf("want empty activity with no job, got %d entries", len(activity))
	}
}
