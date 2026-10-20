package spawn

import (
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// WillConfine reports whether a spawn-session call with this repo argument,
// issued from cwd, would launch a confined worker (FDR 0035). It is the
// permission layer's read-only mirror of LaunchDetached's decision: the target
// repo's trusted [confinement].launch-unit OR the driver's own context.
//
// It is for deciding whether the always-ask floor may yield (spinclass#382),
// so it fails the other way from the spawn: any doubt (an unresolvable target,
// an unreadable hierarchy, no repo to default to) is false, which keeps the
// prompt. It touches no clown, no worktree and no state.
func WillConfine(home, cwd, repo string) bool {
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
	return launchUnitOn(home, target) || (driver != "" && launchUnitOn(home, driver))
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

// launchUnitOn is the trusted [confinement].launch-unit for repo, false when
// the hierarchy cannot be read.
func launchUnitOn(home, repo string) bool {
	h, err := sweatfileio.LoadHierarchy(home, repo)
	if err != nil {
		return false
	}
	on, _, _ := sweatfileio.TrustedConfinement(h, home, repo)
	return on
}
