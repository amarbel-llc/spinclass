package resurrect

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/close"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// mustCommitFile adds and commits a new file in dir, mirroring
// internal/close's helper of the same name (unexported, so duplicated here
// rather than shared across package boundaries).
func mustCommitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", dir, "add", name},
		{"-C", dir, "commit", "-m", msg},
	} {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// setupClosedSession creates a real repo+worktree, writes a live session
// with a marker commit, closes it via close.RunResolved (the real
// production path that now captures DeletedSHA), and returns the resolved
// paths for the caller to resurrect.
func setupClosedSession(t *testing.T, branch string) (repoPath, wtPath string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	repoPath = filepath.Join(root, "repo")
	wtPath = filepath.Join(repoPath, ".worktrees", branch)
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, branch)
	mustCommitFile(t, wtPath, "marker.txt", "hello from before the close", "add marker")

	// PID intentionally left 0 (unset): RequestClose in close.RunResolved
	// sends SIGHUP to a live State.PID, and os.Getpid() would signal this
	// very test process. session.IsAlive(0) is always false, so RunResolved
	// treats this as "no active process to close" — the correct fixture
	// shape for a session that was never actually attached.
	s := session.State{
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       branch,
		SessionKey:   "repo/" + branch,
		Description:  "test session",
		SpawnedBy:    "driver/other",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}
	if err := session.Write(s); err != nil {
		t.Fatal(err)
	}

	// force=true: the marker commit puts the branch ahead of the default
	// branch (unintegrated), which would otherwise block a non-TTY close.
	if err := close.RunResolved(io.Discard, repoPath, wtPath, branch, true, nil, "tap"); err != nil {
		t.Fatalf("close.RunResolved: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("setup: worktree still present after close, stat err = %v", err)
	}
	return repoPath, wtPath
}

// TestRunRecreatesWorktreeFromClosedSession is the happy path end-to-end:
// close (real production path, capturing DeletedSHA) → Run recreates the
// worktree with the exact prior content, and writes fresh inactive session
// state that preserves the description/spawned_by lineage.
func TestRunRecreatesWorktreeFromClosedSession(t *testing.T) {
	repoPath, wtPath := setupClosedSession(t, "feature-x")

	var buf bytes.Buffer
	if err := Run(&buf, "repo/feature-x", "", "tap"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(buf.String(), "ok") {
		t.Errorf("expected TAP ok output, got %q", buf.String())
	}

	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree not recreated: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(wtPath, "marker.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello from before the close" {
		t.Errorf("marker.txt content = %q, want the pre-close content", content)
	}

	got, err := session.Read(repoPath, "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionState != session.StateInactive {
		t.Errorf("resurrected SessionState = %q, want %q", got.SessionState, session.StateInactive)
	}
	if got.Description != "test session" {
		t.Errorf("Description = %q, want preserved from tombstone", got.Description)
	}
	if got.SpawnedBy != "driver/other" {
		t.Errorf("SpawnedBy = %q, want preserved from tombstone", got.SpawnedBy)
	}
	if got.IsTombstone() {
		t.Error("resurrected session must not still read as a tombstone")
	}
}

// TestRunWithNewBranchName verifies --new-branch recreates under a
// different name instead of the original.
func TestRunWithNewBranchName(t *testing.T) {
	repoPath, _ := setupClosedSession(t, "feature-y")

	if err := Run(io.Discard, "repo/feature-y", "feature-y-reborn", "tap"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	newPath := filepath.Join(repoPath, ".worktrees", "feature-y-reborn")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("worktree not created at renamed path: %v", err)
	}
	if _, err := session.Read(repoPath, "feature-y-reborn"); err != nil {
		t.Fatalf("session state not found for renamed branch: %v", err)
	}
}

// TestRunRefusesLiveSession: a target that is still an active (never
// closed) session must be refused, not silently recreated.
func TestRunRefusesLiveSession(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	repoPath := filepath.Join(root, "repo")
	wtPath := filepath.Join(repoPath, ".worktrees", "still-alive")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "still-alive")

	if err := session.Write(session.State{
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       "still-alive",
		SessionKey:   "repo/still-alive",
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	err := Run(io.Discard, "repo/still-alive", "", "tap")
	if err == nil {
		t.Fatal("expected error resurrecting a live (non-tombstoned) session")
	}
	if !strings.Contains(err.Error(), "not a closed session") {
		t.Errorf("error = %q, want mention of 'not a closed session'", err)
	}
}

// TestRunRefusesMissingDeletedSHA covers a tombstone that predates this
// feature (or was closed outside spinclass): DeletedSHA is empty, and Run
// must refuse with an actionable git-reflog pointer rather than attempting
// anything.
func TestRunRefusesMissingDeletedSHA(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	repoPath := filepath.Join(root, "repo")
	wtPath := filepath.Join(repoPath, ".worktrees", "legacy-close")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "legacy-close")

	if err := session.Write(session.State{
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       "legacy-close",
		SessionKey:   "repo/legacy-close",
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// Tombstone directly with sha="" — simulates a tombstone written before
	// DeletedSHA existed, without going through a real close.RunResolved.
	if err := session.Tombstone(repoPath, "legacy-close", ""); err != nil {
		t.Fatal(err)
	}

	err := Run(io.Discard, "repo/legacy-close", "", "tap")
	if err == nil {
		t.Fatal("expected error resurrecting a tombstone with no captured commit")
	}
	if !strings.Contains(err.Error(), "git reflog") {
		t.Errorf("error = %q, want a git-reflog pointer", err)
	}
}

// TestRunRefusesUnreachableCommit covers a captured SHA that no longer
// resolves in the repo's object database (e.g. locally garbage collected):
// Run must fail with a clear message instead of a raw git error from
// worktree.Create.
func TestRunRefusesUnreachableCommit(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	repoPath := filepath.Join(root, "repo")
	wtPath := filepath.Join(repoPath, ".worktrees", "pruned")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "pruned")

	if err := session.Write(session.State{
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       "pruned",
		SessionKey:   "repo/pruned",
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// A well-formed but nonexistent sha — never present in this fresh repo.
	if err := session.Tombstone(repoPath, "pruned", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}

	err := Run(io.Discard, "repo/pruned", "", "tap")
	if err == nil {
		t.Fatal("expected error resurrecting from an unreachable commit")
	}
	if !strings.Contains(err.Error(), "garbage collected") {
		t.Errorf("error = %q, want mention of garbage collection", err)
	}
}
