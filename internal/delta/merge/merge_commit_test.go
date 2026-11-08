package merge

import (
	"strings"
	"testing"
)

// emptyTree is git's well-known empty tree, so an unrelated-history commit
// needs no files.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// mergeUnrelatedHistory commits work on the session branch, then merges two
// root-less upstream commits into it with --allow-unrelated-histories.
func mergeUnrelatedHistory(t *testing.T, wtPath string) {
	t.Helper()
	writeFile(t, wtPath, "work.txt", "w")
	runGit(t, wtPath, "add", "work.txt")
	runGit(t, wtPath, "commit", "-m", "work")
	up1 := runGit(t, wtPath, "commit-tree", emptyTree, "-m", "upstream one")
	up2 := runGit(t, wtPath, "commit-tree", emptyTree, "-p", up1, "-m", "upstream two")
	runGit(t, wtPath, "merge", "--allow-unrelated-histories", "-m", "merge upstream", up2)
}

// #468: a branch that already holds a merge commit and descends from the
// landing target is not rebased, so the merge and the upstream shas survive.
func TestPrepareMergeKeepsAMergeCommitWhenTheTargetIsAnAncestor(t *testing.T) {
	repoDir := setupRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wtPath := setupWorktree(t, repoDir, "feat")
	mergeUnrelatedHistory(t, wtPath)
	before := runGit(t, wtPath, "rev-parse", "HEAD")
	upstream := runGit(t, wtPath, "rev-parse", "HEAD^2")

	stream, err := prepareWithReporter(t, repoDir, wtPath, "feat")
	if err != nil {
		t.Fatalf("prepare: %v\n%s", err, stream)
	}
	if after := runGit(t, wtPath, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved from %s to %s: the merge was rewritten", before, after)
	}
	if got := runGit(t, wtPath, "rev-parse", "HEAD^2"); got != upstream {
		t.Errorf("upstream parent is %s, want %s", got, upstream)
	}
	if !strings.Contains(stream, "SKIP") {
		t.Errorf("the skipped rebase is not reported:\n%s", stream)
	}
}

// With a merge in range and a target that moved past the branch, the merge
// refuses rather than flatten the merge.
func TestPrepareMergeRefusesAMergeCommitWhenTheTargetMoved(t *testing.T) {
	repoDir := setupRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wtPath := setupWorktree(t, repoDir, "feat")
	mergeUnrelatedHistory(t, wtPath)
	writeFile(t, repoDir, "main.txt", "m")
	runGit(t, repoDir, "add", "main.txt")
	runGit(t, repoDir, "commit", "-m", "main moves")
	before := runGit(t, wtPath, "rev-parse", "HEAD")

	stream, err := prepareWithReporter(t, repoDir, wtPath, "feat")
	if err == nil {
		t.Fatalf("a moved target with a merge in range must refuse:\n%s", stream)
	}
	if !strings.Contains(err.Error(), "merge commit") {
		t.Errorf("refusal does not name the merge commit: %v", err)
	}
	if after := runGit(t, wtPath, "rev-parse", "HEAD"); after != before {
		t.Errorf("a refused merge moved HEAD from %s to %s", before, after)
	}
}
