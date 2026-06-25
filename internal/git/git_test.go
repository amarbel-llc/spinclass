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
