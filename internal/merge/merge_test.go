package merge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
	"github.com/amarbel-llc/crap/go-crap/v2/ndjsoncrap"
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

// decodeRecords parses a buffered ndjson-crap stream into its records.
func decodeRecords(t *testing.T, raw []byte) []ndjsoncrap.Record {
	t.Helper()
	rd := ndjsoncrap.NewReader(bytes.NewReader(raw))
	var recs []ndjsoncrap.Record
	for {
		rec, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decoding ndjson-crap stream: %v\nstream:\n%s", err, raw)
		}
		recs = append(recs, rec)
	}
	return recs
}

// testRecords extracts the result-family test records from the stream,
// in wire order.
func testRecords(recs []ndjsoncrap.Record) []ndjsoncrap.Test {
	var tests []ndjsoncrap.Test
	for _, rec := range recs {
		if tr, ok := rec.(ndjsoncrap.Test); ok {
			tests = append(tests, tr)
		}
	}
	return tests
}

// hasSummary reports whether the stream carries the result-family
// summary framing (the ndjson-crap analogue of TAP's plan line).
func hasSummary(recs []ndjsoncrap.Record) bool {
	for _, rec := range recs {
		if _, ok := rec.(ndjsoncrap.Summary); ok {
			return true
		}
	}
	return false
}

// assertTestPoint asserts tests[i] exists, has 1-based number i+1, the given
// description, and the given verdict.
func assertTestPoint(t *testing.T, tests []ndjsoncrap.Test, i int, desc string, ok bool) {
	t.Helper()
	if i >= len(tests) {
		t.Fatalf("expected test point #%d %q, but stream has only %d test records", i+1, desc, len(tests))
	}
	tr := tests[i]
	if tr.N != i+1 {
		t.Errorf("test point %q: n = %d, want %d", desc, tr.N, i+1)
	}
	if tr.Description != desc {
		t.Errorf("test point #%d: description = %q, want %q", i+1, tr.Description, desc)
	}
	if tr.OK != ok {
		t.Errorf("test point %q: ok = %v, want %v", desc, tr.OK, ok)
	}
}

// runResolved drives Resolved with a Reporter over a bytes.Buffer (a
// Reporter writes its Meta record synchronously, so no pipe) and returns
// the decoded record stream and Resolved's error.
func runResolved(t *testing.T, mock *mockExecutor, repoDir, wtPath, branch, defaultBranch string, gitSync, inSession bool) ([]ndjsoncrap.Record, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, err := Resolved(mock, rep, ts, repoDir, wtPath, branch, defaultBranch, gitSync, inSession)
	ts.Finish()
	return decodeRecords(t, buf.Bytes()), err
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
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	pinnedSha, err := PrepareMerge(ts, repoDir, wtPath, "feature-pin", "main", false)
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
	if _, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature-pin", "main", pinnedSha, false, true, nil); err != nil {
		t.Fatalf("FinishMerge: %v\n%s", err, buf.String())
	}
	ts.Finish()

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

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-merge", "main", false, false)
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

	// Every stage should be a passing test record on the wire.
	tests := testRecords(recs)
	if len(tests) == 0 {
		t.Fatalf("expected test records, got none")
	}
	for _, tr := range tests {
		if !tr.OK {
			t.Errorf("expected all stages ok, got failing %+v", tr)
		}
	}
}

