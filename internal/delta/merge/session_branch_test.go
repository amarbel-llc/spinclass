package merge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// A worktree session merges and attests its own branch: the record's branch,
// else the worktree directory name. Another branch checked out there, such as
// the session-owned <session>--* branch a grit_branch rebase leaves checked
// out, is not eligible and is refused by name.
func TestSessionBranch(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")

	t.Run("own branch, no record: the directory name", func(t *testing.T) {
		got, err := SessionBranch(wt)
		if err != nil || got != "feat" {
			t.Fatalf("SessionBranch = %q, %v; want feat", got, err)
		}
	})

	runGit(t, wt, "checkout", "-q", "-b", "feat--probe")

	t.Run("foreign branch, no record: refused against the directory name", func(t *testing.T) {
		_, err := SessionBranch(wt)
		if !errors.Is(err, ErrForeignBranchCheckedOut) {
			t.Fatalf("SessionBranch = %v, want ErrForeignBranchCheckedOut", err)
		}
		if !strings.Contains(err.Error(), "feat--probe") || !strings.Contains(err.Error(), " feat") {
			t.Errorf("refusal names neither the checked-out nor the session branch: %v", err)
		}
	})

	if err := session.Write(session.State{
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: wt,
		Branch:       "feat",
		SessionKey:   "myrepo/feat",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("foreign branch, record: refused against the record's branch", func(t *testing.T) {
		_, err := SessionBranch(wt)
		if !errors.Is(err, ErrForeignBranchCheckedOut) {
			t.Fatalf("SessionBranch = %v, want ErrForeignBranchCheckedOut", err)
		}
	})

	runGit(t, wt, "checkout", "-q", "feat")

	t.Run("own branch, record: the record's branch", func(t *testing.T) {
		got, err := SessionBranch(wt)
		if err != nil || got != "feat" {
			t.Fatalf("SessionBranch = %q, %v; want feat", got, err)
		}
	})
}

// A target-less merge from a session worktree with another branch checked out
// is refused before the hook runs or the upstream moves.
func TestRunRefusesAForeignCheckedOutBranch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdg-state"))
	t.Setenv("SPINCLASS_SESSION_ID", "")

	bare := filepath.Join(root, "upstream.git")
	runGit(t, root, "init", "--bare", "-b", "master", bare)
	checkout := filepath.Join(root, "checkout")
	runGit(t, root, "clone", bare, checkout)
	runGit(t, checkout, "config", "user.email", "test@test.com")
	runGit(t, checkout, "config", "user.name", "Test")
	marker := filepath.Join(root, "hook-ran.marker")
	if err := os.WriteFile(filepath.Join(checkout, "sweatfile"), []byte("[hooks]\npre-merge = \"touch "+marker+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "sweatfile")
	runGit(t, checkout, "commit", "-m", "initial")
	runGit(t, checkout, "push", "-u", "origin", "master")
	upstreamBefore := runGit(t, bare, "rev-parse", "master")

	wt := filepath.Join(checkout, ".worktrees", "feat")
	runGit(t, checkout, "worktree", "add", "-b", "feat", wt)
	if err := os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt, "add", "work.txt")
	runGit(t, wt, "commit", "-m", "work")
	runGit(t, wt, "checkout", "-q", "-b", "feat--probe")

	t.Chdir(wt)
	err := Run(&mockExecutor{}, "ndjson", "", true, PostMergeOptions{})
	if !errors.Is(err, ErrForeignBranchCheckedOut) {
		t.Fatalf("Run with feat--probe checked out = %v, want ErrForeignBranchCheckedOut", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Error("the pre-merge hook ran on a refused merge")
	}
	if got := runGit(t, bare, "rev-parse", "master"); got != upstreamBefore {
		t.Errorf("upstream master moved to %s on a refused merge", got)
	}
}
