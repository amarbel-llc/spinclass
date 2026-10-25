package close

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// setupVanishedWorktree reproduces #409's aftermath: a recorded session whose
// worktree git has already removed (and whose branch is gone) while its agent
// recreated .tmp in the old path. The main checkout is left dirty so a close
// that wrongly ran git in the leftover directory, which resolves to the main
// checkout, would see dirt.
func setupVanishedWorktree(t *testing.T, leftovers ...string) (repoPath, wtPath string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	repoPath = filepath.Join(root, "repo")
	wtPath = filepath.Join(repoPath, ".worktrees", "gone")
	testgit.MustInit(t, repoPath)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "gone")
	if err := session.Write(session.State{
		SessionState: session.StateActive, RepoPath: repoPath, WorktreePath: wtPath,
		Branch: "gone", SessionKey: "repo/gone", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", repoPath, "worktree", "remove", "--force", wtPath},
		{"-C", repoPath, "branch", "-D", "gone"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(wtPath, ".tmp", "claude-1001"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range leftovers {
		if err := os.WriteFile(filepath.Join(wtPath, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoPath, "main-checkout-dirt.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := closeInteractive
	closeInteractive = func() bool { return false }
	t.Cleanup(func() { closeInteractive = orig })
	return repoPath, wtPath
}

// #409: a session whose worktree is gone from git closes without --force:
// there is no tree left to protect, the leftover .tmp is removed, and the
// session is tombstoned instead of failing on "not a working tree".
func TestRunResolvedReapsASessionWhoseWorktreeVanished(t *testing.T) {
	repoPath, wtPath := setupVanishedWorktree(t)
	var out strings.Builder
	if err := RunResolved(&out, repoPath, wtPath, "gone", false, nil, "tap"); err != nil {
		t.Fatalf("RunResolved: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("the leftover directory was not removed: %v", err)
	}
	st, err := session.Read(repoPath, "gone")
	if err != nil || !st.IsTombstone() {
		t.Errorf("session not tombstoned: %+v, %v", st, err)
	}
	if !strings.Contains(out.String(), "already removed") {
		t.Errorf("the output should say the worktree was already gone:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(repoPath, "main-checkout-dirt.txt")); err != nil {
		t.Errorf("the main checkout was touched: %v", err)
	}
}

// Only a live session record makes a missing worktree "vanished": closing an
// already-closed (tombstoned) session again is an error, not a second success.
func TestRunResolvedRefusesToCloseAnAlreadyClosedSession(t *testing.T) {
	repoPath, wtPath := setupVanishedWorktree(t)
	if err := RunResolved(io.Discard, repoPath, wtPath, "gone", false, nil, "tap"); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := RunResolved(io.Discard, repoPath, wtPath, "gone", true, nil, "tap"); err == nil {
		t.Fatal("closing an already-closed session succeeded")
	}
}

// The vanished-worktree path deletes a directory, so it must never reach the
// main checkout or anything outside <repo>/.worktrees/, even forced.
func TestRunResolvedNeverDeletesAPathThatIsNotAWorktreeSlot(t *testing.T) {
	repoPath, _ := setupVanishedWorktree(t)
	for name, target := range map[string]string{
		"main checkout": repoPath,
		"outside":       t.TempDir(),
	} {
		if err := RunResolved(io.Discard, repoPath, target, "gone", true, nil, "tap"); err == nil {
			t.Errorf("%s: closed %s as a vanished worktree", name, target)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("%s: %s was removed: %v", name, target, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		t.Errorf("the main checkout's .git is gone: %v", err)
	}
}

// A vanished worktree whose branch survived with commits not on the default
// branch still has work to lose: the close refuses unless forced, and a forced
// close records the branch tip so sc resurrect can bring it back.
func TestRunResolvedKeepsAnUnintegratedBranchOfAVanishedWorktree(t *testing.T) {
	repoPath, wtPath := setupVanishedWorktree(t)
	testgit.MustGit(t, repoPath, "branch", "gone")
	testgit.MustGit(t, repoPath, "commit", "--allow-empty", "-m", "on master")
	testgit.MustGit(t, repoPath, "checkout", "-q", "gone")
	testgit.MustGit(t, repoPath, "commit", "--allow-empty", "-m", "session work")
	tip := strings.TrimSpace(testgit.MustGit(t, repoPath, "rev-parse", "HEAD"))
	testgit.MustGit(t, repoPath, "checkout", "-q", "-")

	err := RunResolved(io.Discard, repoPath, wtPath, "gone", false, nil, "tap")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal pointing at --force", err)
	}
	if out := testgit.MustGit(t, repoPath, "branch", "--list", "gone"); !strings.Contains(out, "gone") {
		t.Fatal("the unintegrated branch was deleted by a refused close")
	}
	if err := RunResolved(io.Discard, repoPath, wtPath, "gone", true, nil, "tap"); err != nil {
		t.Fatalf("forced close: %v", err)
	}
	st, err := session.Read(repoPath, "gone")
	if err != nil || !st.IsTombstone() || st.DeletedSHA != tip {
		t.Errorf("tombstone = %+v, %v; want DeletedSHA %s", st, err, tip)
	}
}

// Anything in the leftover directory besides .tmp and .spinclass is not ours
// to delete: the close refuses unless forced, and leaves it in place.
func TestRunResolvedKeepsUnexpectedFilesInAVanishedWorktree(t *testing.T) {
	repoPath, wtPath := setupVanishedWorktree(t, "notes.md")
	err := RunResolved(io.Discard, repoPath, wtPath, "gone", false, nil, "tap")
	if err == nil || !strings.Contains(err.Error(), "notes.md") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal naming the leftover file and --force", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "notes.md")); err != nil {
		t.Errorf("an unexpected leftover was removed: %v", err)
	}
	if err := RunResolved(io.Discard, repoPath, wtPath, "gone", true, nil, "tap"); err != nil {
		t.Fatalf("forced close: %v", err)
	}
	if st, err := session.Read(repoPath, "gone"); err != nil || !st.IsTombstone() {
		t.Errorf("forced close did not tombstone: %+v, %v", st, err)
	}
}
