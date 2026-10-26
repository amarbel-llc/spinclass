// Package hungstatus reads the per-session status file clown exports
// ($XDG_STATE_HOME/clown/status/<sha256(principal)>.json, clown#322; contract in
// clown docs/features/0023, section "Hung-session status file") and classifies
// it. Clown exports facts only; the verdict is spinclass's. Everything in the
// file that the unit can reach (turn_running, last_hook_at, transcript_path) is
// untrusted input: the transcript is only ever lstat'd, never opened.
package hungstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxStatusBytes = 1 << 20

// Status is the parsed status file. A zero field means unknown, never a claim.
type Status struct {
	Principal          string
	SessionKey         string
	ClaudeSessionID    string
	TurnRunning        bool
	TurnStartedAt      time.Time
	LastHookAt         time.Time
	WrittenAt          time.Time
	FrontendPID        int
	FrontendStartTicks uint64
	Unit               string
	MainPID            int
	TranscriptPath     string
}

type wire struct {
	Version            int    `json:"version"`
	Principal          string `json:"principal"`
	SessionKey         string `json:"spinclass_session_key"`
	ClaudeSessionID    string `json:"claude_session_id"`
	TurnRunning        bool   `json:"turn_running"`
	TurnStartedAt      string `json:"turn_started_at"`
	LastHookAt         string `json:"last_hook_at"`
	WrittenAt          string `json:"written_at"`
	FrontendPID        int    `json:"frontend_pid"`
	FrontendStartTicks uint64 `json:"frontend_start_ticks"`
	Unit               string `json:"unit"`
	MainPID            int    `json:"main_pid"`
	TranscriptPath     string `json:"transcript_path"`
}

// Path is where clown writes the status of the session with this principal.
func Path(stateHome, principal string) string {
	sum := sha256.Sum256([]byte(principal))
	return filepath.Join(stateHome, "clown", "status", hex.EncodeToString(sum[:])+".json")
}

// Read returns the status for principal, or (nil, nil) when there is no file:
// not armed, not started or exited cleanly, which is unknown, not healthy. A file
// that is not a regular file, is oversized, is not version 1 or does not parse
// is an error.
func Read(stateHome, principal string) (*Status, error) {
	p := Path(stateHome, principal)
	fi, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("hung-session status %s is not a regular file", p)
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxStatusBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxStatusBytes {
		return nil, fmt.Errorf("hung-session status %s is larger than %d bytes", p, maxStatusBytes)
	}
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("hung-session status %s: %w", p, err)
	}
	if w.Version != 1 {
		return nil, fmt.Errorf("hung-session status %s has version %d, want 1", p, w.Version)
	}
	s := &Status{
		Principal: w.Principal, SessionKey: w.SessionKey, ClaudeSessionID: w.ClaudeSessionID,
		TurnRunning: w.TurnRunning, FrontendPID: w.FrontendPID, FrontendStartTicks: w.FrontendStartTicks,
		Unit: w.Unit, MainPID: w.MainPID, TranscriptPath: w.TranscriptPath,
	}
	s.TurnStartedAt = parseTime(w.TurnStartedAt)
	s.LastHookAt = parseTime(w.LastHookAt)
	s.WrittenAt = parseTime(w.WrittenAt)
	return s, nil
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// TranscriptMTime is the modification time of the transcript, or the zero time
// when the path is not an absolute regular file (a symlink, FIFO, device or
// directory, or missing). It lstat's only: the path is attacker-influenced and
// opening it could block or escape.
func TranscriptMTime(path string) time.Time {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return time.Time{}
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return time.Time{}
	}
	return fi.ModTime().UTC()
}

// Frontend is whether the status file's frontend process is alive.
type Frontend int

const (
	FrontendUnknown Frontend = iota // no pid recorded
	FrontendLive
	FrontendDead // no such pid, or the pid was reused (start ticks differ)
)

func (f Frontend) String() string {
	return [...]string{"unknown", "live", "dead"}[f]
}

// FrontendState compares pid and start ticks (field 22 of /proc/<pid>/stat) to
// tell a live frontend from a reused pid. ticks 0 means unknown, never health:
// then only the pid's existence counts.
func FrontendState(procRoot string, pid int, ticks uint64) Frontend {
	if pid <= 0 {
		return FrontendUnknown
	}
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return FrontendDead
	}
	if ticks == 0 {
		return FrontendLive
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return FrontendDead
	}
	fields := strings.Fields(s[i+1:]) // fields[0] is field 3 (state)
	if len(fields) < 20 {
		return FrontendDead
	}
	got, err := strconv.ParseUint(fields[19], 10, 64) // field 22
	if err != nil || got != ticks {
		return FrontendDead
	}
	return FrontendLive
}

// Verdict is the classification of one session.
type Verdict int

const (
	VerdictUnknown      Verdict = iota // no usable file
	VerdictOK                          // idle, or busy within the bound or excused
	VerdictBusyTooLong                 // a turn running past the bound with no sign of progress
	VerdictFrontendDead                // file present, frontend gone
)

func (v Verdict) String() string {
	return [...]string{"unknown", "ok", "busy-too-long", "frontend-dead"}[v]
}

// Input is everything Classify needs; the caller supplies the job state and the
// liveness, so Classify is a pure function.
type Input struct {
	Status          *Status
	Frontend        Frontend
	TranscriptMTime time.Time // zero when unknown
	JobRunning      bool      // a spinclass merge or check job is running for the session
	Now             time.Time
	Bound           time.Duration
}

// Classify decides one session's verdict and says why. Busy too long needs the
// turn to have run past Bound AND neither the last hook nor the transcript to
// have moved within Bound. A running merge or check job excuses a busy turn
// (the gate has its own watchdog). A dead frontend is not a hang.
func Classify(in Input) (Verdict, string) {
	s := in.Status
	if s == nil {
		return VerdictUnknown, "no status file (not armed, not started, or exited cleanly)"
	}
	if in.Frontend == FrontendDead {
		return VerdictFrontendDead, fmt.Sprintf("frontend pid %d is not alive (or was reused)", s.FrontendPID)
	}
	if !s.TurnRunning {
		return VerdictOK, "idle"
	}
	if s.TurnStartedAt.IsZero() {
		return VerdictUnknown, "turn running but no start time"
	}
	age := in.Now.Sub(s.TurnStartedAt)
	if age <= in.Bound {
		return VerdictOK, fmt.Sprintf("busy %s, within %s", age.Round(time.Second), in.Bound)
	}
	if in.JobRunning {
		return VerdictOK, fmt.Sprintf("busy %s but a merge or check job is running", age.Round(time.Second))
	}
	if !s.LastHookAt.IsZero() && in.Now.Sub(s.LastHookAt) <= in.Bound {
		return VerdictOK, fmt.Sprintf("busy %s but a hook fired %s ago", age.Round(time.Second), in.Now.Sub(s.LastHookAt).Round(time.Second))
	}
	if !in.TranscriptMTime.IsZero() && in.Now.Sub(in.TranscriptMTime) <= in.Bound {
		return VerdictOK, fmt.Sprintf("busy %s but the transcript moved %s ago", age.Round(time.Second), in.Now.Sub(in.TranscriptMTime).Round(time.Second))
	}
	return VerdictBusyTooLong, fmt.Sprintf("turn running %s, over the %s bound, no hook or transcript progress", age.Round(time.Second), in.Bound)
}
