package main

import (
	"fmt"

	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

// keyholderDriverRepoFn names the repo whose hierarchy is the DRIVER's own
// context (the repo containing the working directory, else home). A seam for
// tests.
var keyholderDriverRepoFn = spawn.DriverRepo

// keyholderSwitch is the key holder switch and the programs as a launch sees
// them.
type keyholderSwitch struct {
	On           bool
	Fibby, Piggy string
	// Untrusted are the repo- or worktree-level files of the TARGET that set a
	// [keyholder] key and were ignored.
	Untrusted []string
}

// resolveKeyholderSwitch reads the switch the way the two sibling switches are
// read ([profile-rights] enforce, [confinement]; spawn.go "the target repo path
// is caller-chosen, so its hierarchy alone cannot decide the switch"): the TARGET
// repo's hierarchy AND the DRIVER's own context, and the key holder is ON when
// either says so. So a spawn into a repo under a directory the caller controls,
// whose ancestor sweatfile says "off", cannot give the child the caller's agent;
// and a host that writes [keyholder] into one parent-directory layer does not
// lose it for a repo outside that directory while the driver sits inside it.
// Neither context can turn the other off (narrowing-only for the operator's
// switch: only turning it ON can come from the caller's side).
//
// Both are trusted layers only (global and strict ancestors of that context's
// repo); a repo or worktree layer is never honoured. The programs come from the
// target's trusted layers, else the driver's; when both set a program and they
// DISAGREE the launch is refused: a path chosen by the caller's directory must
// not be able to swap the program the pair runs. An unreadable or invalid
// hierarchy in either context is an error (fail closed, never the operator's
// agent).
func resolveKeyholderSwitch(home, repoPath string) (keyholderSwitch, error) {
	var sw keyholderSwitch
	th, err := sweatfileio.LoadHierarchy(home, repoPath)
	if err != nil {
		return sw, fmt.Errorf("reading the sweatfiles for [keyholder]: %w", err)
	}
	tOn, untrusted, err := sweatfileio.TrustedKeyholderMode(th, home, repoPath)
	sw.Untrusted = untrusted
	if err != nil {
		return sw, fmt.Errorf("%w (sessions are not launched until it is fixed: a wrong key holder switch must not fall back to the operator's agent)", err)
	}
	tFibby, tPiggy, _ := sweatfileio.TrustedKeyholderTools(th, home, repoPath)
	sw.On, sw.Fibby, sw.Piggy = tOn, tFibby, tPiggy

	driver := keyholderDriverRepoFn(home)
	if driver == "" || driver == repoPath {
		return sw, nil
	}
	dh, err := sweatfileio.LoadHierarchy(home, driver)
	if err != nil {
		return sw, fmt.Errorf("reading the driver's sweatfiles for [keyholder] (%s): %w", driver, err)
	}
	dOn, _, err := sweatfileio.TrustedKeyholderMode(dh, home, driver)
	if err != nil {
		return sw, fmt.Errorf("the driver's context (%s): %w (sessions are not launched until it is fixed: a wrong key holder switch must not fall back to the operator's agent)", driver, err)
	}
	dFibby, dPiggy, _ := sweatfileio.TrustedKeyholderTools(dh, home, driver)
	sw.On = sw.On || dOn
	for _, p := range []struct {
		name     string
		target   string
		driver   string
		resolved *string
	}{{"fibby", tFibby, dFibby, &sw.Fibby}, {"piggy", tPiggy, dPiggy, &sw.Piggy}} {
		switch {
		case p.target != "" && p.driver != "" && p.target != p.driver:
			if sw.On {
				return sw, fmt.Errorf("[keyholder] %s is %q for the target repo and %q for the driver's context (%s): the program the pair runs may not depend on which directory the caller chose; make the host's configuration write one value in a layer both see", p.name, p.target, p.driver, driver)
			}
		case p.target == "":
			*p.resolved = p.driver
		}
	}
	return sw, nil
}
