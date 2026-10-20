package spawn

import (
	"errors"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// ConfineRequest is what a spawn explicitly asks for about confinement.
type ConfineRequest int

const (
	// ConfineDefault asks for nothing: the sweatfile's default-spawn decides.
	ConfineDefault ConfineRequest = iota
	// ConfineRequire (the `confined` parameter) asks for a confined worker. It
	// narrows, so it is honoured whenever the feature is on and an error when it
	// is off.
	ConfineRequire
	// ConfineOptOut (the `unconfined` parameter) opts out of default-spawn. The
	// permission floor always asks for it.
	ConfineOptOut
)

// ParseConfineRequest folds the spawn's two flags into one request; passing both
// is an error.
func ParseConfineRequest(confined, unconfined bool) (ConfineRequest, error) {
	switch {
	case confined && unconfined:
		return ConfineDefault, errors.New("confined and unconfined are mutually exclusive")
	case confined:
		return ConfineRequire, nil
	case unconfined:
		return ConfineOptOut, nil
	}
	return ConfineDefault, nil
}

// resolveConfined is the single confinement decision, shared by LaunchDetached
// and WillConfine so the permission layer cannot drift from the launch: confined
// iff the feature is enabled (in the target's or the driver's context) and
// either the spawn requests it or the default applies. The default is the
// target's OR the driver's default-spawn, and `unconfined` opts out of it.
// Requesting confinement with the feature off is an error, never a silent
// downgrade.
func resolveConfined(target, driver sweatfileio.ConfinementPolicy, req ConfineRequest) (bool, error) {
	enabled := target.Enabled || driver.Enabled
	if req == ConfineRequire && !enabled {
		return false, errors.New("cannot spawn confined: confinement is not enabled; " +
			"set [confinement] enabled = true in the global sweatfile or a sweatfile above the repo (FDR 0035)")
	}
	if !enabled {
		return false, nil
	}
	if req == ConfineRequire {
		return true, nil
	}
	return req != ConfineOptOut && (target.DefaultConfines() || driver.DefaultConfines()), nil
}

// WillConfine reports whether a spawn-session call with this repo argument and
// request, issued from cwd, would launch a confined worker (FDR 0035). It is the
// permission layer's read-only mirror of LaunchDetached's decision
// (resolveConfined over the target's and the driver's trusted [confinement]).
//
// It is for deciding whether the always-ask floor may yield (spinclass#382),
// so it fails the other way from the spawn: any doubt (an unresolvable target,
// an unreadable hierarchy, no repo to default to, a request the launch would
// refuse) is false, which keeps the prompt. It touches no clown, no worktree
// and no state.
func WillConfine(home, cwd, repo string, req ConfineRequest) bool {
	driver := repoFor(cwd)
	target := driver
	if repo != "" {
		var err error
		if target, err = ResolveRepo(home, repo); err != nil {
			return false
		}
	}
	if target == "" {
		return false
	}
	targetPolicy, ok := policyFor(home, target)
	if !ok {
		return false
	}
	var driverPolicy sweatfileio.ConfinementPolicy
	if driver != "" {
		if driverPolicy, ok = policyFor(home, driver); !ok {
			return false
		}
	}
	confined, err := resolveConfined(targetPolicy, driverPolicy, req)
	return err == nil && confined
}

// repoFor is the main-checkout root containing dir, or "" when there is none.
func repoFor(dir string) string {
	if dir == "" {
		return ""
	}
	root, err := git.DetectRepo(dir)
	if err != nil {
		return ""
	}
	return root
}

// policyFor is the trusted [confinement] policy for repo; ok is false when the
// hierarchy cannot be read.
func policyFor(home, repo string) (policy sweatfileio.ConfinementPolicy, ok bool) {
	h, err := sweatfileio.LoadHierarchy(home, repo)
	if err != nil {
		return policy, false
	}
	policy, _, _ = sweatfileio.TrustedConfinement(h, home, repo)
	return policy, true
}
