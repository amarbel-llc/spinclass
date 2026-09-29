package merge

import (
	"fmt"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// loadCommitHierarchy loads the sweatfile hierarchy with the worktree layer
// taken from the sweatfile COMMITTED at sha (#300), never from any worktree on
// disk. The global, parent and root-checkout layers stay live, the same shape
// as FDR 0031's base-tree read.
func loadCommitHierarchy(home, repoPath, sha string) (sweatfile.Hierarchy, error) {
	short := shortSha(sha)
	data, found, err := git.FileAtRev(repoPath, sha, "sweatfile")
	if err != nil {
		return sweatfile.Hierarchy{}, fmt.Errorf("read sweatfile at %s: %w", short, err)
	}
	return sweatfileio.LoadHierarchyWithLayer(
		home, repoPath, short+":sweatfile", data, found,
	)
}
