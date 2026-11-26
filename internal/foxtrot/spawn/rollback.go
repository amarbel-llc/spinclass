package spawn

import (
	"fmt"
	"io"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
	"code.linenisgreat.com/spinclass/internal/delta/close"
)

// refusedAfterCreate ends a spawn that failed after it had made the worker's
// worktree and before any worker was started: it removes what this spawn
// created, through the same teardown `sc close` runs, and says so. It removes
// nothing this spawn did not create (existed: the worktree was already there;
// rp.ExistingBranch: the branch was adopted), and nothing that holds work (a
// change, a commit, a stash); then it names the command that does.
func refusedAfterCreate(rp worktree.ResolvedPath, existed bool, head string, cause error) error {
	closeCmd := "sc close " + rp.SessionKey
	if existed || rp.ExistingBranch != "" {
		return fmt.Errorf("%w\nthe worktree %s existed before this spawn and was left as it is; to remove it: %s", cause, rp.AbsPath, closeCmd)
	}
	left := func(why string) error {
		return fmt.Errorf("%w\nthe spawn made the worktree %s and could not remove it (%s); look at it, then remove it with: %s --force", cause, rp.AbsPath, why, closeCmd)
	}
	if now, err := git.RevParse(rp.AbsPath, "HEAD"); err != nil || head == "" || now != head {
		return left("its HEAD is not the commit this spawn made it at")
	}
	if why, err := close.Blocker(rp.RepoPath, rp.AbsPath, rp.Branch); err != nil {
		return left(err.Error())
	} else if why != "" {
		return left(why)
	}
	keepNixStore := true // the worker never ran, so it built nothing
	if err := close.RunResolved(io.Discard, rp.RepoPath, rp.AbsPath, rp.Branch, false, &keepNixStore, ""); err != nil {
		return left(err.Error())
	}
	return fmt.Errorf("%w\nnothing is left: the worktree and branch this spawn made were removed", cause)
}
