package clean

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/nixgc"
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
)

// deadPID is above the OS PID_MAX on all supported platforms (math.MaxInt32-1),
// so session.IsAlive reliably reports it dead. Used to simulate a crashed
// merge's orphaned build worktree.
const deadPID = 2147483646

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

// setupRepo creates an isolated git repo (a plain main checkout) under
// t.TempDir() with a .worktrees/ dir present so worktree.ScanRepos discovers
// it. $HOME and git-config env vars are scoped to root for the test so
// clean.Run's tombstone/chat retention lookups stay isolated.
func setupRepo(t *testing.T) (root, repoDir string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

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

	if err := os.MkdirAll(filepath.Join(repoDir, ".worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}

	return root, repoDir
}

// makeBuildWorktreeDir creates a non-empty .merge-* build worktree dir under
// <repo>/.worktrees/ (mirroring the #129 test's non-empty fixture so a stray
// git worktree remove can't silently succeed) and returns its path.
func makeBuildWorktreeDir(t *testing.T, repoDir, name string) string {
	t.Helper()
	p := filepath.Join(repoDir, ".worktrees", name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir build worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p, "leftover.txt"), []byte("interrupted"), 0o644); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	return p
}

func TestPidFromBuildWorktreeName(t *testing.T) {
	cases := []struct {
		name    string
		wantPid int
		wantOk  bool
	}{
		{".merge-feature-abc123-12345", 12345, true},
		{".merge-feat-ure-deadbeef-678", 678, true},
		{".merge-foo", 0, false},
		{".merge-foo-", 0, false},
		{".merge-foo-notanumber", 0, false},
	}
	for _, c := range cases {
		pid, ok := pidFromBuildWorktreeName(c.name)
		if pid != c.wantPid || ok != c.wantOk {
			t.Errorf("pidFromBuildWorktreeName(%q) = (%d,%v); want (%d,%v)",
				c.name, pid, ok, c.wantPid, c.wantOk)
		}
	}
}

func TestCleanPrunesOrphanedDeadPidBuildWorktree(t *testing.T) {
	_, repoDir := setupRepo(t)
	orphan := makeBuildWorktreeDir(t, repoDir, ".merge-feature-abc123-"+itoa(deadPID))

	out := captureRun(t, repoDir, false, false, true, "tap")

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("expected orphan build worktree removed, still present at %q (err=%v)", orphan, err)
	}
	if !strings.Contains(out, "orphaned build worktree") {
		t.Errorf("expected TAP output to mention pruning orphaned build worktree, got:\n%s", out)
	}
}

func TestCleanKeepsLivePidBuildWorktree(t *testing.T) {
	_, repoDir := setupRepo(t)
	live := makeBuildWorktreeDir(t, repoDir, ".merge-feature-abc123-"+itoa(os.Getpid()))

	_ = captureRun(t, repoDir, false, false, true, "tap")

	if _, err := os.Stat(live); err != nil {
		t.Errorf("expected live-pid build worktree kept, but it is gone: %v", err)
	}
}

func TestCleanKeepsUnparseableBuildWorktree(t *testing.T) {
	// A .merge-* dir whose name has no parseable trailing PID must be left
	// alone (we don't guess — see findOrphanBuildWorktrees).
	_, repoDir := setupRepo(t)
	unparseable := makeBuildWorktreeDir(t, repoDir, ".merge-no-pid-here")

	_ = captureRun(t, repoDir, false, false, true, "tap")

	if _, err := os.Stat(unparseable); err != nil {
		t.Errorf("expected unparseable-name build worktree kept, but it is gone: %v", err)
	}
}

func TestCleanDryRunKeepsOrphan(t *testing.T) {
	_, repoDir := setupRepo(t)
	orphan := makeBuildWorktreeDir(t, repoDir, ".merge-feature-abc123-"+itoa(deadPID))

	out := captureRun(t, repoDir, false, true, false, "tap")

	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("expected orphan kept on dry-run, but it is gone: %v", err)
	}
	if !strings.Contains(out, "orphaned build worktree") {
		t.Errorf("expected dry-run TAP to plan an orphaned build worktree prune, got:\n%s", out)
	}
}

