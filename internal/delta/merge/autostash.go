package merge

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/store"
	gitstore "code.linenisgreat.com/spinclass/internal/alfa/store/git"
)

// autostashMessage labels the shelf entries a merge makes, so a kept one is
// recognisable in `grit_stash list`.
const autostashMessage = "spinclass merge autostash (#416)"

// autostash shelves the session worktree's uncommitted tracked changes into its
// own stash shelf (FDR 0036 section 4; never git's shared stack) and returns
// the restore to defer. Untracked files are left where they are: they do not
// block the rebase. A clean worktree emits nothing and restores nothing.
func autostash(ts *crap.TestStream, wtPath, branch string) (restore func(), err error) {
	dirty, err := git.DirtyTrackedPaths(wtPath)
	if err != nil {
		return nil, failStep(ts, "autostash "+branch, err, "")
	}
	if len(dirty) == 0 {
		return func() {}, nil
	}
	// The shelf brings an intent-to-add file back untracked, which a nix build
	// of the dirty tree would then miss (#418); mark them again after restore.
	intentToAdd, err := git.IntentToAddPaths(wtPath)
	if err != nil {
		return nil, failStep(ts, "autostash "+branch, err, "")
	}
	shelf := gitstore.NewShelf()
	entry, err := shelf.Save(wtPath, store.ShelfSaveRequest{Message: autostashMessage})
	if err != nil {
		return nil, failStep(ts, "autostash "+branch, err, "")
	}
	ts.Ok(fmt.Sprintf("autostash %s: %s as stash entry %d", branch, describePaths(dirty), entry.ID))
	return func() { restoreAutostash(ts, shelf, wtPath, branch, entry.ID, dirty, intentToAdd) }, nil
}

// restoreAutostash applies the merge's own shelf entry (by ID, so an entry made
// meanwhile is never taken for it) and drops it. A conflicting apply keeps the
// entry and says how to recover; the merge goes on, since the commit it lands
// is already pinned or the merge has already failed for its own reason.
func restoreAutostash(ts *crap.TestStream, shelf gitstore.Shelf, wtPath, branch string, id int, paths, intentToAdd []string) {
	label := "restore autostash " + branch
	ref := strconv.Itoa(id)
	if _, err := shelf.Apply(wtPath, ref); err != nil {
		var conflict *store.ShelfConflictError
		detail := err.Error()
		if errors.As(err, &conflict) {
			detail = "conflicts in " + strings.Join(conflict.Paths, ", ")
		}
		ts.Skip(label, fmt.Sprintf("could not restore %s: %s; your changes are kept as stash entry %d: resolve the conflict, then grit_stash drop stash_ref=%d, or grit_stash apply stash_ref=%d on a clean tree",
			describePaths(paths), detail, id, id, id))
		return
	}
	if _, err := shelf.Drop(wtPath, ref); err != nil {
		ts.Skip(label, fmt.Sprintf("restored %s but could not drop stash entry %d: %v; grit_stash drop stash_ref=%d", describePaths(paths), id, err, id))
		return
	}
	if len(intentToAdd) > 0 {
		if _, err := git.Run(wtPath, append([]string{"add", "-N", "--"}, intentToAdd...)...); err != nil {
			ts.Skip(label, fmt.Sprintf("restored %s but could not mark %s intent-to-add again: %v; git add -N them, or a nix build of this tree will miss them",
				describePaths(paths), strings.Join(intentToAdd, ", "), err))
			return
		}
	}
	ts.Ok(label + ": " + describePaths(paths))
}

// describePaths names how many tracked files and which, never their contents.
func describePaths(paths []string) string {
	noun := "files"
	if len(paths) == 1 {
		noun = "file"
	}
	return fmt.Sprintf("%d tracked %s (%s)", len(paths), noun, strings.Join(paths, ", "))
}
