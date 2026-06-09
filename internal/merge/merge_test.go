package merge

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/session"
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
)

type mockExecutor struct {
	detachCalled bool
}

func (m *mockExecutor) Attach(dir string, key string, command []string, dryRun bool, tp *tap.TestPoint) error {
	return nil
}

func (m *mockExecutor) Detach() error {
	m.detachCalled = true
	return nil
}

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

func setupRepo(t *testing.T) (repoDir string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	// Isolate git config to prevent interference from global settings
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

	return repoDir
}

// setupWorktree creates repoDir/.worktrees/<branch> via `git worktree add -b`.
// Returns the absolute worktree path.
func setupWorktree(t *testing.T, repoDir, branch string) string {
	t.Helper()
	wtDir := filepath.Join(repoDir, ".worktrees")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(wtDir, branch)
	runGit(t, repoDir, "worktree", "add", "-b", branch, wtPath)
	return wtPath
}

// TestPrepareMergePinsShaAcrossConcurrentCommit verifies the core #106
// guarantee: PrepareMerge pins the post-rebase sha, and FinishMerge merges
// exactly that sha even when a new commit lands on the branch afterward
// (simulating concurrent agent work while the pre-merge hook runs in an
// isolated build worktree). main ends at the pinned sha; the branch keeps its
// later commit, strictly ahead of main by one.
func TestPrepareMergePinsShaAcrossConcurrentCommit(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-pin")

	// One commit ahead of main on the branch.
	if err := os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "a.txt")
	runGit(t, wtPath, "commit", "-m", "feature commit A")

	// No sweatfile → no pre-merge hook → FinishMerge skips the hook entirely.
	var buf bytes.Buffer
	tw := NewMergeWriter(&buf)
	pinnedSha, err := PrepareMerge(tw, &buf, repoDir, wtPath, "feature-pin", "main", false, false)
	if err != nil {
		t.Fatalf("PrepareMerge: %v\n%s", err, buf.String())
	}

	// Simulate concurrent work: a SECOND commit lands on the branch after pin.
	if err := os.WriteFile(filepath.Join(wtPath, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "b.txt")
	runGit(t, wtPath, "commit", "-m", "concurrent commit B")
	if branchTip := runGit(t, wtPath, "rev-parse", "HEAD"); branchTip == pinnedSha {
		t.Fatal("test setup: expected branch tip to advance past the pinned sha")
	}

	// FinishMerge in-session (inSession=true keeps the worktree).
	if _, err := FinishMerge(context.Background(), &mockExecutor{}, tw, &buf,
		repoDir, wtPath, "feature-pin", "main", pinnedSha, false, true, false, nil); err != nil {
		t.Fatalf("FinishMerge: %v\n%s", err, buf.String())
	}

	// main must be at the pinned sha, NOT the concurrent commit B.
	if mainTip := runGit(t, repoDir, "rev-parse", "main"); mainTip != pinnedSha {
		t.Errorf("main = %s, want pinned %s (concurrent commit leaked into the merge)", mainTip, pinnedSha)
	}
	// The branch keeps commit B, strictly ahead of main by one.
	if ahead := git.CommitsAhead(wtPath, "main", "feature-pin"); ahead != 1 {
		t.Errorf("branch ahead of main = %d, want 1 (concurrent commit preserved)", ahead)
	}
}

