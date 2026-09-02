package merge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupSyncRepo builds a bare "origin" plus a clone acting as the repo's main
// checkout, with one pushed initial commit. Returns (bareDir, repoDir).
func setupSyncRepo(t *testing.T) (bareDir, repoDir string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)

	bareDir = filepath.Join(root, "bare.git")
	runGit(t, root, "init", "--bare", "-b", "main", bareDir)

	repoDir = filepath.Join(root, "repo")
	runGit(t, root, "clone", bareDir, repoDir)
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "initial")
	runGit(t, repoDir, "push")
	return bareDir, repoDir
}

// TestResolvedGitSyncLandsOnOriginWithoutAdvancingRootRef is the core #284
// (Alt B) property: a gitSync worktree merge lands by pushing the landing sha
// straight to origin/<default> from a disposable detached worktree. The root
// checkout's LOCAL default ref is never advanced by the merge — it stays where
// the pre-merge pull left it — while origin (and the remote-tracking ref) carry
// the session commit.
func TestResolvedGitSyncLandsOnOriginWithoutAdvancingRootRef(t *testing.T) {
	bareDir, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-land")
	if err := os.WriteFile(filepath.Join(wtPath, "land.txt"), []byte("land"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "land.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")
	sessionSha := runGit(t, wtPath, "rev-parse", "HEAD")
	rootMainBefore := runGit(t, repoDir, "rev-parse", "main")

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-land", "main", true, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v\n%+v", err, recs)
	}

	if got := runGit(t, bareDir, "rev-parse", "main"); got != sessionSha {
		t.Errorf("origin main = %s, want the session commit %s", got, sessionSha)
	}
	if got := runGit(t, repoDir, "rev-parse", "origin/main"); got != sessionSha {
		t.Errorf("remote-tracking origin/main = %s, want %s", got, sessionSha)
	}
	if got := runGit(t, repoDir, "rev-parse", "main"); got != rootMainBefore {
		t.Errorf("root local main advanced to %s; Alt B must leave it at %s", got, rootMainBefore)
	}
	assertNoLandWorktrees(t, repoDir)

	// The landing is the push: one "merge <branch>" point, no separate "push".
	tests := testRecords(recs)
	if len(tests) != 6 {
		t.Fatalf("expected 6 test records, got %d: %+v", len(tests), tests)
	}
	assertTestPoint(t, tests, 0, "pull main", true)
	assertTestPoint(t, tests, 1, "rebase feature-land", true)
	assertTestPoint(t, tests, 2, "pull main (landing)", true)
	assertTestPoint(t, tests, 3, "merge feature-land", true)
	assertTestPoint(t, tests, 4, "remove worktree feature-land", true)
	assertTestPoint(t, tests, 5, "delete branch feature-land", true)
}

// TestResolvedGitSyncPushFailureIsCleanNoOp pins #284's failure-symmetry
// payoff: when the landing push is refused (here: a dead push URL, standing in
// for a dropped credential), NOTHING has moved — origin, the root's local
// default ref, the session branch and its worktree are all exactly as before,
// so a re-merge is a plain retry rather than a divergence repair.
func TestResolvedGitSyncPushFailureIsCleanNoOp(t *testing.T) {
	bareDir, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-noop")
	if err := os.WriteFile(filepath.Join(wtPath, "noop.txt"), []byte("noop"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "noop.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")
	sessionSha := runGit(t, wtPath, "rev-parse", "HEAD")
	originBefore := runGit(t, bareDir, "rev-parse", "main")
	rootMainBefore := runGit(t, repoDir, "rev-parse", "main")

	// Fetch keeps working (the landing pull succeeds); only the push dies.
	runGit(t, repoDir, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "nonexistent.git"))

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-noop", "main", true, false)
	if err == nil {
		t.Fatalf("expected the merge to fail on the dead push URL\n%+v", recs)
	}

	if got := runGit(t, bareDir, "rev-parse", "main"); got != originBefore {
		t.Errorf("origin main moved to %s on a failed push, want %s", got, originBefore)
	}
	if got := runGit(t, repoDir, "rev-parse", "main"); got != rootMainBefore {
		t.Errorf("root local main advanced to %s on a failed push, want %s (no divergence)", got, rootMainBefore)
	}
	if got := runGit(t, repoDir, "rev-parse", "refs/heads/feature-noop"); got != sessionSha {
		t.Errorf("session branch = %s after failed push, want %s", got, sessionSha)
	}
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Errorf("session worktree torn down after a failed push: %v", statErr)
	}
	assertNoLandWorktrees(t, repoDir)

	tests := testRecords(recs)
	var failed []string
	for _, tr := range tests {
		if !tr.OK {
			failed = append(failed, tr.Description)
		}
	}
	if len(failed) != 1 || !strings.HasPrefix(failed[0], "merge feature-noop") {
		t.Errorf("expected exactly the landing point to fail, got failures %v in %+v", failed, tests)
	}
}
