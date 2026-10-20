package close

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/store"
	gitstore "code.linenisgreat.com/spinclass/internal/store/git"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// newShelfCloseRepo is a repo whose tracked file exists before any worktree
// does, so a fresh worktree is integrated and clean, with .spinclass/ excluded
// the way a session worktree excludes it: the shelf alone must decide the close.
func newShelfCloseRepo(t *testing.T) string {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	testgit.MustInit(t, repoPath)
	mustCommitFile(t, repoPath, "tracked.txt", "base", "add tracked")
	exclude := filepath.Join(repoPath, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte(".spinclass/\n.worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := closeInteractive
	closeInteractive = func() bool { return false }
	t.Cleanup(func() { closeInteractive = orig })
	return repoPath
}

func shelveEntries(t *testing.T, wtPath string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if err := os.WriteFile(filepath.Join(wtPath, "tracked.txt"), []byte("edit "+string(rune('a'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := gitstore.NewShelf().Save(wtPath, store.ShelfSaveRequest{}); err != nil {
			t.Fatal(err)
		}
	}
}

func worktreeExists(wtPath string) bool {
	_, err := os.Stat(wtPath)
	return err == nil
}

func TestRunResolvedRefusesAWorktreeWithAShelf(t *testing.T) {
	cases := []struct {
		name    string
		entries int
		want    string
	}{
		{"one entry", 1, `Branch "feature-x" has 1 stash entry that would be lost with the worktree.`},
		{"several entries", 3, `Branch "feature-x" has 3 stash entries that would be lost with the worktree.`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := newShelfCloseRepo(t)
			wtPath := makeSession(t, repoPath, "feature-x")
			shelveEntries(t, wtPath, tc.entries)

			err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", false, boolPtr(false), "tap")
			if err == nil {
				t.Fatal("a worktree with stash entries must not close without --force")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "--force") {
				t.Errorf("error %q lacks %q and a --force hint", err, tc.want)
			}
			if !worktreeExists(wtPath) {
				t.Error("a refused close must leave the worktree")
			}
			if n, _ := gitstore.CountShelfEntries(wtPath); n != tc.entries {
				t.Errorf("a refused close must leave the shelf, has %d entries", n)
			}
		})
	}
}

func TestRunResolvedNamesBothDirtAndShelf(t *testing.T) {
	repoPath := newShelfCloseRepo(t)
	wtPath := makeSession(t, repoPath, "feature-x")
	shelveEntries(t, wtPath, 1)
	if err := os.WriteFile(filepath.Join(wtPath, "tracked.txt"), []byte("dirty again"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", false, boolPtr(false), "tap")
	want := `Branch "feature-x" has uncommitted changes. It also has 1 stash entry that would be lost with the worktree.`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v lacks %q", err, want)
	}
}

func TestRunResolvedForceClosesAShelvedWorktreeWithAWarning(t *testing.T) {
	repoPath := newShelfCloseRepo(t)
	wtPath := makeSession(t, repoPath, "feature-x")
	shelveEntries(t, wtPath, 2)

	var out bytes.Buffer
	if err := RunResolved(&out, repoPath, wtPath, "feature-x", true, boolPtr(false), "tap"); err != nil {
		t.Fatalf("forced close: %v", err)
	}
	if worktreeExists(wtPath) {
		t.Error("a forced close must remove the worktree")
	}
	for _, want := range []string{"discard stash shelf feature-x", "severity: warn", "2 stash entries"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("forced close output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunResolvedClosesWhenTheShelfIsEmpty(t *testing.T) {
	repoPath := newShelfCloseRepo(t)

	t.Run("never used", func(t *testing.T) {
		wtPath := makeSession(t, repoPath, "feature-x")
		if err := RunResolved(io.Discard, repoPath, wtPath, "feature-x", false, boolPtr(false), "tap"); err != nil {
			t.Fatalf("a clean worktree without a shelf must close: %v", err)
		}
		if worktreeExists(wtPath) {
			t.Error("worktree should be removed")
		}
	})

	t.Run("every entry dropped", func(t *testing.T) {
		wtPath := makeSession(t, repoPath, "feature-y")
		shelveEntries(t, wtPath, 1)
		if _, err := gitstore.NewShelf().Drop(wtPath, ""); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := RunResolved(&out, repoPath, wtPath, "feature-y", false, boolPtr(false), "tap"); err != nil {
			t.Fatalf("a drained shelf must not block the close: %v", err)
		}
		if strings.Contains(out.String(), "stash") {
			t.Errorf("a drained shelf must not be mentioned:\n%s", out.String())
		}
	})
}

func TestRunManyRefusesOnlyTheShelvedTarget(t *testing.T) {
	repoPath := newShelfCloseRepo(t)
	wtShelved := makeSession(t, repoPath, "alpha")
	wtClean := makeSession(t, repoPath, "bravo")
	shelveEntries(t, wtShelved, 1)

	var out bytes.Buffer
	err := RunMany(&out, []string{"alpha", "bravo"}, false, boolPtr(false), "tap", nil)
	if err == nil || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("the shelved target must fail the invocation and be named, got %v", err)
	}
	if !worktreeExists(wtShelved) {
		t.Error("the shelved worktree must survive")
	}
	if worktreeExists(wtClean) {
		t.Error("the clean target must still close")
	}
	for _, want := range []string{"not ok 1 - alpha", "1 stash entry", "ok 2 - bravo"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