// TestResolveTargetSessionKeyCrossRepo: explicit merge targets resolve
// current-repo git worktrees first (bare worktrees without session
// state keep working, and a local dirname is never shadowed by another
// repo's session); session targets — including the `<repo>/<branch>`
// keys `sc list` prints — then resolve cross-repo from any cwd.
func TestResolveTargetSessionKeyCrossRepo(t *testing.T) {
	repoA := setupRepo(t) // sets GIT_CEILING_DIRECTORIES + isolated HOME
	root := filepath.Dir(repoA)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdg-state"))

	// repoA: bare git worktree, NO session state.
	wtA := setupWorktree(t, repoA, "feature-x")

	// repoB: same branch/dirname, WITH session state.
	repoB := filepath.Join(root, "other")
	if err := os.MkdirAll(repoB, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoB, "init", "-b", "main")
	runGit(t, repoB, "config", "user.email", "test@test.com")
	runGit(t, repoB, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoB, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoB, "add", "file.txt")
	runGit(t, repoB, "commit", "-m", "initial")
	wtB := setupWorktree(t, repoB, "feature-x")
	if err := session.Write(session.State{
		SessionState: session.StateInactive,
		RepoPath:     repoB,
		WorktreePath: wtB,
		Branch:       "feature-x",
		SessionKey:   "other/feature-x",
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Bare dirname from inside repoA: the local git worktree wins, even
	// though repoB has a session with the same dirname.
	gotRepo, gotWT, gotBranch, err := resolveTarget(repoA, "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if gotRepo != repoA || gotWT != wtA || gotBranch != "feature-x" {
		t.Errorf("local-first: got (%q, %q, %q), want repoA's worktree", gotRepo, gotWT, gotBranch)
	}

	// Session key from inside repoA: resolves cross-repo to repoB.
	gotRepo, gotWT, gotBranch, err = resolveTarget(repoA, "other/feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if gotRepo != repoB || gotWT != wtB || gotBranch != "feature-x" {
		t.Errorf("session key: got (%q, %q, %q), want repoB's worktree", gotRepo, gotWT, gotBranch)
	}

	// From outside any repo, an explicit session target still resolves.
	outside := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	gotRepo, _, _, err = resolveTarget(outside, "other/feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if gotRepo != repoB {
		t.Errorf("outside repo: got repo %q, want %q", gotRepo, repoB)
	}

	// A target matching nothing keeps the worktree-not-found error.
	if _, _, _, err = resolveTarget(repoA, "no-such-thing"); err == nil {
		t.Error("missing target: expected error, got nil")
	}
}

func TestResolvedMergesAndRemovesWorktree(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-merge")

	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "new.txt")
	runGit(t, wtPath, "commit", "-m", "add new file")

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-merge", "main", false, false, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}

	// Commit should be on main now
	mainLog := runGit(t, repoDir, "log", "--oneline")
	if !strings.Contains(mainLog, "add new file") {
		t.Errorf("expected commit on main, got: %s", mainLog)
	}

	// Worktree directory should be removed
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("expected worktree to be removed, but it still exists")
	}

	// Branch should be deleted
	branchCheck := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "refs/heads/feature-merge")
	if err := branchCheck.Run(); err == nil {
		t.Error("expected branch feature-merge to be deleted, but it still exists")
	}

	// TAP output should contain all three steps
	got := buf.String()
	if !strings.Contains(got, "ok") {
		t.Errorf("expected TAP ok lines, got: %q", got)
	}
}

