package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run executes git in dir and returns combined output; it does NOT fail the
// test on a non-zero exit (a conflicting merge exits non-zero by design).
func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := run(t, dir, args...); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestUnmergedPaths covers the conflict-detection primitive the #200 merge guard
// relies on: clean → nil, a merge conflict → the conflicted path, resolved → nil.
func TestUnmergedPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "t@t")
	mustRun(t, repo, "config", "user.name", "t")

	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Clean repo → no unmerged paths.
	write("c.txt", "base\n")
	mustRun(t, repo, "add", "c.txt")
	mustRun(t, repo, "commit", "-m", "base")
	if got, err := UnmergedPaths(repo); err != nil || len(got) != 0 {
		t.Fatalf("clean repo: got %v, err %v; want nil", got, err)
	}

	// Two branches edit the same line → merge conflict.
	mustRun(t, repo, "checkout", "-b", "feature")
	write("c.txt", "feature\n")
	mustRun(t, repo, "commit", "-am", "feature edit")
	mustRun(t, repo, "checkout", "main")
	write("c.txt", "main\n")
	mustRun(t, repo, "commit", "-am", "main edit")
	if _, err := run(t, repo, "merge", "feature"); err == nil {
		t.Fatal("expected a merge conflict")
	}

	got, err := UnmergedPaths(repo)
	if err != nil {
		t.Fatalf("UnmergedPaths during conflict: %v", err)
	}
	if len(got) != 1 || got[0] != "c.txt" {
		t.Fatalf("UnmergedPaths = %v, want [c.txt]", got)
	}

	// Resolve → no unmerged paths.
	mustRun(t, repo, "checkout", "--theirs", "c.txt")
	mustRun(t, repo, "add", "c.txt")
	if got, err := UnmergedPaths(repo); err != nil || len(got) != 0 {
		t.Fatalf("after resolve: got %v, err %v; want nil", got, err)
	}
}

// newRepo returns a fresh repo on `main` with one commit, isolated from the
// host's git config.
func newRepo(t *testing.T) (root, repo string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	repo = filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "t@t")
	mustRun(t, repo, "config", "user.name", "t")
	mustRun(t, repo, "commit", "--allow-empty", "-m", "init")
	return root, repo
}

// TestBranchWorktree pins the porcelain parse, and with it the guard that keeps
// spinclass's own transient worktrees from being mistaken for a holder of the
// branch they were cut from. The merge build worktree (.merge-*) and the
// landing worktree (.land-*) are both created via WorktreeAddDetached, so their
// records carry no `branch` line. Were that to stop holding, the stale-base
// fast-forward would try to advance a detached build worktree mid-merge instead
// of the real default branch.
func TestBranchWorktree(t *testing.T) {
	root, repo := newRepo(t)

	feature := filepath.Join(root, "feature-wt")
	mustRun(t, repo, "worktree", "add", "-b", "feature", feature)
	detached := filepath.Join(root, "detached-wt")
	mustRun(t, repo, "worktree", "add", "--detach", detached, "HEAD")

	// git reports its own canonicalization of a path, which need not match the
	// string we built (a symlinked TMPDIR is the usual culprit), so compare
	// resolved forms rather than raw ones.
	resolve := func(p string) string {
		t.Helper()
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatalf("resolve %q: %v", p, err)
		}
		return r
	}
	wantDetached := resolve(detached)

	for _, tc := range []struct{ branch, want string }{
		{"main", resolve(repo)},
		{"feature", resolve(feature)},
		{"nonexistent", ""},
	} {
		got, err := BranchWorktree(repo, tc.branch)
		if err != nil {
			t.Fatalf("BranchWorktree(%s): %v", tc.branch, err)
		}
		if got != "" {
			got = resolve(got)
		}
		if got != tc.want {
			t.Errorf("BranchWorktree(%s) = %q, want %q", tc.branch, got, tc.want)
		}
		if got != "" && got == wantDetached {
			t.Errorf("BranchWorktree(%s) returned the DETACHED worktree; a record "+
				"with no `branch` line must never match", tc.branch)
		}
	}
}

// TestRemotes covers the first gate every freshness check passes through.
// Getting it wrong in the "none configured" direction would make every
// remote-less fixture repo attempt a fetch.
func TestRemotes(t *testing.T) {
	_, repo := newRepo(t)

	if got := Remotes(repo); got != nil {
		t.Errorf("Remotes on a repo with no remote = %v, want nil", got)
	}

	mustRun(t, repo, "remote", "add", "origin", "/nonexistent/upstream.git")
	got := Remotes(repo)
	if len(got) != 1 || got[0] != "origin" {
		t.Errorf("Remotes = %v, want [origin]", got)
	}
}
