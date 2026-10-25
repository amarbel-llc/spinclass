package executor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// The session's recorded PID lives in <worktree>/.spinclass/state.json, which
// the session's own agent can write, and a confined agent writes it from inside
// its unit's pid namespace. Like the principal, it is untrusted input: it is
// signalled only when the process provably belongs to the session (FDR 0035
// "Session PIDs are untrusted").

// Seams for tests.
var (
	procRoot        = "/proc"
	signalHangup    = func(pid int) error { return syscall.Kill(pid, syscall.SIGHUP) }
	unitStateDir    = confinement.DefaultStateDir
	pidAliveForStop = session.IsAlive
)

// RequestClose asks the session's process to exit with SIGHUP, but only when
// the recorded PID is provably that session's process. A confined session (one
// with a unit record or spec) is never signalled: its unit is stopped instead.
// It returns the PID it signalled (0 when none) and, when it declined, why.
func RequestClose(repoPath, branch string) (signalled int, note string) {
	st, err := session.Read(repoPath, branch)
	if err != nil {
		return 0, ""
	}
	return requestCloseOf(*st)
}

func requestCloseOf(st session.State) (signalled int, note string) {
	if st.PID <= 0 {
		return 0, ""
	}
	if hasUnitRecord(st.WorktreePath) {
		return 0, fmt.Sprintf("confined session: recorded pid %d not signalled; its agent unit is stopped instead", st.PID)
	}
	if !pidAliveForStop(st.PID) {
		return 0, ""
	}
	if !pidBelongsToSession(st.PID, st.WorktreePath, st.SessionKey) {
		return 0, fmt.Sprintf("recorded pid %d is not provably this session's process (cwd outside the worktree, no session key in its environment); not signalled", st.PID)
	}
	if err := signalHangup(st.PID); err != nil {
		return 0, fmt.Sprintf("signalling pid %d: %v", st.PID, err)
	}
	return st.PID, ""
}

// hasUnitRecord reports whether the worktree was launched confined. Any record
// or spec file counts, valid or not, so a damaged record fails closed.
func hasUnitRecord(worktree string) bool {
	if worktree == "" {
		return false
	}
	stateDir, err := unitStateDir()
	if err != nil {
		return true
	}
	for _, p := range []string{confinement.UnitRecordPath(stateDir, worktree), confinement.UnitSpecPath(stateDir, worktree)} {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}

// pidBelongsToSession proves pid is the session's process: its cwd is inside
// the worktree, or its environment carries SPINCLASS_SESSION_ID=<key>.
func pidBelongsToSession(pid int, worktree, key string) bool {
	if worktree != "" {
		if cwd, err := os.Readlink(filepath.Join(procRoot, fmt.Sprint(pid), "cwd")); err == nil && pathWithin(cwd, worktree) {
			return true
		}
	}
	if key == "" {
		return false
	}
	environ, err := os.ReadFile(filepath.Join(procRoot, fmt.Sprint(pid), "environ"))
	if err != nil {
		return false
	}
	want := []byte("SPINCLASS_SESSION_ID=" + key)
	for _, kv := range bytes.Split(environ, []byte{0}) {
		if bytes.Equal(kv, want) {
			return true
		}
	}
	return false
}

func pathWithin(p, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