func TestResolvedTapOutput(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-tap")

	if err := os.WriteFile(filepath.Join(wtPath, "tap.txt"), []byte("tap"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "tap.txt")
	runGit(t, wtPath, "commit", "-m", "tap commit")

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-tap", "main", false, false, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}

	got := buf.String()

	if !strings.Contains(got, "ok 1 - rebase feature-tap") {
		t.Errorf("expected rebase test point, got: %q", got)
	}
	if !strings.Contains(got, "ok 2 - merge feature-tap") {
		t.Errorf("expected merge test point, got: %q", got)
	}
	if !strings.Contains(got, "ok 3 - remove worktree feature-tap") {
		t.Errorf("expected remove worktree test point, got: %q", got)
	}
	if !strings.Contains(got, "ok 4 - delete branch feature-tap") {
		t.Errorf("expected delete branch test point, got: %q", got)
	}
	if !strings.Contains(got, "1..4") {
		t.Errorf("expected plan 1..4, got: %q", got)
	}
}

func TestResolvedGitSyncTapOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)

	// Create a bare remote repo
	bareDir := filepath.Join(root, "bare.git")
	runGit(t, root, "init", "--bare", "-b", "main", bareDir)

	// Clone it to get a repo with a remote
	repoDir := filepath.Join(root, "repo")
	runGit(t, root, "clone", bareDir, repoDir)
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "initial")
	runGit(t, repoDir, "push")

	wtPath := setupWorktree(t, repoDir, "feature-sync")
	if err := os.WriteFile(filepath.Join(wtPath, "sync.txt"), []byte("sync"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "sync.txt")
	runGit(t, wtPath, "commit", "-m", "sync commit")

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-sync", "main", true, false, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}

	got := buf.String()

	if !strings.Contains(got, "ok 1 - pull main") {
		t.Errorf("expected pull test point first (so rebase target is fresh), got: %q", got)
	}
	if !strings.Contains(got, "ok 2 - rebase feature-sync") {
		t.Errorf("expected rebase test point, got: %q", got)
	}
	if !strings.Contains(got, "ok 3 - merge feature-sync") {
		t.Errorf("expected merge test point, got: %q", got)
	}
	if !strings.Contains(got, "ok 4 - remove worktree feature-sync") {
		t.Errorf("expected remove worktree test point, got: %q", got)
	}
	if !strings.Contains(got, "ok 5 - delete branch feature-sync") {
		t.Errorf("expected delete branch test point, got: %q", got)
	}
	if !strings.Contains(got, "ok 6 - push") {
		t.Errorf("expected push test point, got: %q", got)
	}
	if !strings.Contains(got, "1..6") {
		t.Errorf("expected plan 1..6, got: %q", got)
	}
}

// TestResolvedGitSyncPullsBeforeRebase is the #29 regression test.
//
// Scenario: origin has moved since the session was started. The session
// branch is rebased onto local master, which is behind origin. Before
// this fix, `git merge --ff-only` in the final merge step fails because
// local master couldn't fast-forward to the post-rebase session branch
// tip. After this fix, the upfront pull brings local master up to the
// origin tip, the rebase targets the current ref, and the merge succeeds.
func TestResolvedGitSyncPullsBeforeRebase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)

	// Bare "origin"
	bareDir := filepath.Join(root, "bare.git")
	runGit(t, root, "init", "--bare", "-b", "main", bareDir)

	// First clone — where the "session" starts
	repoDir := filepath.Join(root, "repo")
	runGit(t, root, "clone", bareDir, repoDir)
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "initial")
	runGit(t, repoDir, "push")

	wtPath := setupWorktree(t, repoDir, "feature-stale")
	if err := os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "feature.txt")
	runGit(t, wtPath, "commit", "-m", "feature commit")

	// Simulate a concurrent commit landing on origin from a second clone.
	otherDir := filepath.Join(root, "other")
	runGit(t, root, "clone", bareDir, otherDir)
	runGit(t, otherDir, "config", "user.email", "other@test.com")
	runGit(t, otherDir, "config", "user.name", "Other")
	if err := os.WriteFile(filepath.Join(otherDir, "origin-new.txt"), []byte("origin-new"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, otherDir, "add", "origin-new.txt")
	runGit(t, otherDir, "commit", "-m", "concurrent commit on origin")
	runGit(t, otherDir, "push")

	// At this point repoDir's local main is behind origin/main by one commit.
	// Without the fix, Resolved() would rebase feature-stale onto local main
	// (stale) and then `git merge --ff-only` on main would fail.

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-stale", "main", true, false, false)
	if err != nil {
		t.Fatalf("Resolved() error (stale local master should not cause failure): %v\n\nTAP output:\n%s", err, buf.String())
	}

	got := buf.String()
	if !strings.Contains(got, "ok 1 - pull main") {
		t.Errorf("expected upfront pull as step 1, got: %q", got)
	}
	if !strings.Contains(got, "ok 3 - merge feature-stale") {
		t.Errorf("expected merge to succeed after upfront pull, got: %q", got)
	}

	// And the concurrent origin commit should be present in local main now.
	mainLog := runGit(t, repoDir, "log", "--oneline", "main")
	if !strings.Contains(mainLog, "concurrent commit on origin") {
		t.Errorf("expected concurrent origin commit on local main after pull, got log:\n%s", mainLog)
	}
	if !strings.Contains(mainLog, "feature commit") {
		t.Errorf("expected feature commit on local main after merge, got log:\n%s", mainLog)
	}
}