func TestResolvedRecordStream(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-tap")

	if err := os.WriteFile(filepath.Join(wtPath, "tap.txt"), []byte("tap"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "tap.txt")
	runGit(t, wtPath, "commit", "-m", "tap commit")

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-tap", "main", false, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}

	tests := testRecords(recs)
	if len(tests) != 4 {
		t.Fatalf("expected 4 test records, got %d: %+v", len(tests), tests)
	}
	assertTestPoint(t, tests, 0, "rebase feature-tap", true)
	assertTestPoint(t, tests, 1, "merge feature-tap", true)
	assertTestPoint(t, tests, 2, "remove worktree feature-tap", true)
	assertTestPoint(t, tests, 3, "delete branch feature-tap", true)
	if !hasSummary(recs) {
		t.Errorf("expected summary record (stream framing), got: %+v", recs)
	}
}

func TestResolvedGitSyncRecordStream(t *testing.T) {
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

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-sync", "main", true, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}

	tests := testRecords(recs)
	if len(tests) != 6 {
		t.Fatalf("expected 6 test records, got %d: %+v", len(tests), tests)
	}
	// Pull must come first so the rebase target is fresh.
	assertTestPoint(t, tests, 0, "pull main", true)
	assertTestPoint(t, tests, 1, "rebase feature-sync", true)
	assertTestPoint(t, tests, 2, "merge feature-sync", true)
	assertTestPoint(t, tests, 3, "remove worktree feature-sync", true)
	assertTestPoint(t, tests, 4, "delete branch feature-sync", true)
	assertTestPoint(t, tests, 5, "push", true)
	if !hasSummary(recs) {
		t.Errorf("expected summary record (stream framing), got: %+v", recs)
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

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-stale", "main", true, false)
	if err != nil {
		t.Fatalf("Resolved() error (stale local master should not cause failure): %v\n\nrecords:\n%+v", err, recs)
	}

	tests := testRecords(recs)
	assertTestPoint(t, tests, 0, "pull main", true)
	merged := false
	for _, tr := range tests {
		if tr.Description == "merge feature-stale" && tr.OK {
			merged = true
		}
	}
	if !merged {
		t.Errorf("expected merge to succeed after upfront pull, got: %+v", tests)
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
	recs, err := runResolved(t, &mockExecutor{}, "/nonexistent/path", "/nonexistent/wt", "feature", "main", false, false)
	if err == nil {
		t.Error("expected error for nonexistent repo, got nil")
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("expected 'repository not found' error, got: %v", err)
	}
	if tests := testRecords(recs); len(tests) != 0 {
		t.Errorf("expected no test records before repo validation, got: %+v", tests)
	}
}

func TestResolvedRequiresDefaultBranch(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-nodefault")

	_, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-nodefault", "", false, false)
	if err == nil {
		t.Fatal("expected error for empty defaultBranch, got nil")
	}
	if !strings.Contains(err.Error(), "default branch not resolved") {
		t.Errorf("expected 'default branch not resolved' error, got: %v", err)
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

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-diverge", "main", false, false)
	if err == nil {
		t.Error("expected error for conflicting rebase, got nil")
	}

	// The rebase failure surfaces as a failing test record with the git
	// output in its diagnostic.
	tests := testRecords(recs)
	if len(tests) != 1 || tests[0].OK {
		t.Errorf("expected exactly one failing rebase record, got: %+v", tests)
	} else {
		if tests[0].Description != "rebase feature-diverge" {
			t.Errorf("expected failing 'rebase feature-diverge' record, got %q", tests[0].Description)
		}
		if out, _ := tests[0].Diagnostic["output"].(string); out == "" {
			t.Errorf("expected git output in failure diagnostic, got %+v", tests[0].Diagnostic)
		}
	}

	// Abort the rebase to clean up
	_ = exec.Command("git", "-C", wtPath, "rebase", "--abort").Run()
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
	_, err := runResolved(t, mock, repoDir, wtPath, "feature-insession", "main", false, true)
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

func TestResolvedInSessionRecordStream(t *testing.T) {
	repoDir := setupRepo(t)

	wtPath := setupWorktree(t, repoDir, "feature-session-tap")

	if err := os.WriteFile(filepath.Join(wtPath, "tap.txt"), []byte("tap"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "tap.txt")
	runGit(t, wtPath, "commit", "-m", "session tap commit")

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-session-tap", "main", false, true)
	if err != nil {
		t.Fatalf("Resolved() error: %v", err)
	}

	tests := testRecords(recs)
	if len(tests) != 2 {
		t.Fatalf("expected exactly 2 test records in session mode (no cleanup stages), got %d: %+v", len(tests), tests)
	}
	assertTestPoint(t, tests, 0, "rebase feature-session-tap", true)
	assertTestPoint(t, tests, 1, "merge feature-session-tap", true)
	if !hasSummary(recs) {
		t.Errorf("expected summary record (stream framing), got: %+v", recs)
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

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-disabled", "main", false, false)
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

	// The gate surfaces as a single failing test record whose diagnostic
	// carries the full message.
	tests := testRecords(recs)
	if len(tests) != 1 || tests[0].OK {
		t.Fatalf("expected exactly one failing test record, got: %+v", tests)
	}
	msg, _ := tests[0].Diagnostic["message"].(string)
	if !strings.Contains(msg, "disable-merge") {
		t.Errorf("expected diagnostic message to mention 'disable-merge', got: %q", msg)
	}
	if !strings.Contains(msg, "sc check") {
		t.Errorf("expected diagnostic hint 'sc check', got: %q", msg)
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

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-noop", "main", false, false)
	if err == nil {
		t.Fatalf("expected error when branch has no commits ahead of main, got nil. Records:\n%+v", recs)
	}
	if !strings.Contains(err.Error(), "nothing to merge") {
		t.Errorf("expected 'nothing to merge' in error, got: %v", err)
	}

	// The short-circuit is a failing 'merge' record after the passing
	// rebase; the pre-merge hook never runs.
	tests := testRecords(recs)
	if len(tests) != 2 {
		t.Fatalf("expected rebase + failing merge records, got %d: %+v", len(tests), tests)
	}
	assertTestPoint(t, tests, 0, "rebase feature-noop", true)
	assertTestPoint(t, tests, 1, "merge feature-noop", false)
	if msg, _ := tests[1].Diagnostic["message"].(string); !strings.Contains(msg, "nothing to merge") {
		t.Errorf("expected diagnostic message to mention 'nothing to merge', got: %q", msg)
	}
	for _, tr := range tests {
		if strings.Contains(tr.Description, "pre-merge hook") {
			t.Errorf("did not expect pre-merge hook to run when nothing to merge, got: %+v", tr)
		}
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
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	links, err := MergeImplicit(context.Background(), rep, ts, checkout, checkout, "master", nil)
	ts.Finish()
	if err != nil {
		t.Fatalf("MergeImplicit: %v\nrecords:\n%s", err, buf.String())
	}
	_ = links

	// Hook ran: marker exists.
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("expected pre-merge hook marker %s to exist, stat err: %v\nrecords:\n%s", marker, statErr, buf.String())
	}

	// Push landed: bare upstream's master == checkout HEAD.
	bareMaster := runGit(t, bare, "rev-parse", "master")
	checkoutHead := runGit(t, checkout, "rev-parse", "HEAD")
	if bareMaster != checkoutHead {
		t.Errorf("push did not land: bare master = %s, checkout HEAD = %s", bareMaster, checkoutHead)
	}

	recs := decodeRecords(t, buf.Bytes())
	tests := testRecords(recs)
	pushed := false
	for _, tr := range tests {
		if tr.Description == "push master" && tr.OK {
			pushed = true
		}
		if strings.Contains(tr.Description, "rebase") {
			t.Errorf("did not expect any rebase in implicit merge, got: %+v", tr)
		}
	}
	if !pushed {
		t.Errorf("expected passing 'push master' test record, got: %+v", tests)
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
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, err := MergeImplicit(context.Background(), rep, ts, checkout, checkout, "master", nil)
	ts.Finish()
	if err == nil {
		t.Fatalf("expected error when disable-merge is set, got nil\nrecords:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "merge disabled") {
		t.Errorf("expected 'merge disabled' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "disable-merge") {
		t.Errorf("expected 'disable-merge' in error, got: %v", err)
	}

	recs := decodeRecords(t, buf.Bytes())
	tests := testRecords(recs)
	if len(tests) != 1 || tests[0].OK {
		t.Fatalf("expected exactly one failing test record, got: %+v", tests)
	}
	if msg, _ := tests[0].Diagnostic["message"].(string); !strings.Contains(msg, "disable-merge") {
		t.Errorf("expected diagnostic message to mention 'disable-merge', got: %q", msg)
	}
	for _, tr := range tests {
		if strings.Contains(tr.Description, "push") {
			t.Errorf("did not expect any push step when merge is disabled, got: %+v", tr)
		}
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
