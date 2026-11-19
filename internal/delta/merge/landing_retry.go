package merge

import (
	"errors"

	"code.linenisgreat.com/spinclass/internal/0/git"
)

// landingRaceRetries bounds how often a queued merge whose landing push lost a
// race retries in place, under its lock: two retries, so at most three gates.
// The merge queue is host-local (FDR 0022), so a merge from another clone can
// land while this one's gate runs; each retry re-fetches, rebases, re-gates the
// exact new commit and pushes, instead of failing back to the queue.
const landingRaceRetries = 2

// ErrLandingRaceExhausted is returned when the landing target moved during
// every gate of a merge, past landingRaceRetries retries. Nothing landed; a
// re-merge is a plain retry.
var ErrLandingRaceExhausted = errors.New("landing target kept moving during the gate")

// landingLostRace reports whether a refused landing push lost a race: the
// target tip moved since the attempt began (tipBefore → tipAfter) and the
// commit that was pushed does not already contain the new tip. A refusal that
// leaves the tip where it was (a dropped credential, a bad URL) is not a race,
// and neither is an unknown tip.
func landingLostRace(repoPath, tipBefore, tipAfter, landingSha string) bool {
	return tipAfter != "" && tipAfter != tipBefore && !git.IsAncestor(repoPath, tipAfter, landingSha)
}
