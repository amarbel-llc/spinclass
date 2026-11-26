package spawn

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
	"code.linenisgreat.com/spinclass/internal/delta/close"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// madeTree is a worktree as a spawn makes one, with no session record yet, and
// the commit it was made at.
func madeTree(t *testing.T) (rp worktree.ResolvedPath, head string, record session.State) {
	t.Helper()
	_, repoPath := newWorkerFixture(t, "")
	rp = worktree.ResolvedPath{
		RepoPath: repoPath, AbsPath: filepath.Join(repoPath, ".worktrees", "made-tree"),
		Branch: "made-tree", SessionKey: "worker/made-tree",
	}
	gitIn(t, repoPath, "worktree", "add", "-q", "-b", rp.Branch, rp.AbsPath)
	// As a spawn-made worktree has it: spinclass's own files are not changes.
	exclude := filepath.Join(repoPath, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return rp, gitIn(t, rp.AbsPath, "rev-parse", "HEAD"), newWorkerState(rp, testDriver("driver/test-session"), "", ResolvedProfile{}, "", nil)
}

// closeCalls records the teardowns a rollback asks for, without running them.
type closeCalls struct {
	n        int
	override *bool
	err      error
}

func spyRollbackClose(t *testing.T, result error) *closeCalls {
	t.Helper()
	c := &closeCalls{err: result}
	prev := rollbackClose
	t.Cleanup(func() { rollbackClose = prev })
	rollbackClose = func(_ io.Writer, _, _, _ string, _ bool, nixGCOverride *bool, _ string) error {
		c.n, c.override = c.n+1, nixGCOverride
		return c.err
	}
	return c
}

var printedClose = regexp.MustCompile(`sc close (\S+)( --force)?`)

// runPrintedClose runs the `sc close` command a refusal printed, as printed.
func runPrintedClose(t *testing.T, refusal error) {
	t.Helper()
	m := printedClose.FindStringSubmatch(refusal.Error())
	if m == nil {
		t.Fatalf("no `sc close` command in: %v", refusal)
	}
	skipGC := false
	if err := close.Run(io.Discard, m[1], m[2] != "", &skipGC, "", slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("the printed command %q does not work: %v", m[0], err)
	}
}

func assertGone(t *testing.T, rp worktree.ResolvedPath) {
	t.Helper()
	if _, err := os.Stat(rp.AbsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the worktree is still there: %v", err)
	}
}

func TestTheCommandARefusalPrintsRemovesTheKeptWorktree(t *testing.T) {
	rp, head, record := madeTree(t)
	if err := os.WriteFile(filepath.Join(rp.AbsPath, "precious"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := refusedAfterCreate(rp, false, head, record, errors.New("boom"))
	runPrintedClose(t, err)
	assertGone(t, rp)
}

func TestTheCommandARefusalPrintsForAPreexistingWorktreeResolves(t *testing.T) {
	rp, head, record := madeTree(t)
	err := refusedAfterCreate(rp, true, head, record, errors.New("boom"))
	runPrintedClose(t, err)
	assertGone(t, rp)
}

func TestWhenTheRollbacksCloseFailsThePrintedCommandsWork(t *testing.T) {
	rp, head, record := madeTree(t)
	spyRollbackClose(t, errors.New("the units will not stop"))
	err := refusedAfterCreate(rp, false, head, record, errors.New("boom"))
	if !strings.Contains(err.Error(), "sc keyholder-stop "+rp.SessionKey) {
		t.Errorf("no keyholder-stop: %v", err)
	}
	runPrintedClose(t, err)
	assertGone(t, rp)
}

func TestARollbackNeverTurnsTheNixGCOn(t *testing.T) {
	rp, head, record := madeTree(t)
	calls := spyRollbackClose(t, nil)
	_ = refusedAfterCreate(rp, false, head, record, errors.New("boom"))
	if calls.n != 1 || calls.override == nil || *calls.override {
		t.Errorf("closes = %d, nix gc override = %v, want one close with false", calls.n, calls.override)
	}
}

// keepsWithoutClosing asserts the rollback keeps the tree without so much as
// asking for a teardown, so no later check can cover for a missing guard.
func keepsWithoutClosing(t *testing.T, rp worktree.ResolvedPath, existed bool, head string, record session.State) {
	t.Helper()
	calls := spyRollbackClose(t, nil)
	err := refusedAfterCreate(rp, existed, head, record, errors.New("boom"))
	if calls.n != 0 {
		t.Errorf("the rollback asked to remove the tree: %v", err)
	}
	if _, serr := os.Stat(rp.AbsPath); serr != nil {
		t.Errorf("the worktree is gone: %v", serr)
	}
}

func TestRollbackKeepsAWorktreeThatExisted(t *testing.T) {
	rp, head, record := madeTree(t)
	keepsWithoutClosing(t, rp, true, head, record)
}

func TestRollbackKeepsAWorktreeOnAnAdoptedBranch(t *testing.T) {
	rp, head, record := madeTree(t)
	rp.ExistingBranch = rp.Branch
	keepsWithoutClosing(t, rp, false, head, record)
}

// With both main and master there is no default branch to count commits
// against, so only the HEAD check sees the commit.
func TestRollbackKeepsAMovedHEADWhereNoDefaultBranchResolves(t *testing.T) {
	rp, head, record := madeTree(t)
	current := gitIn(t, rp.RepoPath, "branch", "--show-current")
	for _, b := range []string{"main", "master"} {
		if b != current {
			gitIn(t, rp.RepoPath, "branch", b)
		}
	}
	gitIn(t, rp.AbsPath, "commit", "--allow-empty", "--no-verify", "-q", "-m", "work")
	if why, err := close.Blocker(rp.RepoPath, rp.AbsPath, rp.Branch); err != nil || why != "" {
		t.Fatalf("Blocker sees the commit (%q, %v): this case proves nothing", why, err)
	}
	keepsWithoutClosing(t, rp, false, head, record)
}

// A stash shelf entry shows in neither HEAD nor git status: only Blocker.
func TestRollbackKeepsAWorktreeWithAShelfEntry(t *testing.T) {
	rp, head, record := madeTree(t)
	if err := os.MkdirAll(filepath.Join(rp.AbsPath, ".spinclass", "stash", "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	keepsWithoutClosing(t, rp, false, head, record)
}

// status.showUntrackedFiles=no hides the file from close.Blocker.
func TestRollbackKeepsAnUntrackedFileGitIsToldNotToShow(t *testing.T) {
	rp, head, record := madeTree(t)
	gitIn(t, rp.RepoPath, "config", "status.showUntrackedFiles", "no")
	if err := os.WriteFile(filepath.Join(rp.AbsPath, "precious"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keepsWithoutClosing(t, rp, false, head, record)
}

func TestRollbackKeepsATreeGitCannotAnswerAbout(t *testing.T) {
	rp, head, record := madeTree(t)
	index := gitIn(t, rp.AbsPath, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err := os.WriteFile(index, []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	keepsWithoutClosing(t, rp, false, head, record)
}

// A failure after the pair started (here: the entry cannot be run) is rolled
// back like one before it.
func TestASpawnWhoseEntryCannotRunLeavesNothing(t *testing.T) {
	home, repoPath := newWorkerFixture(t, "[session-entry]\nspawn-entry = [\"/nonexistent/spawn-entry\", \"--\", \"{prompt}\"]\n")
	_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault)
	if err == nil || !strings.Contains(err.Error(), "nothing is left") {
		t.Fatalf("err = %v", err)
	}
	if wt, br := madeBySpawns(t, repoPath); len(wt) != 0 || len(br) != 0 {
		t.Errorf("worktrees %v, branches %v: want nothing", wt, br)
	}
}
