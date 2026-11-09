package run

import (
	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/bravo/regenerate"
)

// regenerateStepName is the result point the phase reports under.
const regenerateStepName = "regenerate"

// reportRegenerate is the regenerate phase's R0a seam: it decides whether the
// step changed flake.lock and says so, and does nothing else yet (the
// admission, the run and the commit are later steps of the plan). A run whose
// lock did not change, or whose repo has no default branch to compare with,
// emits NOTHING, so the stream of every ordinary run is unchanged.
func reportRegenerate(ts *crap.TestStream, wtPath, repoPath string, off bool) {
	base, ok := regenerate.BaseRef(repoPath)
	if !ok {
		return
	}
	changed, err := regenerate.LockChanged(wtPath, base)
	if err != nil {
		ts.Skip(regenerateStepName, "could not compare "+regenerate.LockFile+" with "+base+": "+err.Error())
		return
	}
	if !changed {
		return
	}
	if off {
		ts.Skip(regenerateStepName, regenerate.LockFile+" changed against "+base+"; skipped by --no-regenerate")
		return
	}
	ts.Skip(regenerateStepName, regenerate.LockFile+" changed against "+base+"; regeneration is not built yet, nothing was regenerated")
}
