package close

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/testgit"
)

// TestResolveTargetByIDFindsSession is the happy path: a tracked
// session for a repo can be resolved by its worktree dirname even when
// resolveTarget is called from outside that worktree (cwd is the main
// repo).
func TestResolveTargetByIDFindsSession(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoPath := filepath.Join(root, "repo")
	wtPath := filepath.Join(repoPath, ".worktrees", "feature-x")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "feature-x")

	s := session.State{
		PID:          12345,
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       "feature-x",
		SessionKey:   "repo/feature-x",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := session.Write(s); err != nil {
		t.Fatal(err)
	}

	gotRepo, gotWT, gotBranch, err := resolveTarget(repoPath, "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if gotRepo != repoPath {
		t.Errorf("repo = %q, want %q", gotRepo, repoPath)
	}
	if gotWT != wtPath {
		t.Errorf("wt = %q, want %q", gotWT, wtPath)
	}
	if gotBranch != "feature-x" {
		t.Errorf("branch = %q, want %q", gotBranch, "feature-x")
	}
}

// TestResolveTargetByIDOrphanedWorktreeRejected covers the new contract:
// a git worktree without a spinclass state file is not a valid close
// target. The error must mention `git worktree remove` so users know
// the escape hatch.
func TestResolveTargetByIDOrphanedWorktreeRejected(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoPath := filepath.Join(root, "repo")
	wtPath := filepath.Join(repoPath, ".worktrees", "orphan")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "orphan")

	_, _, _, err := resolveTarget(repoPath, "orphan")
	if err == nil {
		t.Fatal("expected error for orphaned worktree")
	}
	if !strings.Contains(err.Error(), "git worktree remove") {
		t.Errorf("error = %q, want contains 'git worktree remove'", err.Error())
	}
}
