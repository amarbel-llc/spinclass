package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/trustedhome"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/validate"
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
	// File is what the deploy-free switch file under the account's home says (#519):
	// empty when the file is absent, else on, off or default. FilePath is where it
	// lives. They are zero on a host that never used the file.
	File     sweatfileio.KeyholderSwitchValue
	FilePath string
	// ToolConflict is the target/driver disagreement on a program path, kept even
	// while the switch is off: the switch can still turn on LATER (the passwd
	// home's layers, the switch file), and a path chosen by the caller's directory
	// may not swap the program the pair runs on any route to on (M-1 of the
	// switch review). It is returned wherever On becomes true.
	ToolConflict error
}

// refuseToolConflict is the one check every route to On runs once On is true.
func (s keyholderSwitch) refuseToolConflict() error {
	if s.On && s.ToolConflict != nil {
		return s.ToolConflict
	}
	return nil
}

// Source says where the effective mode came from: the switch file when it said
// on or off, a sweatfile layer when a layer turned the key holder on, else none.
func (s keyholderSwitch) Source() string {
	switch {
	case s.File == sweatfileio.KeyholderSwitchOn || s.File == sweatfileio.KeyholderSwitchOff:
		return "switch file"
	case s.On:
		return "layer"
	}
	return "none"
}

// resolveKeyholderSwitch is the one place every launch site, `sc validate`,
// `sc host-load` and `sc keyholder-switch` read the key holder switch: the
// sweatfile layers (resolveKeyholderLayersAndPasswd) and then the deploy-free
// switch file under the account's home (applyKeyholderSwitchFile). With the
// file absent it is exactly what the layers say.
func resolveKeyholderSwitch(home, repoPath string) (keyholderSwitch, error) {
	return resolveKeyholderSwitchFrom(home, repoPath, keyholderDriverRepoFn(home))
}

// resolveSessionKeyholderSwitch is the switch for a sentence about a session
// that already exists: its OWN repo's trusted layers and the switch file, never
// the caller's directory. A listing run from a directory where the key holder is
// on must not say it is on for every session on the host.
func resolveSessionKeyholderSwitch(home, repoPath string) (keyholderSwitch, error) {
	return resolveKeyholderSwitchFrom(home, repoPath, "")
}

func resolveKeyholderSwitchFrom(home, repoPath, driver string) (keyholderSwitch, error) {
	sw, err := resolveKeyholderLayersAndPasswdFrom(home, repoPath, driver)
	if err != nil {
		return sw, err
	}
	if err := applyKeyholderSwitchFile(&sw); err != nil {
		return sw, err
	}
	return sw, nil
}

// applyKeyholderSwitchFile lays the switch file over the layers' answer, for NEW
// launches (a running pair is never touched). off is the kill switch and beats
// every layer; on turns the key holder on, and refuses when no trusted layer
// supplies fibby and piggy; default, or no file, leaves the layers to decide. The
// file is read at the account database's home, never at a home the caller's
// environment names. An unreadable or invalid file refuses (the error names the
// path and the way out). An account database that cannot answer says nothing
// here: the layers' own reading of it has already failed or warned.
func applyKeyholderSwitchFile(sw *keyholderSwitch) error {
	pw, err := keyholderPasswdHomeFn()
	if err != nil || pw == "" {
		return nil
	}
	v, present, err := sweatfileio.ReadKeyholderSwitch(pw)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	sw.File, sw.FilePath = v, sweatfileio.KeyholderSwitchPath(pw)
	switch v {
	case sweatfileio.KeyholderSwitchOff:
		sw.On = false
	case sweatfileio.KeyholderSwitchOn:
		if sw.Fibby == "" || sw.Piggy == "" {
			return keyholder.PreflightError([]keyholder.Problem{{
				Name: "switch-file",
				What: fmt.Sprintf("the key holder switch file %s says on, but no trusted [keyholder] layer sets fibby and piggy (the host's configuration writes them): launches are refused until it does", sw.FilePath),
				Next: validate.KeyholderProgramNext + "; or undo the switch itself with `sc keyholder-switch default` at a terminal",
			}})
		}
		sw.On = true
	}
	return sw.refuseToolConflict()
}

// resolveKeyholderLayersAndPasswd reads the switch the way the two sibling switches are
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
// The account database's home not resolving is the one failure left of a source
// this feature added: it refuses only with evidence the switch is or was on for
// this host (a layer that did load says on, or state under either state root);
// otherwise it is warned once and the launch proceeds as before
// (keyholderNewSourceFailure). A layer that EXISTS but cannot be loaded (the
// driver's context, the account's home layers), a [keyholder] table that does not
// parse or validate, and the target repo's own hierarchy not loading all refuse:
// an unloadable file may be the one that switches the key holder on, and only an
// ABSENT layer is "never on".
func resolveKeyholderLayersAndPasswd(home, repoPath string) (keyholderSwitch, error) {
	return resolveKeyholderLayersAndPasswdFrom(home, repoPath, keyholderDriverRepoFn(home))
}

// resolveKeyholderLayersAndPasswdFrom is the same with the driver's context
// named; an empty driver reads none.
func resolveKeyholderLayersAndPasswdFrom(home, repoPath, driver string) (keyholderSwitch, error) {
	var soft []error
	sw, err := resolveKeyholderLayers(home, repoPath, driver)
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
	ph, err := sweatfileio.LoadTrustedHierarchy(pw, repoPath)
	if err != nil {
		// LoadTrustedHierarchy fails only for a TRUSTED layer (the global file or a
		// directory above the repo) that EXISTS and cannot be read or parsed (an
		// absent layer is skipped; the repo's own file is never read here, it could
		// never have said "on" and the target's was already read by the launch): that
		// file may be the one that switches the key holder on, so it refuses, with no
		// state needed as evidence.
		return fmt.Errorf("reading the sweatfiles under the account's home for [keyholder]: %w (a sweatfile that exists but cannot be loaded may be the layer that switches the key holder on: sessions are not launched until it is fixed or removed)", err)
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
	return sw.refuseToolConflict()
}

func resolveKeyholderLayers(home, repoPath, driver string) (keyholderSwitch, error) {
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

	if driver == "" || driver == repoPath {
		return sw, nil
	}
	dh, err := sweatfileio.LoadTrustedHierarchy(home, driver)
	if err != nil {
		// An existing TRUSTED layer that cannot be loaded may be the one that switches
		// the key holder on: it refuses, with no state needed as evidence (an absent
		// layer is skipped and is "never on"). The driver repo's OWN main-checkout
		// sweatfile, and ~/sweatfile when the working directory is in no repo (the
		// driver is then home and that file is its "repo" layer), are untrusted: they
		// could never have said "on", so they are not read here and their failure
		// refuses nothing (F1 of the follow-up review).
		return sw, fmt.Errorf("reading the driver's sweatfiles for [keyholder] (%s): %w (a sweatfile that exists but cannot be loaded may be the layer that switches the key holder on: sessions are not launched until it is fixed or removed)", driver, err)
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
			conflict := fmt.Errorf("[keyholder] %s is %q for the target repo and %q for the driver's context (%s): the program the pair runs may not depend on which directory the caller chose; make the host's configuration write one value in a layer both see", p.name, p.target, p.driver, driver)
			if sw.ToolConflict == nil {
				sw.ToolConflict = conflict
			}
			if sw.On {
				return sw, conflict
			}
		case p.target == "":
			*p.resolved = p.driver
		}
	}
	return sw, nil
}
