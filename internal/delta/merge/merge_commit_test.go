package merge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
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

// mergeFixture is setupRepo plus an isolated state home. setupRepo points HOME
// at the repo's temp root, so no sweatfile of the machine running the tests is
// read; the global (above-repo) sweatfile is the one under that HOME.
func mergeFixture(t *testing.T) (repoDir, globalSweatfile string) {
	t.Helper()
	repoDir = setupRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return repoDir, filepath.Join(os.Getenv("HOME"), ".config", "spinclass", "sweatfile")
}

const optInSweatfile = "[hooks]\nallow-merge-commits = true\n"

func writeSweatfile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// #468: by default a merge commit between the landing target and the branch is
// refused (a rebase would flatten it), and nothing moves.
func TestPrepareMergeRefusesAMergeCommitByDefault(t *testing.T) {
	repoDir, _ := mergeFixture(t)
	wtPath := setupWorktree(t, repoDir, "feat")
	mergeUnrelatedHistory(t, wtPath)
	before := runGit(t, wtPath, "rev-parse", "HEAD")

	stream, err := prepareWithReporter(t, repoDir, wtPath, "feat")
	if err == nil {
		t.Fatalf("a merge commit in range must be refused by default:\n%s", stream)
	}
	if !strings.Contains(err.Error(), "merge commit") {
		t.Errorf("refusal does not name the merge commit: %v", err)
	}
	if after := runGit(t, wtPath, "rev-parse", "HEAD"); after != before {
		t.Errorf("a refused merge moved HEAD from %s to %s", before, after)
	}
}

// With [hooks].allow-merge-commits set in a layer above the repo, a branch that
// holds a merge commit and descends from the target lands as it is: the rebase
// is skipped and the merge and the upstream shas survive.
func TestPrepareMergeKeepsAMergeCommitWhenAllowedAndTheTargetIsAnAncestor(t *testing.T) {
	repoDir, global := mergeFixture(t)
	writeSweatfile(t, global, optInSweatfile)
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

// Even when allowed, a target that moved past a branch holding a merge commit
// refuses rather than flatten the merge.
func TestPrepareMergeRefusesAMergeCommitWhenAllowedButTheTargetMoved(t *testing.T) {
	repoDir, global := mergeFixture(t)
	writeSweatfile(t, global, optInSweatfile)
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

// The repo's own sweatfile is a layer the session worker controls, so the
// opt-in there is ignored: the merge is still refused.
func TestPrepareMergeIgnoresTheOptInInTheRepoLayer(t *testing.T) {
	repoDir, _ := mergeFixture(t)
	writeFile(t, repoDir, "sweatfile", optInSweatfile)
	runGit(t, repoDir, "add", "sweatfile")
	runGit(t, repoDir, "commit", "-m", "repo sweatfile opts in")
	wtPath := setupWorktree(t, repoDir, "feat")
	mergeUnrelatedHistory(t, wtPath)

	stream, err := prepareWithReporter(t, repoDir, wtPath, "feat")
	if err == nil {
		t.Fatalf("a repo-layer opt-in must not be honoured:\n%s", stream)
	}
	if !strings.Contains(err.Error(), "merge commit") {
		t.Errorf("refusal does not name the merge commit: %v", err)
	}
}

// The queued landing rebase replays the pinned commits onto a target that moved
// during the gate, and that replay flattens a merge commit just as the
// prepare-time rebase would. When the opt-in let a merge-bearing branch through
// prepare and the target then moves, the landing refuses with the integration
// conflict (re-merge) and lands nothing.
func TestFinishMergeRefusesToFlattenAMergeCommitWhenTheTargetMovesDuringTheGate(t *testing.T) {
	repoDir, global := mergeFixture(t)
	writeSweatfile(t, global, optInSweatfile)
	wtPath := setupWorktree(t, repoDir, "feat")
	mergeUnrelatedHistory(t, wtPath)

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	pinned, err := PrepareMerge(context.Background(), ts, repoDir, wtPath, "feat", "main", false, PostMergeOptions{})
	if err != nil {
		t.Fatalf("PrepareMerge: %v\n%s", err, buf.String())
	}
	writeFile(t, repoDir, "racing.txt", "r")
	runGit(t, repoDir, "add", "racing.txt")
	runGit(t, repoDir, "commit", "-m", "racing commit on main")
	raceSha := runGit(t, repoDir, "rev-parse", "main")

	_, err = FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feat", "main", pinned, false, true, nil, PostMergeOptions{})
	ts.Finish()
	if !errors.Is(err, ErrIntegrationConflict) {
		t.Fatalf("FinishMerge = %v, want ErrIntegrationConflict\n%s", err, buf.String())
	}
	if !strings.Contains(err.Error(), "merge commit") {
		t.Errorf("refusal does not name the merge commit: %v", err)
	}
	if tip := runGit(t, repoDir, "rev-parse", "main"); tip != raceSha {
		t.Errorf("main = %s, want the racing tip %s: nothing may land", tip, raceSha)
	}
	assertNoLandWorktrees(t, repoDir)
}

// A branch with no merge commit takes the unchanged rebase path whether or not
// the opt-in is set.
func TestPrepareMergeRebasesAMergeFreeBranchAsBefore(t *testing.T) {
	repoDir, _ := mergeFixture(t)
	wtPath := setupWorktree(t, repoDir, "feat")
	writeFile(t, wtPath, "work.txt", "w")
	runGit(t, wtPath, "add", "work.txt")
	runGit(t, wtPath, "commit", "-m", "work")
	writeFile(t, repoDir, "main.txt", "m")
	runGit(t, repoDir, "add", "main.txt")
	runGit(t, repoDir, "commit", "-m", "main moves")

	stream, err := prepareWithReporter(t, repoDir, wtPath, "feat")
	if err != nil {
		t.Fatalf("prepare: %v\n%s", err, stream)
	}
	if strings.Contains(stream, "SKIP") {
		t.Errorf("a merge-free branch must be rebased, not skipped:\n%s", stream)
	}
}
