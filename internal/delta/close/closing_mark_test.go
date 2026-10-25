package close

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// writeClosingTestSession records a live session for setupNoTTYClose's
// worktree, so the closing mark has a state to land in.
func writeClosingTestSession(t *testing.T, repoPath, wtPath string) {
	t.Helper()
	if err := session.Write(session.State{
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       "feature-x",
		SessionKey:   "repo/feature-x",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

// A close marks the session closing BEFORE it signals the agent, so a frontend
// restart racing the close sees it as closed (#419, restart-preflight --peek).
func TestCloseMarksTheSessionClosingBeforeSignallingTheAgent(t *testing.T) {
	repoPath, wtPath := setupNoTTYClose(t)
	writeClosingTestSession(t, repoPath, wtPath)
	prev := requestClose
	t.Cleanup(func() { requestClose = prev })
	var markedAtSignal bool
	requestClose = func(repo, branch string) (int, string) {
		st, err := session.Read(repo, branch)
		markedAtSignal = err == nil && st.ClosingAt != nil
		return 0, ""
	}

	if err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", true, nil, "tap"); err != nil {
		t.Fatalf("forced close: %v", err)
	}
	if !markedAtSignal {
		t.Error("the session was not marked closing when its agent was signalled")
	}
}

// A close that fails after the mark (here: an agent unit that will not stop,
// without --force) clears it again, so the still-live session is not reported
// closed to a later restart.
// setupCleanClose is a clean, integrated worktree with a live session, so an
// unforced close gets past the unintegrated-work check.
func setupCleanClose(t *testing.T) (repoPath, wtPath string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repoPath = filepath.Join(t.TempDir(), "repo")
	wtPath = filepath.Join(repoPath, ".worktrees", "feature-x")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "feature-x")
	// The session's own .spinclass/ state must not make the worktree dirty.
	if err := os.WriteFile(filepath.Join(repoPath, ".git", "info", "exclude"), []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeClosingTestSession(t, repoPath, wtPath)
	return repoPath, wtPath
}

// markRestarting stamps the session as having had a restart consumed by
// restart-preflight at the given time.
func markRestarting(t *testing.T, repoPath string, at time.Time) {
	t.Helper()
	st, err := session.Read(repoPath, "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	st.RestartingAt = &at
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
}

// #419: a close that begins while a restart the preflight already consumed is
// in flight is refused, before the agent is signalled, so the relaunched
// session cannot revive what the close just tore down.
func TestCloseRefusesWhileARestartIsInFlight(t *testing.T) {
	repoPath, wtPath := setupCleanClose(t)
	markRestarting(t, repoPath, time.Now().UTC())
	prev := requestClose
	t.Cleanup(func() { requestClose = prev })
	signalled := false
	requestClose = func(string, string) (int, string) { signalled = true; return 0, "" }

	err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", false, nil, "tap")
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("want a refusal naming the restart in flight, got %v", err)
	}
	if signalled {
		t.Error("the agent was signalled despite the refusal")
	}
	st, rerr := session.Read(repoPath, "feature-x")
	if rerr != nil || st.IsTombstone() || st.ClosingAt != nil {
		t.Errorf("the session must stay live and unmarked: %+v, %v", st, rerr)
	}
}

// The in-flight window is bounded: a restart consumed long ago no longer
// blocks a close.
func TestCloseProceedsOnceTheRestartWindowHasPassed(t *testing.T) {
	repoPath, wtPath := setupCleanClose(t)
	markRestarting(t, repoPath, time.Now().UTC().Add(-time.Hour))
	prev := requestClose
	t.Cleanup(func() { requestClose = prev })
	requestClose = func(string, string) (int, string) { return 0, "" }

	if err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", false, nil, "tap"); err != nil {
		t.Fatalf("a stale restart mark must not block the close: %v", err)
	}
}

// --force closes anyway.
func TestForcedCloseIgnoresARestartInFlight(t *testing.T) {
	repoPath, wtPath := setupCleanClose(t)
	markRestarting(t, repoPath, time.Now().UTC())
	prev := requestClose
	t.Cleanup(func() { requestClose = prev })
	requestClose = func(string, string) (int, string) { return 0, "" }

	if err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", true, nil, "tap"); err != nil {
		t.Fatalf("a forced close must go through: %v", err)
	}
}

func TestAFailedCloseClearsTheClosingMark(t *testing.T) {
	// Fails only at the unit stop.
	repoPath, wtPath := setupCleanClose(t)
	prevUnit := stopUnit
	t.Cleanup(func() { stopUnit = prevUnit })
	stopUnit = func(context.Context, string, string) (bool, error) {
		return false, errors.New("cgroup still holds processes")
	}
	prev := requestClose
	t.Cleanup(func() { requestClose = prev })
	requestClose = func(string, string) (int, string) { return 0, "" }

	err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", false, nil, "tap")
	if err == nil || !strings.Contains(err.Error(), "agent unit") {
		t.Fatalf("precondition: the unforced close must fail at the unit stop, got %v", err)
	}
	st, err := session.Read(repoPath, "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if st.ClosingAt != nil {
		t.Errorf("a failed close left the session marked closing at %v", st.ClosingAt)
	}
}
