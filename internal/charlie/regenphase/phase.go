// Package regenphase is the regenerate phase of docs/plans/2026-11-09-sc-run-
// regenerate-under-new-lock.md, shared by its two callers: `sc run` (after the
// step) and the merge path (in PrepareMerge, before REPAIR). When a branch
// changed flake.lock against the landed default branch, the phase admits the
// committed lock with the guarded gate's lock policy, runs the repo's repair or
// pre-commit hook in the worktree under the new lock, and commits whatever it
// changed as one separate commit.
//
// It sits below both callers, so the lock-policy admission is passed in (the
// callers hold check.AdmitLockForRegenerate) rather than imported.
package regenphase

import (
	"context"
	"path/filepath"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/bravo/regenerate"
)

// StepName is the result point the phase reports under.
const StepName = "regenerate"

// AdmitFunc is the lock-policy admission: it judges the committed flake.nix
// and flake.lock at HEAD of dir, with forbidden the directories the worker
// writes. A non-nil error means nothing may be evaluated at dir.
type AdmitFunc func(ctx context.Context, dir string, forbidden []string) error

// Phase decides whether the branch changed flake.lock against base (the landed
// default branch's ref; a branch with no base to compare with is the caller's
// to skip); if so, and off is false, it admits the lock and runs the hook (see
// run). sf is the merged sweatfile: [hooks].repair, else [hooks].pre-commit.
// A branch whose lock did not change emits NOTHING, so the stream of every
// ordinary run or merge is unchanged. A non-nil error is a refusal or a hook
// failure already reported on ts: the caller fails and leaves the worktree for
// inspection.
func Phase(ctx context.Context, ts *crap.TestStream, sf sweatfile.Sweatfile, wtPath, base string, off bool, admit AdmitFunc) error {
	changed, err := regenerate.LockChanged(wtPath, base)
	if err != nil {
		ts.Skip(StepName, "could not compare "+regenerate.LockFile+" with "+base+": "+err.Error())
		return nil
	}
	if !changed {
		return nil
	}
	if off {
		ts.Skip(StepName, regenerate.LockFile+" changed against "+base+"; skipped by --no-regenerate")
		return nil
	}
	// The directories the worker writes: the session worktree and every
	// worktree of the repo, as the guarded gate lists them. The scrubbed
	// frontend removes what lies under them instead of refusing (see
	// hookrun.ConfinedGateEnvScrubbed).
	forbidden := []string{wtPath}
	if common, cErr := git.CommonDir(wtPath); cErr == nil {
		forbidden = append(forbidden, filepath.Join(common, ".worktrees"))
	}
	if err := admit(ctx, wtPath, forbidden); err != nil {
		return failStep(ts, StepName, err,
			"pass --no-regenerate to run without the phase; nothing was evaluated")
	}
	return run(ctx, ts, sf, wtPath, base)
}

// failStep emits a failing test point for label populated from err and returns
// err unchanged, the shape check.FailStep has (not imported: it is a level up).
func failStep(ts *crap.TestStream, label string, err error, output string) error {
	diag := map[string]any{"severity": "fail", "message": err.Error()}
	if output != "" {
		diag["output"] = output
	}
	ts.NotOk(label, diag)
	return err
}
