package session

import (
	"slices"
	"testing"
)

func TestIdentityEnvSplitsTheKeyIntoRepoAndBranch(t *testing.T) {
	got := IdentityEnv("myrepo/quiet-oak", "/repos/myrepo/.worktrees/quiet-oak", "a session")
	want := []string{
		"SPINCLASS_SESSION_ID=myrepo/quiet-oak",
		"SPINCLASS_REPO=myrepo",
		"SPINCLASS_BRANCH=quiet-oak",
		"SPINCLASS_WORKTREE=/repos/myrepo/.worktrees/quiet-oak",
		"SPINCLASS_DESCRIPTION=a session",
	}
	if !slices.Equal(got, want) {
		t.Errorf("IdentityEnv = %q, want %q", got, want)
	}
}

// A branch may itself contain slashes; only the first one separates the repo.
func TestIdentityEnvKeepsSlashesInTheBranch(t *testing.T) {
	got := IdentityEnv("myrepo/feat/x", "/wt", "")
	if !slices.Contains(got, "SPINCLASS_REPO=myrepo") || !slices.Contains(got, "SPINCLASS_BRANCH=feat/x") {
		t.Errorf("IdentityEnv = %q, want repo myrepo and branch feat/x", got)
	}
}

// A key with no slash is all repo and no branch.
func TestIdentityEnvKeyWithoutASlash(t *testing.T) {
	got := IdentityEnv("myrepo", "/wt", "")
	if !slices.Contains(got, "SPINCLASS_REPO=myrepo") || !slices.Contains(got, "SPINCLASS_BRANCH=") {
		t.Errorf("IdentityEnv = %q, want repo myrepo and an empty branch", got)
	}
}

func TestTmpDirEnvPointsAtTheWorktreeTmp(t *testing.T) {
	got := TmpDirEnv("/repos/myrepo/.worktrees/quiet-oak")
	want := []string{
		"TMPDIR=/repos/myrepo/.worktrees/quiet-oak/.tmp",
		"CLAUDE_CODE_TMPDIR=/repos/myrepo/.worktrees/quiet-oak/.tmp",
	}
	if !slices.Equal(got, want) {
		t.Errorf("TmpDirEnv = %q, want %q", got, want)
	}
}
