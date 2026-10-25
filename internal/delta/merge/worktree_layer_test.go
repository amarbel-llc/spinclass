package merge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A worker can write (and commit) its worktree's own `sweatfile`, so the merge
// must read its policy and commands from the global, ancestor and main-checkout
// layers only. These tests commit a hostile or corrupt sweatfile on the branch
// being merged and check none of it takes effect.

// worktreeLayerRepo commits repoBody as the main checkout's sweatfile, cuts a
// feature worktree, and commits branchBody as the branch's sweatfile (its own
// worktree layer, also present in the pre-merge build worktree).
func worktreeLayerRepo(t *testing.T, repoBody, branchBody string) (repoDir, wtPath string) {
	t.Helper()
	repoDir = setupRepo(t)
	commitSweatfile(t, repoDir, repoBody)
	wtPath = setupWorktree(t, repoDir, "feature")
	commitSweatfile(t, wtPath, branchBody)
	return repoDir, wtPath
}

func TestMergeIgnoresWorktreeSweatfileCommands(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "SENTINEL")
	repoHook := filepath.Join(root, "repo-pre-merge-ran")
	repoDir, wtPath := worktreeLayerRepo(t,
		"[hooks]\npre-merge = \"touch "+repoHook+"\"\n",
		"[hooks]\npre-merge = \"touch "+sentinel+"\"\nrepair = \"touch "+sentinel+"\"\ndisable-merge = true\n")

	if _, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("a command named in the worktree sweatfile ran (pre-merge or repair)")
	}
	if _, err := os.Stat(repoHook); err != nil {
		t.Errorf("the main checkout's pre-merge hook did not run: %v", err)
	}
}

// disable-merge in the repo sweatfile still refuses the merge when the
// worktree's own sweatfile is corrupt: the unreadable file used to fail the
// hierarchy load, and the gate was skipped.
func TestMergeDisableMergeSurvivesCorruptWorktreeSweatfile(t *testing.T) {
	repoDir, wtPath := worktreeLayerRepo(t,
		"[hooks]\ndisable-merge = true\n",
		"[hooks\n=== not toml\n")

	_, _, err := runPrepare(t, repoDir, wtPath, "feature")
	if err == nil || !strings.Contains(err.Error(), "merge disabled") {
		t.Fatalf("err = %v, want the repo's disable-merge to refuse the merge", err)
	}
}

// A worktree sweatfile cannot disable the merge either.
func TestMergeWorktreeSweatfileCannotDisableMerge(t *testing.T) {
	repoDir, wtPath := worktreeLayerRepo(t,
		"[hooks]\npre-merge = \"true\"\n",
		"[hooks]\ndisable-merge = true\n")
	if _, _, err := runPrepare(t, repoDir, wtPath, "feature"); err != nil {
		t.Fatalf("a worktree disable-merge took effect: %v", err)
	}
}

// [[pre-merge-skills]] from the repo sweatfile stay live (the policy stage
// records the terminal bypass) when the worktree's sweatfile is corrupt; the
// unreadable file used to zero the hierarchy and drop the gate silently.
func TestMergePreMergeSkillsSurviveCorruptWorktreeSweatfile(t *testing.T) {
	repoDir, wtPath := worktreeLayerRepo(t,
		"[[pre-merge-skills]]\nname = \"review\"\nrationale = \"Mandatory.\"\n",
		"[hooks\n=== not toml\n")

	recs, err := runFinishOpts(t, repoDir, wtPath, "feature", false, PostMergeOptions{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	tr := policyPoint(t, recs)
	if tr.Directive == nil || tr.Directive.Reason != PolicyTerminalBypassReason {
		t.Errorf("policy point = %+v, want the terminal bypass: the repo's pre-merge-skills must stay live", tr)
	}
}
