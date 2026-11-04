package main

import (
	"context"
	"encoding/json"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// sessionWorktreeWithForeignBranch builds a repo (its sweatfile from the
// argument, committed on the default branch) with a worktree session at
// .worktrees/feature and a session-owned branch feature--probe checked out in
// it, then chdirs there. $HOME is pinned to the repo's parent so the sweatfile
// cascade stops there (see TestResolveSession).
func sessionWorktreeWithForeignBranch(t *testing.T, sweatfile string) (repo, wt string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "")
	base := t.TempDir()
	t.Setenv("HOME", base)
	repo = filepath.Join(base, "repo")
	testgit.MustInit(t, repo)
	if sweatfile != "" {
		writeSweatfile(t, repo, sweatfile)
	}
	wt = filepath.Join(repo, ".worktrees", "feature")
	testgit.MustWorktreeAdd(t, repo, wt, "feature")
	if out, err := osexec.Command("git", "-C", wt, "checkout", "-q", "-b", "feature--probe").CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b: %v\n%s", err, out)
	}
	t.Chdir(wt)
	return repo, wt
}

func assertNamesBothBranches(t *testing.T, msg string) {
	t.Helper()
	if !strings.Contains(msg, "feature--probe") || !strings.Contains(msg, " feature") {
		t.Errorf("refusal names neither the checked-out nor the session branch: %q", msg)
	}
}

// The merge/check session preamble rejects (never a gitErr, never ok) a
// session worktree with another branch checked out, before any gate, so the
// attestation is never touched.
func TestResolveSessionRejectsAForeignCheckedOutBranch(t *testing.T) {
	_, wt := sessionWorktreeWithForeignBranch(t, "")

	gs, failMsg, ok, gitErr := resolveSession(wt)
	if ok {
		t.Fatalf("expected reject, got ok (gs=%+v, gitErr=%v)", gs, gitErr)
	}
	if gitErr != nil {
		t.Errorf("reject carried a gitErr: %v", gitErr)
	}
	assertNamesBothBranches(t, failMsg)
}

// nothing-but-the-truth refuses to record an attestation for a session
// worktree with another branch checked out.
func TestNothingButTheTruthRefusesAForeignCheckedOutBranch(t *testing.T) {
	const skills = "[[pre-merge-skills]]\nname = \"review\"\nrationale = \"Mandatory.\"\n"
	sessionWorktreeWithForeignBranch(t, skills)

	res, err := handleNothingButTheTruth(context.Background(),
		json.RawMessage(`{"skills":[{"name":"review","used":true,"reasoning":"looked"}]}`), nil)
	if err != nil {
		t.Fatalf("handleNothingButTheTruth: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("attestation recorded with feature--probe checked out: %s", res.Text)
	}
	assertNamesBothBranches(t, res.Text)
}
