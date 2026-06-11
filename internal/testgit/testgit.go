// Package testgit provides shared test helpers for spawning real git
// repos and worktrees inside Go tests. Kept minimal: callers that need
// extra git ops should call exec.Command("git", ...) directly.
package testgit

import (
	"os"
	"os/exec"
	"testing"
)

// SetHermeticEnv isolates every git invocation in the test process from the
// host's git configuration. The global config is pointed at a generated
// minimal file (supplying the identity fixture commits need) and the system
// config at the null device, so host settings — commit.gpgsign and its
// signing-agent dependency, hook paths, templates, init.defaultBranch — can
// never reach a fixture repo OR a production code path under test (e.g. a
// rebase re-signing commits). This matches the nix sandbox, where no host
// config exists and the suite is hermetic by construction.
//
// Call from TestMain before m.Run and run the returned cleanup after.
// Process-wide by design: per-test t.Setenv would miss nothing today but
// forces sequential tests; TestMain covers parallel subtests too.
func SetHermeticEnv() (cleanup func(), err error) {
	f, err := os.CreateTemp("", "testgit-gitconfig-")
	if err != nil {
		return nil, err
	}
	config := "[user]\n\tname = spinclass-test\n\temail = test@spinclass.invalid\n[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n"
	if _, err := f.WriteString(config); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return nil, err
	}
	os.Setenv("GIT_CONFIG_GLOBAL", f.Name())
	os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	return func() { _ = os.Remove(f.Name()) }, nil
}

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
