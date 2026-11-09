package run

import (
	"context"
	"path/filepath"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/bravo/regenerate"
	"code.linenisgreat.com/spinclass/internal/charlie/check"
)

// regenerateStepName is the result point the phase reports under.
const regenerateStepName = "regenerate"

// admitLock is the lock-policy admission (R0b), a seam so a test needs neither
// nix nor systemd.
var admitLock = check.AdmitLockForRegenerate

// regeneratePhase is the regenerate phase so far (R0a, R0b): decide whether the
// step changed flake.lock; if so, and the phase is on, admit the committed
// lock with the guarded gate's policy before anything evaluates it. The run of
// the new generators and the separate commit are later steps of the plan. A
// run whose lock did not change, or whose repo has no default branch to compare
// with, emits NOTHING, so the stream of every ordinary run is unchanged. A
// non-nil error is a refusal: the policy found the lock unsafe to evaluate (or
// could not tell), the run fails and the worktree is left for inspection.
func regeneratePhase(ctx context.Context, ts *crap.TestStream, wtPath, repoPath string, off bool) error {
	base, ok := regenerate.BaseRef(repoPath)
	if !ok {
		return nil
	}
	changed, err := regenerate.LockChanged(wtPath, base)
	if err != nil {
		ts.Skip(regenerateStepName, "could not compare "+regenerate.LockFile+" with "+base+": "+err.Error())
		return nil
	}
	if !changed {
		return nil
	}
	if off {
		ts.Skip(regenerateStepName, regenerate.LockFile+" changed against "+base+"; skipped by --no-regenerate")
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
	if err := admitLock(ctx, wtPath, forbidden); err != nil {
		return check.FailStep(ts, regenerateStepName, err,
			"pass --no-regenerate to run without the phase; nothing was evaluated")
	}
	ts.Skip(regenerateStepName, regenerate.LockFile+" changed against "+base+" and the lock policy admitted it; the generators are not run yet (later steps of the plan)")
	return nil
}
