package hooks

import (
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testfs"
)

// policyFixture lays out a main checkout with a repo sweatfile and a linked
// worktree (the on-disk shape `git worktree add` produces) holding wtBody as its
// own sweatfile.
func policyFixture(t *testing.T, repoBody, wtBody string) (home, repo, wt string) {
	t.Helper()
	home = t.TempDir()
	repo = filepath.Join(home, "work", "repo")
	wt = filepath.Join(repo, ".worktrees", "x")
	gitdir := filepath.Join(repo, ".git", "worktrees", "x")
	testfs.MustMkdirAll(t, gitdir, 0o755)
	testfs.MustWriteFile(t, filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644)
	testfs.MustMkdirAll(t, wt, 0o755)
	testfs.MustWriteFile(t, filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644)
	testfs.MustWriteFile(t, filepath.Join(repo, "sweatfile"), []byte(repoBody), 0o644)
	testfs.MustWriteFile(t, filepath.Join(wt, "sweatfile"), []byte(wtBody), 0o644)
	return home, repo, wt
}

const repoPolicy = "[hooks]\ndisallow-main-worktree = true\n"

// A corrupt worktree sweatfile must not weaken the policy resolved from the
// main checkout (it used to error the load, and the caller fell back to the
// weakest policy).
func TestResolveWorktreePolicyCorruptWorktreeSweatfile(t *testing.T) {
	home, repo, _ := policyFixture(t, repoPolicy, "[hooks\n=== not toml\n")
	got := resolveWorktreePolicy(home, repo)
	if !got.disallowMainWorktree {
		t.Error("disallow-main-worktree from the repo sweatfile was lost")
	}
	if !got.rewriteEnabled {
		t.Error("worktree path rewrite should stay on by default")
	}
}

// A worktree sweatfile that tries to relax the policy has no effect.
func TestResolveWorktreePolicyWorktreeCannotWeaken(t *testing.T) {
	home, repo, _ := policyFixture(t, repoPolicy,
		"[hooks]\ndisallow-main-worktree = false\ndisable-worktree-path-rewrite = true\n")
	got := resolveWorktreePolicy(home, repo)
	if !got.disallowMainWorktree {
		t.Error("the worktree sweatfile turned disallow-main-worktree off")
	}
	if !got.rewriteEnabled {
		t.Error("the worktree sweatfile turned the path rewrite off")
	}
}

// deny-sweatfile-edits is read from the trusted layers and survives a corrupt
// worktree sweatfile.
func TestDenySweatfileEditsSurvivesCorruptWorktreeSweatfile(t *testing.T) {
	home, repo, _ := policyFixture(t, "", "[permissions\n=== not toml\n")
	testfs.MustMkdirAll(t, filepath.Join(home, ".config", "spinclass"), 0o755)
	testfs.MustWriteFile(t, filepath.Join(home, ".config", "spinclass", "sweatfile"),
		[]byte("[permissions]\ndeny-sweatfile-edits = true\n"), 0o644)
	if !resolveWorktreePolicy(home, repo).denySweatfileEdits {
		t.Error("deny-sweatfile-edits lost to a corrupt worktree sweatfile")
	}
}
