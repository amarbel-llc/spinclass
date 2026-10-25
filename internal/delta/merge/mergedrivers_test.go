package merge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// setupMissingDriverRepo builds a repo whose gen.txt is bound to a merge
// driver that is NOT on PATH, with gen.txt changed on both main and branch —
// the spinclass#324 shape: the rebase would "conflict" with the ours content
// and no markers.
func setupMissingDriverRepo(t *testing.T, branch string) (repoDir, wtPath string) {
	t.Helper()
	repoDir = setupRepo(t)
	testgit.MustSeedMergeDriverPath(t, repoDir, "gen.txt", "codegen-header")
	// Global config, as the reporter had it; the worktree inherits it.
	runGit(t, repoDir, "config", "--global", "merge.codegen-header.driver", "spinclass-test-absent-driver %O %A %B %L %P")
	wtPath = setupWorktree(t, repoDir, branch)
	testgit.MustDivergePath(t, repoDir, wtPath, "gen.txt")
	return repoDir, wtPath
}

// spinclass#324: PrepareMerge refuses BEFORE the rebase starts when a merge
// driver the rebase would invoke is not on PATH, naming the driver and the
// remedy, and leaves the worktree untouched (no rebase in progress).
func TestPrepareMergeRefusesWhenMergeDriverMissing(t *testing.T) {
	repoDir, wtPath := setupMissingDriverRepo(t, "feature")
	head := runGit(t, wtPath, "rev-parse", "HEAD")

	_, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err == nil {
		t.Fatalf("PrepareMerge succeeded with a missing merge driver; points: %v", testDescs(tests))
	}
	tr, ok := findTest(tests, "merge drivers feature")
	if !ok {
		t.Fatalf("no merge-drivers point in %v", testDescs(tests))
	}
	if tr.OK {
		t.Errorf("merge-drivers point passed: %+v", tr)
	}
	for _, want := range []string{"codegen-header", "spinclass-test-absent-driver", "not on PATH", "gen.txt", "merge.codegen-header.driver"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
	if _, rebased := findTest(tests, "rebase feature"); rebased {
		t.Errorf("rebase point emitted; the pre-flight must run before the rebase: %v", testDescs(tests))
	}
	if got := runGit(t, wtPath, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved %s → %s; the worktree must be left untouched", head, got)
	}
	if st := runGit(t, wtPath, "status", "--porcelain"); st != "" {
		t.Errorf("worktree dirty after the refusal:\n%s", st)
	}
	if _, statErr := os.Stat(filepath.Join(repoDir, ".git", "worktrees", "feature", "rebase-merge")); statErr == nil {
		t.Error("a rebase was left in progress")
	}
}

// The driver resolving on PATH lets the merge proceed (and the rebase then
// really runs it).
func TestPrepareMergeProceedsWhenMergeDriverPresent(t *testing.T) {
	repoDir, wtPath := setupMissingDriverRepo(t, "feature")
	// A driver that resolves by taking "theirs" (%B), so the rebase succeeds.
	testgit.MustPutOnPath(t, "spinclass-test-absent-driver", "#!/bin/sh\ncp \"$3\" \"$2\"\n")

	_, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err != nil {
		t.Fatalf("PrepareMerge: %v; points: %v", err, testDescs(tests))
	}
	tr, ok := findTest(tests, "merge drivers feature")
	if !ok || !tr.OK {
		t.Fatalf("want an ok merge-drivers point naming the driver, got %+v in %v", tr, testDescs(tests))
	}
}
