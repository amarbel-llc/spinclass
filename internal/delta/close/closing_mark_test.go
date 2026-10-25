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
func TestAFailedCloseClearsTheClosingMark(t *testing.T) {
	// A clean, integrated worktree, so the unforced close gets past the
	// unintegrated-work check and fails only at the unit stop.
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	wtPath := filepath.Join(repoPath, ".worktrees", "feature-x")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "feature-x")
	// The session's own .spinclass/ state must not make the worktree dirty.
	if err := os.WriteFile(filepath.Join(repoPath, ".git", "info", "exclude"), []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeClosingTestSession(t, repoPath, wtPath)
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
