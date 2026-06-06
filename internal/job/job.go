// Package job tracks background merge/check jobs started by the async MCP
// tools (merge-this-session-async / check-this-session-async). State lives
// worktree-local under <worktree>/.spinclass/ alongside session state, with
// the pre-merge hook's live output streamed to a sibling job.log so
// session-job-status can tail it and derive a last-activity timestamp from
// the log's mtime.
package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Status values for a Job.
const (
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusCancelled   = "cancelled"
	StatusInterrupted = "interrupted"
)

// Kind values for a Job.
const (
	KindMerge = "merge"
	KindCheck = "check"
)

// Job is the persisted record of one background merge/check run. Exactly one
// job file lives per worktree (<worktree>/.spinclass/job.json); a new
// async-start overwrites it (the runner refuses earlier if the prior one is
// still running in this serve process).
type Job struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	GitSync     bool       `json:"git_sync"`
	ServePID    int        `json:"serve_pid"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ResultText  string     `json:"result_text,omitempty"`
	ResultIsErr bool       `json:"result_is_err,omitempty"`
	// ClownJobID correlates this job with its clown job-wakeup journal entry
	// (clown RFC-0009); empty when the job ran without clown.
	ClownJobID string `json:"clown_job_id,omitempty"`
}

func jobPath(wt string) string { return filepath.Join(wt, ".spinclass", "job.json") }

// LogPath is the live hook-output log for the worktree's current job. Its
// mtime doubles as the job's last-activity signal.
func LogPath(wt string) string { return filepath.Join(wt, ".spinclass", "job.log") }

// Write persists j to the worktree's job.json, creating .spinclass on
// demand. The write is atomic (temp file + rename, mirroring chat.Send) so a
// concurrent reader — session-job-status polls while the job goroutine
// rewrites the record — never observes a truncated file.
func Write(wt string, j *Job) error {
	if wt == "" {
		return errors.New("job.Write: worktree path required")
	}
	dir := filepath.Join(wt, ".spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".job-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, jobPath(wt)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Read loads the worktree's job record. A stale StatusRunning record whose
// owning serve process is no longer alive is reported as StatusInterrupted
// (the serve died mid-job, cutting off the goroutine and its git/hook work).
// Returns os.ErrNotExist when no job has ever run for this worktree.
func Read(wt string) (*Job, error) {
	data, err := os.ReadFile(jobPath(wt))
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	if j.Status == StatusRunning && !alive(j.ServePID) {
		j.Status = StatusInterrupted
	}
	return &j, nil
}

// Elapsed returns how long the job ran (or has been running).
func (j *Job) Elapsed() time.Duration {
	if j.EndedAt != nil {
		return j.EndedAt.Sub(j.StartedAt)
	}
	return time.Since(j.StartedAt)
}

// LastActivity returns the mtime of the job log (when the hook last produced
// output), or the job's start time if the log isn't present yet.
func LastActivity(wt string) time.Time {
	if info, err := os.Stat(LogPath(wt)); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// alive reports whether pid is a live process (mirrors session.IsAlive,
// duplicated to keep this package dependency-free).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
