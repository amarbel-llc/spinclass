package merge

import (
	"os"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/landing"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// loadCommitHierarchy loads the sweatfile hierarchy with the repo layer taken
// from the sweatfile COMMITTED at sha (#300), never from any worktree on disk
// and never from the main checkout's live file. The global and parent layers
// stay live, the same shape as FDR 0031's base-tree read.
func loadCommitHierarchy(home, repoPath, sha string) (sweatfile.Hierarchy, error) {
	return sweatfileio.LoadCommittedHierarchy(home, repoPath, sha, shortSha(sha))
}

// trustedHierarchy is the hierarchy a merge's commands and policy come from:
// the repo layer as committed on the landing target's tip (the default branch,
// as last fetched), never a session's commits and never a live file a session
// could edit. A sweatfile change therefore takes effect for merges after it
// lands. ok is false when home does not resolve or the load fails; callers keep
// their existing graceful-degrade behaviour for that case.
func trustedHierarchy(repoPath, defaultBranch string, gitSync bool) (h sweatfile.Hierarchy, ok bool) {
	home, _ := os.UserHomeDir()
	if home == "" {
		return sweatfile.Hierarchy{}, false
	}
	ref := landing.ForMerge(repoPath, defaultBranch, gitSync).Ref()
	h, err := sweatfileio.LoadCommittedHierarchy(home, repoPath, ref, ref)
	if err != nil {
		return sweatfile.Hierarchy{}, false
	}
	return h, true
}
