package pull

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// driverWorktree builds repo with a worktree at .worktrees/feature whose
// gen.txt, bound to merge=gen, changed on both sides: a rebase of the
// worktree onto main three-way merges it through the gen driver.
func driverWorktree(t *testing.T, driverCommand string) (repo, wt string) {
	t.Helper()
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	repo = filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repo)
	testgit.MustSeedMergeDriverPath(t, repo, "gen.txt", "gen")
	testgit.MustGit(t, repo, "config", "merge.gen.driver", driverCommand)
	wt = filepath.Join(repo, ".worktrees", "feature")
	testgit.MustWorktreeAdd(t, repo, wt, "feature")
	testgit.MustDivergePath(t, repo, wt, "gen.txt")
	return repo, wt
}

func runPull(t *testing.T, startDir string) string {
	t.Helper()
	var out bytes.Buffer
	_ = run(&out, startDir, false)
	return out.String()
}

// spinclass#333: a bound driver that is not on PATH refuses the worktree's
// rebase before it starts, naming the driver, instead of letting git keep the
// ours side silently.
func TestPullRefusesARebaseWhoseMergeDriverIsMissing(t *testing.T) {
	repo, wt := driverWorktree(t, "spinclass-test-absent-driver %O %A %B")
	head := testgit.MustGit(t, wt, "rev-parse", "HEAD")

	out := runPull(t, repo)
	if !strings.Contains(out, "not ok") || !strings.Contains(out, "spinclass-test-absent-driver") || !strings.Contains(out, "not on PATH") {
		t.Fatalf("want a failing point naming the absent driver, got:\n%s", out)
	}
	if got := testgit.MustGit(t, wt, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved %s -> %s; the rebase must not start", head, got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "worktrees", "feature", "rebase-merge")); err == nil {
		t.Error("a rebase was left in progress")
	}
}

// A driver the pre-flight cannot judge (an env-assignment prefix) reaches the
// rebase; its failure says the "conflict" is a missing driver, not a content
// conflict (the #334 explanation, for sc pull).
func TestPullExplainsADriverNotFoundBehindARebaseConflict(t *testing.T) {
	repo, wt := driverWorktree(t, "SPINCLASS_TEST=1 spinclass-test-absent-driver %O %A %B")
	t.Cleanup(func() { _, _ = git.Run(wt, "rebase", "--abort") })

	out := runPull(t, repo)
	for _, want := range []string{"not ok", "spinclass-test-absent-driver: command not found", "not a content conflict"} {
		if !strings.Contains(out, want) {
			t.Errorf("pull output lacks %q:\n%s", want, out)
		}
	}
}