func TestResolvedRepoNotFound(t *testing.T) {
	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", "/nonexistent/path", "/nonexistent/wt", "feature", "main", false, false, false)
	if err == nil {
		t.Error("expected error for nonexistent repo, got nil")
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("expected 'repository not found' error, got: %v", err)
	}
}

func TestResolvedDivergedBranch(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-diverge")

	// Make a commit on the worktree
	if err := os.WriteFile(filepath.Join(wtPath, "diverge.txt"), []byte("diverge"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "diverge.txt")
	runGit(t, wtPath, "commit", "-m", "diverge commit")

	// Make a conflicting commit on main
	if err := os.WriteFile(filepath.Join(repoDir, "diverge.txt"), []byte("conflict"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "diverge.txt")
	runGit(t, repoDir, "commit", "-m", "conflicting commit on main")

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-diverge", "main", false, false, false)
	if err == nil {
		t.Error("expected error for conflicting rebase, got nil")
	}

	// Abort the rebase to clean up
	exec.Command("git", "-C", wtPath, "rebase", "--abort").Run()
}

func TestResolvedInSessionSkipsCleanup(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-insession")

	if err := os.WriteFile(filepath.Join(wtPath, "session.txt"), []byte("session"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "session.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-insession", "main", false, true, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}

	// Commit should be on main
	mainLog := runGit(t, repoDir, "log", "--oneline")
	if !strings.Contains(mainLog, "session commit") {
		t.Errorf("expected commit on main, got: %s", mainLog)
	}

	// Worktree should still exist (not removed)
	if _, err := os.Stat(wtPath); os.IsNotExist(err) {
		t.Error("expected worktree to still exist in session mode")
	}

	// Branch should still exist (not deleted)
	branchCheck := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "refs/heads/feature-insession")
	if err := branchCheck.Run(); err != nil {
		t.Error("expected branch to still exist in session mode")
	}

	// Detach should NOT have been called
	if mock.detachCalled {
		t.Error("expected Detach() to NOT be called in session mode")
	}
}

func TestResolvedInSessionTapOutput(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-session-tap")

	if err := os.WriteFile(filepath.Join(wtPath, "tap.txt"), []byte("tap"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "tap.txt")
	runGit(t, wtPath, "commit", "-m", "session tap commit")

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-session-tap", "main", false, true, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}

	got := buf.String()

	if !strings.Contains(got, "ok 1 - rebase feature-session-tap") {
		t.Errorf("expected rebase test point, got: %q", got)
	}
	if !strings.Contains(got, "ok 2 - merge feature-session-tap") {
		t.Errorf("expected merge test point, got: %q", got)
	}
	// Should NOT contain worktree removal or branch deletion
	if strings.Contains(got, "remove worktree") {
		t.Errorf("did not expect remove worktree in session mode, got: %q", got)
	}
	if strings.Contains(got, "delete branch") {
		t.Errorf("did not expect delete branch in session mode, got: %q", got)
	}
	if !strings.Contains(got, "1..2") {
		t.Errorf("expected plan 1..2, got: %q", got)
	}
}

func TestResolvedDisabledByMergeFlag(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-disabled")

	if err := os.WriteFile(filepath.Join(wtPath, "disabled.txt"), []byte("disabled"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "disabled.txt")
	runGit(t, wtPath, "commit", "-m", "disabled commit")

	// Drop a sweatfile in the worktree that disables merge.
	sweatfilePath := filepath.Join(wtPath, "sweatfile")
	if err := os.WriteFile(sweatfilePath, []byte("[hooks]\ndisable-merge = true\n"), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	// Snapshot main and feature-disabled state before the call so we can
	// verify nothing happened.
	mainLogBefore := runGit(t, repoDir, "log", "--oneline", "main")
	branchLogBefore := runGit(t, repoDir, "log", "--oneline", "feature-disabled")

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-disabled", "main", false, false, false)
	if err == nil {
		t.Fatal("expected error when disable-merge is set, got nil")
	}
	if !strings.Contains(err.Error(), "merge disabled") {
		t.Errorf("expected 'merge disabled' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "disable-merge") {
		t.Errorf("expected 'disable-merge' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sc check") {
		t.Errorf("expected 'sc check' hint in error, got: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "disable-merge") {
		t.Errorf("expected TAP output to mention 'disable-merge', got: %s", got)
	}
	if !strings.Contains(got, "sc check") {
		t.Errorf("expected TAP hint 'sc check', got: %s", got)
	}

	// No merge-y side effects: main and the feature branch should be
	// byte-identical to their pre-call state, the worktree directory must
	// still exist, and the branch must not have been deleted.
	mainLogAfter := runGit(t, repoDir, "log", "--oneline", "main")
	if mainLogBefore != mainLogAfter {
		t.Errorf("main branch log changed; merge guard did not short-circuit before git ops\nbefore:\n%s\nafter:\n%s", mainLogBefore, mainLogAfter)
	}
	branchLogAfter := runGit(t, repoDir, "log", "--oneline", "feature-disabled")
	if branchLogBefore != branchLogAfter {
		t.Errorf("feature-disabled branch log changed; rebase ran despite disable-merge\nbefore:\n%s\nafter:\n%s", branchLogBefore, branchLogAfter)
	}
	if _, statErr := os.Stat(wtPath); os.IsNotExist(statErr) {
		t.Error("expected worktree to still exist when merge is disabled, but it was removed")
	}
	branchCheck := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "refs/heads/feature-disabled")
	if err := branchCheck.Run(); err != nil {
		t.Errorf("expected branch feature-disabled to still exist when merge is disabled, but rev-parse failed: %v", err)
	}
}

func TestResolvedShortCircuitsNoOpMerge(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-noop")

	mainLogBefore := runGit(t, repoDir, "log", "--oneline", "main")
	branchLogBefore := runGit(t, repoDir, "log", "--oneline", "feature-noop")

	mock := &mockExecutor{}
	var buf bytes.Buffer

	_, err := Resolved(mock, &buf, nil, "tap", repoDir, wtPath, "feature-noop", "main", false, false, false)
	if err == nil {
		t.Fatalf("expected error when branch has no commits ahead of main, got nil. Output:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "nothing to merge") {
		t.Errorf("expected 'nothing to merge' in error, got: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "not ok") {
		t.Errorf("expected TAP 'not ok' on short-circuit, got:\n%s", got)
	}
	if !strings.Contains(got, "nothing to merge") {
		t.Errorf("expected TAP message to mention 'nothing to merge', got:\n%s", got)
	}
	if strings.Contains(got, "pre-merge hook") {
		t.Errorf("did not expect pre-merge hook to run when nothing to merge, got:\n%s", got)
	}

	// No merge-y side effects: branches, worktree, refs all intact.
	if mainLogBefore != runGit(t, repoDir, "log", "--oneline", "main") {
		t.Error("main branch log changed; short-circuit ran the merge anyway")
	}
	if branchLogBefore != runGit(t, repoDir, "log", "--oneline", "feature-noop") {
		t.Error("feature-noop branch log changed; short-circuit did not happen cleanly")
	}
	if _, statErr := os.Stat(wtPath); os.IsNotExist(statErr) {
		t.Error("expected worktree to still exist when short-circuited")
	}
	if !git.BranchExists(repoDir, "feature-noop") {
		t.Error("expected branch feature-noop to still exist after short-circuit")
	}
}

// TestMergeImplicitRunsHookThenPushesNoRebase verifies the implicit-session
// merge path: the work is already on the default branch (a main checkout, not
// a feature worktree), so MergeImplicit runs the pre-merge hook against HEAD
// and pushes the default branch — with no rebase and no ff-merge. The hook is
// proven to run via a marker file it touches; the push is proven by the bare
// upstream's master advancing to the checkout's HEAD.
func TestMergeImplicitRunsHookThenPushesNoRebase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)

	// Bare upstream (push target).
	bare := filepath.Join(root, "upstream.git")
	runGit(t, root, "init", "--bare", "-b", "master", bare)

	// Clone into the "main checkout" on master, with origin tracking the bare.
	checkout := filepath.Join(root, "checkout")
	runGit(t, root, "clone", bare, checkout)
	runGit(t, checkout, "config", "user.email", "test@test.com")
	runGit(t, checkout, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(checkout, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "file.txt")
	runGit(t, checkout, "commit", "-m", "initial")
	runGit(t, checkout, "push", "-u", "origin", "master")

	// Sweatfile with a pre-merge hook that touches a marker — proves it ran.
	marker := filepath.Join(root, "hook-ran.marker")
	sweatfileBody := "[hooks]\npre-merge = \"touch " + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(checkout, "sweatfile"), []byte(sweatfileBody), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	// The "work already on the default branch": a new commit on master.
	if err := os.WriteFile(filepath.Join(checkout, "work.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "work.txt")
	runGit(t, checkout, "commit", "-m", "work on master")

	var buf bytes.Buffer
	tw := NewMergeWriter(&buf)
	links, err := MergeImplicit(context.Background(), tw, &buf, checkout, checkout, "master", true, nil)
	tw.Plan()
	if err != nil {
		t.Fatalf("MergeImplicit: %v\nTAP:\n%s", err, buf.String())
	}
	_ = links

	// Hook ran: marker exists.
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("expected pre-merge hook marker %s to exist, stat err: %v\nTAP:\n%s", marker, statErr, buf.String())
	}

	// Push landed: bare upstream's master == checkout HEAD.
	bareMaster := runGit(t, bare, "rev-parse", "master")
	checkoutHead := runGit(t, checkout, "rev-parse", "HEAD")
	if bareMaster != checkoutHead {
		t.Errorf("push did not land: bare master = %s, checkout HEAD = %s", bareMaster, checkoutHead)
	}

	got := buf.String()
	if !strings.Contains(got, "push master") {
		t.Errorf("expected 'push master' TAP step, got:\n%s", got)
	}
	if strings.Contains(got, "rebase") {
		t.Errorf("did not expect any rebase in implicit merge, got:\n%s", got)
	}
}

// TestMergeImplicitDisabledByMergeFlag verifies the disable-merge gate short-
// circuits MergeImplicit before any push: an error mentioning disable-merge,
// and the bare upstream's master left untouched.
func TestMergeImplicitDisabledByMergeFlag(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)

	bare := filepath.Join(root, "upstream.git")
	runGit(t, root, "init", "--bare", "-b", "master", bare)

	checkout := filepath.Join(root, "checkout")
	runGit(t, root, "clone", bare, checkout)
	runGit(t, checkout, "config", "user.email", "test@test.com")
	runGit(t, checkout, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(checkout, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "file.txt")
	runGit(t, checkout, "commit", "-m", "initial")
	runGit(t, checkout, "push", "-u", "origin", "master")

	// Sweatfile disabling merge.
	if err := os.WriteFile(filepath.Join(checkout, "sweatfile"), []byte("[hooks]\ndisable-merge = true\n"), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	// A new commit that is NOT yet on the upstream.
	if err := os.WriteFile(filepath.Join(checkout, "work.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "work.txt")
	runGit(t, checkout, "commit", "-m", "work on master")

	bareMasterBefore := runGit(t, bare, "rev-parse", "master")

	var buf bytes.Buffer
	tw := NewMergeWriter(&buf)
	_, err := MergeImplicit(context.Background(), tw, &buf, checkout, checkout, "master", false, nil)
	tw.Plan()
	if err == nil {
		t.Fatalf("expected error when disable-merge is set, got nil\nTAP:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "merge disabled") {
		t.Errorf("expected 'merge disabled' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "disable-merge") {
		t.Errorf("expected 'disable-merge' in error, got: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "disable-merge") {
		t.Errorf("expected TAP output to mention 'disable-merge', got:\n%s", got)
	}
	if strings.Contains(got, "push") {
		t.Errorf("did not expect any push step when merge is disabled, got:\n%s", got)
	}

	// No push happened: upstream master unchanged.
	if bareMasterAfter := runGit(t, bare, "rev-parse", "master"); bareMasterAfter != bareMasterBefore {
		t.Errorf("upstream master advanced despite disable-merge: before %s, after %s", bareMasterBefore, bareMasterAfter)
	}
}

func TestIsInsideSession(t *testing.T) {
	t.Run("no env var", func(t *testing.T) {
		t.Setenv("SPINCLASS_SESSION_ID", "")
		if isInsideSession("/tmp/repo/.worktrees/branch", "/tmp/repo/.worktrees/branch") {
			t.Error("expected false when SPINCLASS_SESSION_ID is empty")
		}
	})

	t.Run("env set and cwd matches wtPath", func(t *testing.T) {
		t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
		if !isInsideSession("/tmp/repo/.worktrees/branch", "/tmp/repo/.worktrees/branch") {
			t.Error("expected true when cwd equals wtPath")
		}
	})

	t.Run("env set and cwd is subdirectory", func(t *testing.T) {
		t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
		if !isInsideSession("/tmp/repo/.worktrees/branch/src/pkg", "/tmp/repo/.worktrees/branch") {
			t.Error("expected true when cwd is inside wtPath")
		}
	})

	t.Run("env set but cwd is outside wtPath", func(t *testing.T) {
		t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
		if isInsideSession("/tmp/other-repo", "/tmp/repo/.worktrees/branch") {
			t.Error("expected false when cwd is outside wtPath")
		}
	})

	t.Run("env set but cwd is sibling prefix", func(t *testing.T) {
		t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
		if isInsideSession("/tmp/repo/.worktrees/branch-other", "/tmp/repo/.worktrees/branch") {
			t.Error("expected false for sibling path that shares prefix")
		}
	})
}

func TestIsInsideWorktree(t *testing.T) {
	t.Run("cwd matches wtPath", func(t *testing.T) {
		if !isInsideWorktree("/tmp/repo/.worktrees/branch", "/tmp/repo/.worktrees/branch") {
			t.Error("expected true when cwd equals wtPath")
		}
	})

	t.Run("cwd is subdirectory", func(t *testing.T) {
		if !isInsideWorktree("/tmp/repo/.worktrees/branch/src/pkg", "/tmp/repo/.worktrees/branch") {
			t.Error("expected true when cwd is inside wtPath")
		}
	})

	t.Run("cwd is outside wtPath", func(t *testing.T) {
		if isInsideWorktree("/tmp/other-repo", "/tmp/repo/.worktrees/branch") {
			t.Error("expected false when cwd is outside wtPath")
		}
	})

	t.Run("cwd is sibling prefix", func(t *testing.T) {
		if isInsideWorktree("/tmp/repo/.worktrees/branch-other", "/tmp/repo/.worktrees/branch") {
			t.Error("expected false for sibling path that shares prefix")
		}
	})
}
