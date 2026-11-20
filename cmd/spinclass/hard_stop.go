package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/alfa/job"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// `sc stop --hard` stops a session whose clown frontend cannot honour a stop
// request (one older than the stop verdict): it records the stopped mark, then
// sends SIGTERM to the frontend pid that clown's own status file
// ($XDG_STATE_HOME/clown/status/<sha256(principal)>.json, clown#322) names for
// the session's principal. Clown forwards the signal to the provider and exits
// with no relaunch. The pid is taken only from that file, never found by
// scanning /proc, and it is signalled only when the file names this session's
// principal and session key, records the frontend's start time, the process
// with that pid has the same start time (so the pid was not reused) and its
// executable is clown.

// Seams for command tests.
var (
	hardStopProcRoot   = "/proc"
	hardStopStateHome  = defaultStateHome
	hardStopSignal     = func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
	hardStopJobRunning = func(st *session.State) bool {
		j, err := job.Read(st.WorktreePath)
		return err == nil && j.Status == job.StatusRunning
	}
)

// clownFrontendExe is the basename of the clown frontend's executable: the
// clown on PATH execs the clown-go binary, so the frontend runs as "clown".
const clownFrontendExe = "clown"

func defaultStateHome() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if filepath.IsAbs(stateHome) {
		return stateHome
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// hardStopFrontend returns the frontend pid to signal for st, or why it cannot
// be trusted.
func hardStopFrontend(st *session.State, principal string) (int, string) {
	path := hungstatus.Path(hardStopStateHome(), principal)
	s, err := hungstatus.Read(hardStopStateHome(), principal)
	switch {
	case err != nil:
		return 0, "reading clown's status file: " + err.Error()
	case s == nil:
		return 0, fmt.Sprintf("there is no clown status file for its principal (%s): its frontend is not armed, not started, or has exited, so there is no pid to signal", path)
	case s.Principal != principal:
		return 0, fmt.Sprintf("clown's status file %s names principal %q, not %s", path, s.Principal, principal)
	case s.SessionKey != "" && s.SessionKey != st.Key():
		return 0, fmt.Sprintf("clown's status file %s names session %q, not %s", path, s.SessionKey, st.Key())
	case s.FrontendPID <= 0:
		return 0, fmt.Sprintf("clown's status file %s names no frontend pid", path)
	case s.FrontendStartTicks == 0:
		return 0, fmt.Sprintf("clown's status file %s records no frontend start time, so a reused pid %d cannot be ruled out", path, s.FrontendPID)
	}
	if hungstatus.FrontendState(hardStopProcRoot, s.FrontendPID, s.FrontendStartTicks) != hungstatus.FrontendLive {
		return 0, fmt.Sprintf("frontend pid %d is not alive or was reused (its start time differs from clown's status file %s)", s.FrontendPID, path)
	}
	exe, err := os.Readlink(filepath.Join(hardStopProcRoot, fmt.Sprint(s.FrontendPID), "exe"))
	if err != nil {
		return 0, fmt.Sprintf("cannot tell what pid %d runs: %v", s.FrontendPID, err)
	}
	if filepath.Base(exe) != clownFrontendExe {
		return 0, fmt.Sprintf("pid %d is not a clown frontend (it runs %s)", s.FrontendPID, exe)
	}
	return s.FrontendPID, ""
}

// judgeHardStop is judgeOperatorStop's check for --hard: no hand-off is used,
// so its absence does not refuse; a running job refuses unless now.
func judgeHardStop(j operatorSessionTarget, now bool) operatorSessionTarget {
	st := j.st
	if st.Kind == session.KindImplicit {
		j.refusal = st.Key() + " is a main-checkout (implicit) session; only worktree sessions can be stopped"
		return j
	}
	if !now && hardStopJobRunning(st) {
		j.refusal = fmt.Sprintf("a merge or check job is running for %s; --hard --now would kill it too", st.Key())
		return j
	}
	j.pid, j.refusal = hardStopFrontend(st, j.principal)
	return j
}

// hardStopSession records the stopped mark, then signals the frontend; a
// failed signal takes the mark back. The holders are told.
func hardStopSession(j operatorSessionTarget, reason string) error {
	now := time.Now().UTC()
	if err := updateLoadedSession(j.st, func(cur *session.State) bool {
		if cur.StoppedAt != nil {
			return false
		}
		cur.StoppedAt, cur.StoppedBy, cur.StoppedReason = &now, operatorRestartRequester, reason
		return true
	}); err != nil {
		return fmt.Errorf("recording the stopped mark, so the frontend was not signalled: %w", err)
	}
	if err := hardStopSignal(j.pid); err != nil {
		_ = updateLoadedSession(j.st, func(cur *session.State) bool {
			if cur.StoppedAt == nil || !cur.StoppedAt.Equal(now) {
				return false
			}
			cur.StoppedAt, cur.StoppedBy, cur.StoppedReason = nil, "", ""
			return true
		})
		return fmt.Errorf("signalling frontend pid %d failed (%v); the stopped mark was taken back", j.pid, err)
	}
	msg := fmt.Sprintf("%s was hard-stopped (its clown frontend pid %d was sent SIGTERM) and will stay stopped (%s, by the operator)", j.st.Key(), j.pid, reason)
	wakeRestartAudience(j.st, "sc stop --hard", j.st.RestartWakeTargets(operatorRestartRequester, j.principal), msg)
	return nil
}
