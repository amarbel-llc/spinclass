// Package testgit provides shared test helpers for spawning real git
// repos and worktrees inside Go tests. Kept minimal: callers that need
// extra git ops should call exec.Command("git", ...) directly.
package testgit

import (
	"os/exec"
	"testing"
)

// RequireGit skips the test when git isn't on PATH. The nix-build
// sandbox runs `go test` without git, and integration tests like these
// must skip rather than fail in that environment.
func RequireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
}

// MustInit initializes a fresh git repo at dir with a single empty
// commit on `main`. Sets a deterministic identity and disables GPG
// signing so tests don't depend on the host's git config.
func MustInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "config", "user.email", "test@test"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "config", "commit.gpgsign", "false"},
		{"-C", dir, "commit", "--allow-empty", "-m", "init"},
	} {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// MustWorktreeAdd creates a new branch and worktree at wtPath off the
// repo at repoPath.
func MustWorktreeAdd(t *testing.T, repoPath, wtPath, branch string) {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "worktree", "add", "-b", branch, wtPath).CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree add %s: %v\n%s", wtPath, err, out)
	}
}