// captureRun runs clean.Run with os.Stdout redirected to a pipe and returns the
// captured TAP output.
func captureRun(t *testing.T, startDir string, interactive, dryRun, yes bool, format string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := Run(startDir, interactive, dryRun, yes, format)
	os.Stdout = orig
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if runErr != nil {
		t.Fatalf("clean.Run: %v", runErr)
	}
	return buf.String()
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func TestParsePorcelainEmpty(t *testing.T) {
	changes := ParsePorcelain("")
	if len(changes) != 0 {
		t.Errorf("expected 0 changes, got %d", len(changes))
	}
}

func TestParsePorcelainModified(t *testing.T) {
	changes := ParsePorcelain(" M file.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Code != " M" {
		t.Errorf("expected code ' M', got %q", changes[0].Code)
	}
	if changes[0].Path != "file.go" {
		t.Errorf("expected path 'file.go', got %q", changes[0].Path)
	}
	if changes[0].Description() != "modified" {
		t.Errorf("expected description 'modified', got %q", changes[0].Description())
	}
}

func TestParsePorcelainUntracked(t *testing.T) {
	changes := ParsePorcelain("?? newfile.txt")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Code != "??" {
		t.Errorf("expected code '??', got %q", changes[0].Code)
	}
	if changes[0].Description() != "untracked" {
		t.Errorf("expected description 'untracked', got %q", changes[0].Description())
	}
}

func TestParsePorcelainDeleted(t *testing.T) {
	changes := ParsePorcelain(" D removed.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Description() != "deleted" {
		t.Errorf("expected description 'deleted', got %q", changes[0].Description())
	}
}

func TestParsePorcelainAdded(t *testing.T) {
	changes := ParsePorcelain("A  staged.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Description() != "added" {
		t.Errorf("expected description 'added', got %q", changes[0].Description())
	}
}

func TestParsePorcelainRenamed(t *testing.T) {
	changes := ParsePorcelain("R  old.go -> new.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Code != "R " {
		t.Errorf("expected code 'R ', got %q", changes[0].Code)
	}
	if changes[0].Path != "new.go" {
		t.Errorf("expected path 'new.go', got %q", changes[0].Path)
	}
	if changes[0].Description() != "renamed" {
		t.Errorf("expected description 'renamed', got %q", changes[0].Description())
	}
}

func TestParsePorcelainMultiple(t *testing.T) {
	input := " M file1.go\n?? file2.txt\nA  file3.go\n D file4.go"
	changes := ParsePorcelain(input)
	if len(changes) != 4 {
		t.Fatalf("expected 4 changes, got %d", len(changes))
	}

	expected := []struct {
		code string
		path string
		desc string
	}{
		{" M", "file1.go", "modified"},
		{"??", "file2.txt", "untracked"},
		{"A ", "file3.go", "added"},
		{" D", "file4.go", "deleted"},
	}

	for i, exp := range expected {
		if changes[i].Code != exp.code {
			t.Errorf("change %d: expected code %q, got %q", i, exp.code, changes[i].Code)
		}
		if changes[i].Path != exp.path {
			t.Errorf("change %d: expected path %q, got %q", i, exp.path, changes[i].Path)
		}
		if changes[i].Description() != exp.desc {
			t.Errorf("change %d: expected desc %q, got %q", i, exp.desc, changes[i].Description())
		}
	}
}

func TestParsePorcelainStagedModified(t *testing.T) {
	changes := ParsePorcelain("MM both.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Code != "MM" {
		t.Errorf("expected code 'MM', got %q", changes[0].Code)
	}
	if changes[0].Description() != "modified" {
		t.Errorf("expected description 'modified', got %q", changes[0].Description())
	}
}

func TestParsePorcelainStagedDeleted(t *testing.T) {
	changes := ParsePorcelain("D  gone.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Description() != "deleted" {
		t.Errorf("expected description 'deleted', got %q", changes[0].Description())
	}
}

// TestRunReapEmptyClosureEmitsOk is the regression guard for #76:
// nixgc.Reap with an empty Closure is a no-op success, and the
// surrounding OutputBlock must render "ok" — not "not ok". Pre-fix,
// the success path returned a non-nil *yaml_diagnostic.YAMLDiagnostic carrying only
// summary extras, which tap-go interprets as "not ok". Mirrors the
// twin test in internal/close.
func TestRunReapEmptyClosureEmitsOk(t *testing.T) {
	var buf bytes.Buffer
	tw := tap.NewWriter(&buf)
	plan := nixgc.Plan{WorktreePath: "/tmp/empty-wt"}
	runReap(tw, plan, "brave-myrtle")
	tw.Plan()

	out := buf.String()
	if strings.Contains(out, "not ok") {
		t.Errorf("TAP output unexpectedly contains 'not ok' for empty-closure reap:\n%s", out)
	}
	if !strings.Contains(out, "ok 1 - nix-gc reap brave-myrtle") {
		t.Errorf("TAP output missing 'ok 1 - nix-gc reap brave-myrtle':\n%s", out)
	}
	for _, want := range []string{"reclaimed: 0", "bytes_freed: 0", "closure: 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("TAP output missing summary line %q:\n%s", want, out)
		}
	}
}
