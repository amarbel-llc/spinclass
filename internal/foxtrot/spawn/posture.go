package spawn

import (
	"fmt"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// Posture is a spawn's requested permission posture (spinclass#474, operator
// rulings 2026-11-10 and 2026-11-11). PostureDefault leaves the trusted
// [session-entry].unattended-permission-mode in charge, as before.
// PostureUnattended is a narrowing: the worker runs in claude's dontAsk mode
// with the CLOWN_UNATTENDED marker, whatever the key says. PostureAttended is a
// widening: the worker runs without dontAsk, so it waits for a human to answer
// its prompts. It is refused for a confined worker and needs the trusted
// allow-posture-widening key, and the permission floor asks a human for it on
// every call.
type Posture string

const (
	PostureDefault    Posture = ""
	PostureUnattended Posture = "unattended"
	PostureAttended   Posture = "attended"
)

// ParsePosture maps the spawn surfaces' `posture` value to a Posture; "" is the
// default and anything other than unattended or attended is an error.
func ParsePosture(s string) (Posture, error) {
	switch p := Posture(s); p {
	case PostureDefault, PostureUnattended, PostureAttended:
		return p, nil
	}
	return PostureDefault, fmt.Errorf("posture %q is not supported; use unattended or attended, or omit it for the sweatfile's default", s)
}

// LaunchOptions is what a spawn explicitly asks for beyond its brief, model and
// profile: confinement and posture.
type LaunchOptions struct {
	Confine ConfineRequest
	Posture Posture
}

// postureMode is the unattended permission mode a spawn applies: the trusted
// key's mode for the default posture, dontAsk for unattended, and none (no
// dontAsk) for attended, which first needs the trusted allow-posture-widening
// key. Nothing is created before this runs, so a refusal litters nothing.
func postureMode(posture Posture, trustedMode string, h sweatfile.Hierarchy, home, repoRoot string) (string, error) {
	switch posture {
	case PostureUnattended:
		return sweatfile.UnattendedModeDontAsk, nil
	case PostureAttended:
		if allowed, _ := sweatfileio.TrustedAllowPostureWidening(h, home, repoRoot); !allowed {
			return "", fmt.Errorf("spawn refused: an attended worker widens the permissions spawns get (out of dontAsk), " +
				"and widening is not allowed: an operator must set `allow-posture-widening = true` under [session-entry] in the " +
				"global sweatfile or one above the repo (a repo or worktree sweatfile is ignored). It always asks a human even then")
		}
		return "", nil
	}
	return trustedMode, nil
}

// refuseConfinedAttended refuses an attended posture for a confined worker: no
// operator sits at a unit's tty (FDR 0035 F8a), so it always runs dontAsk.
func refuseConfinedAttended(confined bool, posture Posture) error {
	if confined && posture == PostureAttended {
		return fmt.Errorf("spawn refused: a confined worker always runs unattended (dontAsk), since no operator sits at an agent unit's terminal; " +
			"spawn it unconfined to make it attended")
	}
	return nil
}

// pinnedPostureOf is the posture recorded in the worker's pinned entry: what
// the launch actually applied, not what was asked.
func pinnedPostureOf(unattended bool) string {
	if unattended {
		return string(PostureUnattended)
	}
	return string(PostureAttended)
}
