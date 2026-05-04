package check

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/embeds"
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
	blobURIs, err := Run(&buf, "tap", wtPath, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(blobURIs) != 0 {
		t.Errorf("expected no blob URIs without madder pinned, got %v", blobURIs)
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
	_, err := Run(&buf, "tap", wtPath, false)
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

// withFakeMadder installs a fake `madder write -format json -` for the
// duration of the test. The fake reads stdin to EOF and emits a known
// JSON envelope; the bytes written to stdin are captured to a file so
// the test can assert on what spinclass piped through.
func withFakeMadder(t *testing.T) (madderBin, stdinCapture string) {
	t.Helper()
	dir := t.TempDir()
	madderBin = filepath.Join(dir, "fake-madder")
	stdinCapture = filepath.Join(dir, "stdin")
	script := `#!/bin/sh
case "$1" in
  init)
    mkdir -p "$PWD/.madder/local/share/blob_stores/default"
    touch "$PWD/.madder/local/share/blob_stores/default/blob_store-config"
    ;;
  write)
    cat >"` + stdinCapture + `"
    printf '{"id":"sha256-fake","size":0,"source":"-"}\n'
    ;;
esac
exit 0
`
	if err := os.WriteFile(madderBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prevMadder, prevDirenv := embeds.MadderBin(), embeds.DirenvBin()
	embeds.Set(madderBin, prevDirenv)
	t.Cleanup(func() { embeds.Set(prevMadder, prevDirenv) })
	return madderBin, stdinCapture
}

func TestRunHookCompactShape(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-compact")
	_, stdinCapture := withFakeMadder(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo line-one; echo line-two\"\n")

	var buf bytes.Buffer
	blobURIs, err := Run(&buf, "tap", wtPath, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(blobURIs) != 1 || blobURIs[0] != "madder://.default/sha256-fake" {
		t.Errorf("expected blob URIs == [madder://.default/sha256-fake], got %v", blobURIs)
	}

	got := buf.String()

	if !strings.Contains(got, "# directive: if status is ok") {
		t.Errorf("expected directive comment, got:\n%s", got)
	}
	for _, want := range []string{
		"command: echo line-one; echo line-two",
		"resource_link: madder://.default/sha256-fake",
		"exit_code: 0",
		"elapsed:",
		"tail:",
		"line-one",
		"line-two",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got:\n%s", want, got)
		}
	}
	// Compact shape never opens an OutputBlock — no nested `# Subtest`
	// or indented diagnostic block separator from OutputBlock.
	if strings.Contains(got, "# Output:") {
		t.Errorf("did not expect OutputBlock '# Output:' line, got:\n%s", got)
	}

	stdinBytes, err := os.ReadFile(stdinCapture)
	if err != nil {
		t.Fatalf("reading madder stdin capture: %v", err)
	}
	if !strings.Contains(string(stdinBytes), "line-one") || !strings.Contains(string(stdinBytes), "line-two") {
		t.Errorf("expected hook output piped to madder stdin, got: %q", stdinBytes)
	}
}

func TestRunHookCompactShape_Failure(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-compact-fail")
	withFakeMadder(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo about-to-fail; exit 7\"\n")

	var buf bytes.Buffer
	blobURIs, err := Run(&buf, "tap", wtPath, false)
	if err == nil {
		t.Fatal("expected hook failure")
	}
	if len(blobURIs) != 1 || blobURIs[0] != "madder://.default/sha256-fake" {
		t.Errorf("expected blob URIs even on failure, got %v", blobURIs)
	}

	got := buf.String()
	if !strings.Contains(got, "not ok") {
		t.Errorf("expected 'not ok' for failed hook, got:\n%s", got)
	}
	if !strings.Contains(got, "exit_code: 7") {
		t.Errorf("expected exit_code: 7, got:\n%s", got)
	}
	if !strings.Contains(got, "about-to-fail") {
		t.Errorf("expected hook stdout in tail, got:\n%s", got)
	}
	if !strings.Contains(got, "resource_link: madder://.default/sha256-fake") {
		t.Errorf("expected resource_link in failure response, got:\n%s", got)
	}
}

func TestRunNoHookConfigured(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-no-hook")
	// No sweatfile written.

	var buf bytes.Buffer
	if _, err := Run(&buf, "tap", wtPath, false); err != nil {
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
