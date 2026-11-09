// Package regenerate is the regenerate phase of docs/plans/2026-11-09-sc-run-
// regenerate-under-new-lock.md: after a step bumps flake.lock, regenerate and
// format the tree under the NEW lock and commit the result as its own commit.
// This file is the trigger (R0a); the admission, the run and the commit come in
// later steps. It lives below both callers (sc run and the merge path).
package regenerate

import (
	"bytes"
	"fmt"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/landing"
)

// LockFile is the one path whose change triggers the phase.
const LockFile = "flake.lock"

// BaseRef is the ref the lock is compared against: the landed default branch
// (the remote-tracking ref when it resolves, else the local branch), the base a
// session is cut from and a merge lands on. ok is false when the repo has no
// default branch, in which case the phase cannot tell and does not run.
func BaseRef(repoPath string) (ref string, ok bool) {
	return landing.DefaultBranchRef(repoPath)
}

// LockChanged reports whether flake.lock at HEAD of wtPath differs from
// flake.lock at baseRef. Absent on one side and present on the other counts as
// a change; absent on both does not. It reads committed content only and
// evaluates nothing. wtPath must be the worktree root, as for git.FileAtRev.
func LockChanged(wtPath, baseRef string) (bool, error) {
	head, headFound, err := git.FileAtRev(wtPath, "HEAD", LockFile)
	if err != nil {
		return false, fmt.Errorf("read %s at HEAD: %w", LockFile, err)
	}
	base, baseFound, err := git.FileAtRev(wtPath, baseRef, LockFile)
	if err != nil {
		return false, fmt.Errorf("read %s at %s: %w", LockFile, baseRef, err)
	}
	if headFound != baseFound {
		return true, nil
	}
	return !bytes.Equal(head, base), nil
}
