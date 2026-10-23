package main

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// The attestation gate's reader (peekGate / holdGate / nothing-but-the-truth go
// through mergedSweatfileForDir) must take the repo layer from the sweatfile
// COMMITTED on the default branch, so neither the session branch's commits nor
// a live edit to the main checkout's sweatfile can drop a required skill.
func TestGateSkillsComeFromCommittedDefaultBranch(t *testing.T) {
	const required = "[[pre-merge-skills]]\nname = \"review\"\nrationale = \"Mandatory.\"\n"
	const dropped = "[hooks]\n"

	setup := func(t *testing.T) (repoDir, wtPath string) {
		t.Helper()
		testgit.RequireGit(t)
		t.Setenv("HOME", t.TempDir())
		repoDir = filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		testgit.MustInit(t, repoDir)
		testgit.MustWriteFile(t, repoDir, "sweatfile", required)
		testgit.MustGit(t, repoDir, "add", "sweatfile")
		testgit.MustGit(t, repoDir, "commit", "-m", "default branch sweatfile")
		wtPath = filepath.Join(repoDir, ".worktrees", "feature")
		testgit.MustWorktreeAdd(t, repoDir, wtPath, "feature")
		return repoDir, wtPath
	}
	assertStillRequired := func(t *testing.T, wtPath string) {
		t.Helper()
		merged, ok := mergedSweatfileForDir(wtPath)
		if !ok {
			t.Fatal("hierarchy did not load")
		}
		if got := merged.ActivePreMergeSkills(); len(got) != 1 || got[0].Name != "review" {
			t.Errorf("ActivePreMergeSkills = %+v, want the committed default branch's [review]", got)
		}
	}

	t.Run("session commit drops the skill", func(t *testing.T) {
		_, wtPath := setup(t)
		testgit.MustWriteFile(t, wtPath, "sweatfile", dropped)
		testgit.MustGit(t, wtPath, "add", "sweatfile")
		testgit.MustGit(t, wtPath, "commit", "-m", "drop skill")
		assertStillRequired(t, wtPath)
	})

	t.Run("live edit drops the skill", func(t *testing.T) {
		repoDir, wtPath := setup(t)
		testgit.MustWriteFile(t, repoDir, "sweatfile", dropped)
		assertStillRequired(t, wtPath)
	})
}
