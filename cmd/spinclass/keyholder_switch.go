package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/0/trustedhome"
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
//
// A failure of a source this feature added (the account database's home, the
// layers under it, the driver's own context) refuses only with evidence the
// switch is or was on for this host; otherwise it is warned once and the launch
// proceeds as before (keyholderNewSourceFailure). A [keyholder] table that does
// not parse or validate, and the target repo's own hierarchy not loading, still
// refuse: the first names the key holder, the second refused before it existed.
func resolveKeyholderSwitch(home, repoPath string) (keyholderSwitch, error) {
	var soft []error
	sw, err := resolveKeyholderLayers(home, repoPath)
	if err != nil {
		if !isSoftKeyholderErr(err) {
			return sw, err
		}
		soft = append(soft, err)
	}
	if err := applyPasswdHomeLayers(&sw, home, repoPath); err != nil {
		if !isSoftKeyholderErr(err) {
			return sw, err
		}
		soft = append(soft, err)
	}
	if len(soft) == 0 {
		return sw, nil
	}
	if err := keyholderNewSourceFailure(errors.Join(soft...), sw.On); err != nil {
		return sw, err
	}
	return sw, nil
}

// keyholderPasswdHomeFn is the home the switch's trust anchor lives under: the
// account database's, never the caller's environment (internal/0/trustedhome). A
// seam for tests.
var keyholderPasswdHomeFn = trustedhome.Home

// applyPasswdHomeLayers closes the HOME half of "the caller chooses what is
// trusted". The callers hand resolveKeyholderSwitch a home taken from $HOME, so a
// process that starts with HOME=/somewhere/else reads the global sweatfile of
// /somewhere/else and sees the switch off. The passwd home's layers (the global
// file and the directories above the repo) are read as well: the switch is ON if
// they say on, and the two program paths come from them when they set them (a
// path the caller's own directory names may not replace the one the host wrote).
// An account database that cannot answer fails closed: no launch.
func applyPasswdHomeLayers(sw *keyholderSwitch, home, repoPath string) error {
	pw, err := keyholderPasswdHomeFn()
	if err != nil {
		return softKeyholderErr(fmt.Errorf("resolving the account's home for [keyholder]: %w (the switch is read from the passwd home, never from $HOME alone)", err))
	}
	if filepath.Clean(pw) == filepath.Clean(home) {
		return nil
	}
	ph, err := sweatfileio.LoadHierarchy(pw, repoPath)
	if err != nil {
		return softKeyholderErr(fmt.Errorf("reading the sweatfiles under the account's home for [keyholder]: %w", err))
	}
	on, _, err := sweatfileio.TrustedKeyholderMode(ph, pw, repoPath)
	if err != nil {
		return fmt.Errorf("the account's home (%s): %w (sessions are not launched until it is fixed: a wrong key holder switch must not fall back to the operator's agent)", pw, err)
	}
	fibby, piggy, _ := sweatfileio.TrustedKeyholderTools(ph, pw, repoPath)
	sw.On = sw.On || on
	if fibby != "" {
		sw.Fibby = fibby
	}
	if piggy != "" {
		sw.Piggy = piggy
	}
	return nil
}

func resolveKeyholderLayers(home, repoPath string) (keyholderSwitch, error) {
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
		return sw, softKeyholderErr(fmt.Errorf("reading the driver's sweatfiles for [keyholder] (%s): %w", driver, err))
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
