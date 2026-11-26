package spawn

import (
	"errors"
	"fmt"
	"io"
	"os"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
	"code.linenisgreat.com/spinclass/internal/delta/close"
)

// rollbackClose is the teardown a rollback runs; a seam for tests.
var rollbackClose = close.RunResolved

// refusedAfterCreate ends a spawn that failed after it had made the worker's
// worktree and before any worker was started: it removes what this spawn
// created, through the same teardown `sc close` runs, and says so. It removes
// nothing this spawn did not create (existed: the worktree was already there;
// rp.ExistingBranch: the branch was adopted), and nothing that holds work (a
// change, a commit, a stash) or that git cannot answer about. A tree it keeps
// gets record as its session record when it has none, so the `sc close` the
// refusal prints resolves.
func refusedAfterCreate(rp worktree.ResolvedPath, existed bool, head string, record session.State, cause error) error {
	if existed || rp.ExistingBranch != "" {
		return fmt.Errorf("%w\nthe worktree %s or its branch was there before this spawn and was left as it is; to remove it: %s",
			cause, rp.AbsPath, removeCommand(rp, record, ""))
	}
	kept := func(why string) error {
		return fmt.Errorf("%w\nthe spawn made the worktree %s and kept it (%s); look at it, then remove it with: %s",
			cause, rp.AbsPath, why, removeCommand(rp, record, " --force"))
	}
	if now, err := git.RevParse(rp.AbsPath, "HEAD"); err != nil || head == "" || now != head {
		return kept("its HEAD is not the commit this spawn made it at")
	}
	// close.Blocker reads a failing `git status` as clean and honors
	// status.showUntrackedFiles, so the changes are asked about here first.
	if changes, err := git.Run(rp.AbsPath, "status", "--porcelain", "--untracked-files=all"); err != nil {
		return kept("git could not say whether it holds changes: " + err.Error())
	} else if changes != "" {
		return kept("it has uncommitted changes")
	}
	if why, err := close.Blocker(rp.RepoPath, rp.AbsPath, rp.Branch); err != nil {
		return kept(err.Error())
	} else if why != "" {
		return kept(why)
	}
	// The worker never ran, so there is nothing of its to collect: false skips
	// the nix gc, and a rollback never turns it on over [hooks].disable-nix-gc.
	runNixGC := false
	if err := rollbackClose(io.Discard, rp.RepoPath, rp.AbsPath, rp.Branch, false, &runNixGC, ""); err != nil {
		return fmt.Errorf("%w\nthe spawn made the worktree %s and could not remove it (%v); run `sc keyholder-stop %s`, then remove it with: %s",
			cause, rp.AbsPath, err, rp.SessionKey, removeCommand(rp, record, " --force"))
	}
	return fmt.Errorf("%w\nnothing is left: the worktree and branch this spawn made were removed", cause)
}

// removeCommand is the command that removes a worktree the rollback keeps:
// `sc close`, which resolves a session record, so record is written when the
// spawn had not got as far as writing one. A record that exists and cannot be
// read is left as it is. Where `sc close` has no record to resolve (that one,
// or one that cannot be written) it is the plain git commands.
func removeCommand(rp worktree.ResolvedPath, record session.State, flags string) string {
	plain := fmt.Sprintf("git -C %s worktree remove --force %s && git -C %s branch -D %s", rp.RepoPath, rp.AbsPath, rp.RepoPath, rp.Branch)
	_, err := session.Read(rp.RepoPath, rp.Branch)
	if errors.Is(err, os.ErrNotExist) {
		record.SessionState = session.StateInactive
		err = session.Write(record)
	}
	if err != nil {
		return plain
	}
	return "sc close " + rp.SessionKey + flags
}
