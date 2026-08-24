package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ramonskie/groovearr/internal/jobs"
)

// jobStateFileName is the file (inside the config dir) holding the last
// background job snapshot. It survives restarts so an interrupted job (e.g.
// container killed mid-run) is visible after boot instead of vanishing.
const jobStateFileName = "job.json"

// loadJobState reads the persisted job snapshot; nil when absent or corrupt.
func (s *Server) loadJobState() *jobs.Job {
	s.jobStateMu.Lock()
	defer s.jobStateMu.Unlock()
	if s.jobStatePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.jobStatePath)
	if err != nil {
		return nil
	}
	var j jobs.Job
	if err := json.Unmarshal(data, &j); err != nil {
		return nil
	}
	return &j
}

// saveJobState persists a job snapshot atomically (temp + rename in the same
// directory) so a kill mid-write never leaves a corrupt file.
func (s *Server) saveJobState(j *jobs.Job) {
	if s.jobStatePath == "" {
		return
	}
	data, err := json.Marshal(j)
	if err != nil {
		s.log.Error("marshal job state failed", "error", err, "component", "jobs")
		return
	}

	s.jobStateMu.Lock()
	defer s.jobStateMu.Unlock()
	tmp, err := os.CreateTemp(filepath.Dir(s.jobStatePath), ".job-state-*.tmp")
	if err != nil {
		s.log.Error("create job state temp failed", "error", err, "component", "jobs")
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		s.log.Error("write job state temp failed", "error", err, "component", "jobs")
		return
	}
	if err := tmp.Close(); err != nil {
		s.log.Error("close job state temp failed", "error", err, "component", "jobs")
		return
	}
	if err := os.Rename(tmpName, s.jobStatePath); err != nil {
		s.log.Error("rename job state failed", "error", err, "component", "jobs")
	}
}

// restoreInterruptedJob loads the persisted job state at startup. A job left
// in "running" (process died mid-run) is marked interrupted and persisted so
// the UI shows what happened after a restart. The snapshot is kept in memory
// so /api/jobs returns it until a new job runs in this process.
func (s *Server) restoreInterruptedJob() {
	j := s.loadJobState()
	if j == nil {
		return
	}
	if j.State == jobs.StateRunning {
		j.State = jobs.StateInterrupted
		now := time.Now().UTC()
		j.FinishedAt = &now
		s.saveJobState(j)
	}
	s.runners.SetBootJob(j)
}
