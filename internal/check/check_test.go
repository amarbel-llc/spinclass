package check

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupRepoWithWorktree creates an isolated git repo + worktree under
// t.TempDir() and returns (root, repoDir, wtPath). $HOME and other
// git-config env vars are scoped to root for the duration of the test.
func setupRepoWithWorktree(t *testing.T, branch string) (root, repoDir, wtPath string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)

	repoDir = filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "initial")

	wtDir := filepath.Join(repoDir, ".worktrees")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wtPath = filepath.Join(wtDir, branch)
	runGit(t, repoDir, "worktree", "add", "-b", branch, wtPath)

	return root, repoDir, wtPath
}

func writeSweatfile(t *testing.T, wtPath, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wtPath, "sweatfile"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}
}

func TestRunHookSuccessTAP(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-success")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"true\"\n")

	var buf bytes.Buffer
	if err := Run(&buf, "tap", wtPath, false); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "ok") {
		t.Errorf("expected TAP 'ok' in output, got: %q", got)
	}
	if strings.Contains(got, "not ok") {
		t.Errorf("did not expect 'not ok' in output, got: %q", got)
	}
	if !strings.Contains(got, "pre-merge hook") {
		t.Errorf("expected 'pre-merge hook' description in output, got: %q", got)
	}
	if !strings.Contains(got, "1..") {
		t.Errorf("expected TAP plan in output, got: %q", got)
	}
}

func TestRunHookFailureTAP(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-failure")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"false\"\n")

	var buf bytes.Buffer
	err := Run(&buf, "tap", wtPath, false)
	if err == nil {
		t.Fatalf("expected error when hook fails, got nil. Output: %s", buf.String())
	}

	got := buf.String()
	if !strings.Contains(got, "not ok") {
		t.Errorf("expected TAP 'not ok' in output, got: %q", got)
	}
	if !strings.Contains(got, "1..") {
		t.Errorf("expected TAP plan in output (so client can detect failure), got: %q", got)
	}
}

func TestRunNoHookConfigured(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-no-hook")
	// No sweatfile written.

	var buf bytes.Buffer
	if err := Run(&buf, "tap", wtPath, false); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "ok") {
		t.Errorf("expected TAP 'ok' for no-hook case, got: %q", got)
	}
	// Per the design: agents should treat "no hook" as a successful check
	// because there is nothing to run. The TAP message should make that
	// reason explicit so a human reading the output is not confused.
	if !strings.Contains(strings.ToLower(got), "no pre-merge hook") {
		t.Errorf("expected 'no pre-merge hook' message, got: %q", got)
	}
	if !strings.Contains(got, "1..") {
		t.Errorf("expected TAP plan in output, got: %q", got)
	}
}
