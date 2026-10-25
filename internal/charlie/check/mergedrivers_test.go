package check

import (
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// spinclass#324: `sc check` is the agent-CI surface, so it reports a merge
// driver the eventual merge would need but cannot find, instead of running
// the hook against a tree whose rebase is doomed to a silent-ours "conflict".
// The wiring is under test here (the refusal fires, the hook Phase never
// starts); the refusal's wording is pinned by the merge package.
func TestCheckRefusesWhenMergeDriverMissing(t *testing.T) {
	_, repoDir := setupRepo(t)
	testgit.MustSeedMergeDriverPath(t, repoDir, "gen.txt", "codegen-header")
	wtPath := filepath.Join(repoDir, ".worktrees", "feature")
	testgit.MustWorktreeAdd(t, repoDir, wtPath, "feature")
	testgit.MustDivergePath(t, repoDir, wtPath, "gen.txt")
	runGit(t, repoDir, "config", "--global", "merge.codegen-header.driver", "spinclass-test-absent-driver %O %A %B")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"true\"\n")

	_, recs, err := runCheck(t, wtPath)
	if err == nil {
		t.Fatal("check succeeded with a missing merge driver")
	}
	tr := singleTest(t, recs)
	if tr.OK || !strings.HasPrefix(tr.Description, "merge drivers feature") {
		t.Errorf("want a failing merge-drivers point, got %+v", tr)
	}
	for _, rec := range recs {
		if _, isPhase := rec.(ndjsoncrap.NodeStart); isPhase {
			t.Errorf("the pre-merge hook ran despite the refusal: %+v", rec)
		}
	}
}
