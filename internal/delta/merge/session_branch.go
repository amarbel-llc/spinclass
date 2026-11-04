package merge

import (
	"errors"
	"fmt"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// ErrForeignBranchCheckedOut is returned by SessionBranch when the session
// worktree has a branch checked out that is not the session's own. Such a
// branch (a session-owned <session>--* branch a grit_branch rebase leaves
// checked out, say) is not eligible for merging or for attestation
// (spinclass#463).
var ErrForeignBranchCheckedOut = errors.New("the worktree has a branch checked out that is not its session's branch")

// SessionBranch is the branch the worktree session at wtPath merges and
// attests: its record's branch, else (an untracked worktree) the worktree's
// directory name, the <repo>/.worktrees/<branch> layout spinclass creates. A
// session is keyed on its worktree, never on whatever git has checked out
// there, so when the checked-out branch differs the result wraps
// ErrForeignBranchCheckedOut and names both branches. Callers refuse on it
// before any attestation peek or claim, so the refusal burns nothing.
func SessionBranch(wtPath string) (string, error) {
	want := filepath.Base(wtPath)
	if st, err := session.FindByWorktreePath(wtPath); err == nil && !st.IsTombstone() && st.Branch != "" {
		want = st.Branch
	}
	got, err := git.BranchCurrent(wtPath)
	if err != nil {
		return "", fmt.Errorf("could not determine current branch: %w", err)
	}
	if got != want {
		return "", fmt.Errorf("%w: %s has %s checked out, and its session's branch is %s; "+
			"check %s out again (grit_branch checkout) before merging or attesting (spinclass#463)",
			ErrForeignBranchCheckedOut, wtPath, got, want, want)
	}
	return want, nil
}
