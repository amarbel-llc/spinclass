package main

import (
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// trackedWorktreeOnAnotherBranch builds a repo with a tracked session at
// .worktrees/feat and then checks a session-owned branch out in that worktree,
// the move a grit_branch rebase of a `<session>--*` branch makes. The session's
// record stays keyed on the worktree directory, not on whatever branch happens
// to be checked out (spinclass#461).
func trackedWorktreeOnAnotherBranch(t *testing.T) (repoDir, wtPath string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "")
	repoDir = filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repoDir)
	wtPath = filepath.Join(repoDir, ".worktrees", "feat")
	if out, err := osexec.Command("git", "-C", repoDir, "worktree", "add", "-b", "feat", wtPath).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	if err := session.Write(session.State{
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repoDir,
		WorktreePath: wtPath,
		Branch:       "feat",
		SessionKey:   "myrepo/feat",
		Description:  "before",
	}); err != nil {
		t.Fatalf("session.Write: %v", err)
	}
	if out, err := osexec.Command("git", "-C", wtPath, "checkout", "-q", "-b", "feat--probe").CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b: %v\n%s", err, out)
	}
	t.Chdir(wtPath)
	return repoDir, wtPath
}

func assertDescriptionOnTheWorktreeSession(t *testing.T, repoDir string) {
	t.Helper()
	got, err := session.Read(repoDir, "feat")
	if err != nil {
		t.Fatalf("session.Read(feat): %v", err)
	}
	if got.Description != "after" {
		t.Errorf("description = %q, want %q on the worktree's own session", got.Description, "after")
	}
	if _, err := session.Read(repoDir, "feat--probe"); err == nil {
		t.Errorf("a session record appeared for the checked-out branch feat--probe; the record must stay keyed on the worktree")
	}
}

func TestHandleUpdateDescriptionWithAnotherBranchCheckedOut(t *testing.T) {
	repoDir, _ := trackedWorktreeOnAnotherBranch(t)

	res, err := handleUpdateDescription(context.Background(), json.RawMessage(`{"description":"after"}`), nil)
	if err != nil {
		t.Fatalf("handleUpdateDescription: %v", err)
	}
	if res.IsErr {
		t.Fatalf("update refused with another branch checked out: %s", res.Text)
	}
	assertDescriptionOnTheWorktreeSession(t, repoDir)
}

func TestRunUpdateDescriptionWithAnotherBranchCheckedOut(t *testing.T) {
	repoDir, _ := trackedWorktreeOnAnotherBranch(t)

	if err := runUpdateDescription("after", ""); err != nil {
		t.Fatalf("runUpdateDescription: %v", err)
	}
	assertDescriptionOnTheWorktreeSession(t, repoDir)
}

// The chat identity derived from the worktree (no $SPINCLASS_SESSION_ID) is
// the worktree session's key whatever branch is checked out, and the fallback
// writes no record for that branch (spinclass#461).
func TestCurrentSessionKeyWithAnotherBranchCheckedOut(t *testing.T) {
	repoDir, _ := trackedWorktreeOnAnotherBranch(t)

	key, err := currentSessionKey()
	if err != nil {
		t.Fatalf("currentSessionKey: %v", err)
	}
	if key != "myrepo/feat" {
		t.Errorf("key = %q, want the worktree session's key myrepo/feat", key)
	}
	if _, err := session.Read(repoDir, "feat--probe"); err == nil {
		t.Errorf("a session record appeared for the checked-out branch feat--probe")
	}
}

// The heal of an untracked worktree keys on the worktree directory too: a
// worktree whose checked-out branch is not its directory name still heals
// into a record a later Read at (repo, dirname) finds (spinclass#461).
func TestHandleUpdateDescriptionHealsByWorktreeDirectory(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "")
	repoDir := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repoDir)
	wtPath := filepath.Join(repoDir, ".worktrees", "feat")
	if out, err := osexec.Command("git", "-C", repoDir, "worktree", "add", "-b", "feat--probe", wtPath).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	t.Chdir(wtPath)

	res, err := handleUpdateDescription(context.Background(), json.RawMessage(`{"description":"after"}`), nil)
	if err != nil {
		t.Fatalf("handleUpdateDescription: %v", err)
	}
	if res.IsErr {
		t.Fatalf("heal refused: %s", res.Text)
	}
	got, err := session.Read(repoDir, "feat")
	if err != nil {
		t.Fatalf("session.Read(feat) after heal: %v", err)
	}
	if got.Description != "after" || got.WorktreePath != wtPath {
		t.Errorf("healed state = %+v, want description after at worktree %s", got, wtPath)
	}
}
