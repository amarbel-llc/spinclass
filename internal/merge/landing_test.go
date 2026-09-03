package merge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/auth"
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

// TestResolvedGitSyncMirrorsCredentialIntoLandingWorktree covers the FDR 0028
// injection surface: when the session worktree carries a minted credential,
// the landing worktree the push runs from gets the same worktree-scoped
// credential wiring — observed from the post-merge phase, which runs there.
func TestResolvedGitSyncMirrorsCredentialIntoLandingWorktree(t *testing.T) {
	_, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-cred")
	if err := os.WriteFile(filepath.Join(wtPath, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "c.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")

	// What sc start's setup applies: .spinclass/ is git-excluded, so teardown's
	// plain `git worktree remove` does not see the credential as untracked
	// (a host's global gitignore must not be what makes this pass).
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A minted credential: the file plus the session worktree's wiring (a
	// path origin has no ssh prefix, so only the helper is set).
	credPath := filepath.Join(wtPath, ".spinclass", auth.CredentialFile)
	if err := os.MkdirAll(filepath.Dir(credPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte("https://spinclass:tok@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := auth.Inject(wtPath, credPath, auth.Remote{Host: "example.com"}); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	seen := filepath.Join(t.TempDir(), "helper")
	sweatfileBody := "[hooks]\npost-merge = \"git config --get credential.helper > " + seen + "\"\n"
	if err := os.WriteFile(filepath.Join(repoDir, "sweatfile"), []byte(sweatfileBody), 0o644); err != nil {
		t.Fatal(err)
	}

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-cred", "main", true, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v\n%+v", err, recs)
	}
	got, readErr := os.ReadFile(seen)
	if readErr != nil {
		t.Fatalf("post-merge hook did not run in the landing worktree: %v\n%+v", readErr, recs)
	}
	if want := "store --file=" + credPath; strings.TrimSpace(string(got)) != want {
		t.Errorf("landing worktree credential.helper = %q, want %q", strings.TrimSpace(string(got)), want)
	}
	assertNoLandWorktrees(t, repoDir)
}

// TestResolvedGitSyncTeardownRevokesCredential covers FDR 0028's revoke on
// the merge's own teardown: an out-of-session merge removes the session
// worktree, so the [auth].revoke-command must run first or the token is
// orphaned (no tombstone is written on this path for the sweep to find).
func TestResolvedGitSyncTeardownRevokesCredential(t *testing.T) {
	_, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-revoke")
	if err := os.WriteFile(filepath.Join(wtPath, "r.txt"), []byte("r"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "r.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(wtPath, ".spinclass", auth.CredentialFile)
	if err := os.MkdirAll(filepath.Dir(credPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte("https://spinclass:tok@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "revoked")
	sweatfileBody := "[auth]\nmint-command = \"echo tok\"\nrevoke-command = \"echo $SPINCLASS_SESSION_ID > " + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(repoDir, "sweatfile"), []byte(sweatfileBody), 0o644); err != nil {
		t.Fatal(err)
	}

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-revoke", "main", true, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v\n%+v", err, recs)
	}
	if got, rerr := os.ReadFile(marker); rerr != nil || strings.TrimSpace(string(got)) != "repo/feature-revoke" {
		t.Errorf("revoke-command did not run before teardown: %q (%v)", got, rerr)
	}
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Errorf("worktree not removed: %v", statErr)
	}
	found := false
	for _, tr := range testRecords(recs) {
		if tr.Description == "revoke credential feature-revoke" && tr.OK {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an ok 'revoke credential feature-revoke' point, got %+v", testRecords(recs))
	}
}

// TestResolvedGitSyncPullWithRootOffDefault covers the credentialed pull's
// ref-move path: the root checkout is parked on another branch, so the
// default branch is not checked out anywhere and the pull advances its ref
// directly (a `git pull` in the root would have pulled into the parked branch).
func TestResolvedGitSyncPullWithRootOffDefault(t *testing.T) {
	bareDir, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-parked")
	if err := os.WriteFile(filepath.Join(wtPath, "p.txt"), []byte("p"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "p.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")
	runGit(t, repoDir, "checkout", "-b", "parked")

	// origin moves via a second clone.
	other := filepath.Join(filepath.Dir(repoDir), "other")
	runGit(t, filepath.Dir(repoDir), "clone", bareDir, other)
	runGit(t, other, "config", "user.email", "o@test.com")
	runGit(t, other, "config", "user.name", "Other")
	if err := os.WriteFile(filepath.Join(other, "o.txt"), []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "o.txt")
	runGit(t, other, "commit", "-m", "concurrent commit on origin")
	runGit(t, other, "push")
	originTip := runGit(t, bareDir, "rev-parse", "main")

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-parked", "main", true, false)
	if err != nil {
		t.Fatalf("Resolved() error: %v\n%+v", err, recs)
	}
	if got := runGit(t, repoDir, "rev-parse", "main"); got != originTip {
		t.Errorf("local main = %s after the pull, want origin's pre-merge tip %s", got, originTip)
	}
	if got := runGit(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"); got != "parked" {
		t.Errorf("root checkout moved off its parked branch: %s", got)
	}
	log := runGit(t, bareDir, "log", "--oneline", "main")
	if !strings.Contains(log, "session commit") || !strings.Contains(log, "concurrent commit on origin") {
		t.Errorf("origin main missing commits:\n%s", log)
	}
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
