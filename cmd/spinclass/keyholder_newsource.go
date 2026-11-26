package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// A host that never switched the key holder on must launch exactly as it did
// before the key holder existed. The key holder added NEW places a launch can
// fail: the account database's home lookup, the layers read under that home, the
// driver's own context, the home directory and the state directory the relaunch
// paths resolve. A failure of one of those is a keyholderSoftError. It refuses
// the launch only when there is EVIDENCE the switch is, or once was, on for this
// host: a layer that did load says on, or the key holder has left state under
// the state root (HasAnyState). Without evidence it is warned once on stderr and
// the launch goes ahead with the switch off, as before.
//
// An error that would have refused the launch before the key holder (the target
// repo's own hierarchy not loading) is not soft, and a [keyholder] table that
// does not parse or validate is not soft either: that is a layer that names the
// key holder, which is evidence in itself.
type keyholderSoftError struct{ err error }

func (e keyholderSoftError) Error() string { return e.err.Error() }
func (e keyholderSoftError) Unwrap() error { return e.err }

func softKeyholderErr(err error) error {
	if err == nil {
		return nil
	}
	return keyholderSoftError{err}
}

func isSoftKeyholderErr(err error) bool {
	var s keyholderSoftError
	return errors.As(err, &s)
}

// keyholderEvidenceRoots are the state roots whose contents are evidence the
// key holder is, or once was, on for this host: the one the caller's environment
// names (XDG_STATE_HOME, else $HOME) AND the account database's home, so a caller
// that starts a process with another XDG_STATE_HOME or an empty HOME cannot hide
// what the host holds. A root that is not absolute (an empty HOME makes
// keyholder.StateBase relative to the working directory) says nothing and is
// skipped; the account's own root covers that case.
func keyholderEvidenceRoots() []string {
	var roots []string
	add := func(r string) {
		if r == "" || !filepath.IsAbs(r) {
			return
		}
		r = filepath.Clean(r)
		for _, have := range roots {
			if have == r {
				return
			}
		}
		roots = append(roots, r)
	}
	add(keyholderStateBaseFn())
	if pw, err := keyholderPasswdHomeFn(); err == nil {
		add(keyholder.PasswdStateBase(pw))
	}
	return roots
}

// keyholderEvidenceFn says whether the key holder has left state under one of
// the state roots, and which root: the evidence a new failure source needs before
// it may refuse a launch. A root that cannot be read counts as evidence (not
// knowing is not "no"). A seam for tests.
var keyholderEvidenceFn = func() (bool, string) {
	for _, root := range keyholderEvidenceRoots() {
		if has, _ := keyholder.HasAnyState(root); has {
			return true, root
		}
	}
	return false, ""
}

// keyholderLoadedOnViaPasswd says whether the account database's own home layers
// say the key holder is on, for the sites that resolve the caller's HOME or state
// directory themselves and cannot trust either: HOME empty or unset must not turn
// "a layer says on" into "nothing says on". A refusal from the resolution counts
// as on (not knowing is not "off"); a passwd lookup that fails says nothing.
func keyholderLoadedOnViaPasswd(repoPath string) bool {
	pw, err := keyholderPasswdHomeFn()
	if err != nil || pw == "" {
		return false
	}
	sw, err := resolveKeyholderSwitch(pw, repoPath)
	return err != nil || sw.On
}

// keyholderWarnWriter is where the one-line warning goes. A seam for tests.
var keyholderWarnWriter io.Writer = os.Stderr

var keyholderWarned sync.Map

// keyholderWarnOnce prints msg to stderr once per process.
func keyholderWarnOnce(msg string) {
	if _, dup := keyholderWarned.LoadOrStore(msg, true); dup {
		return
	}
	fmt.Fprintf(keyholderWarnWriter, "spinclass: key holder: %s (no layer says the key holder is on and it has left no state on this host, so this launch proceeds as it did before)\n", msg)
}

// keyholderNewSourceFailure decides what a failure of a NEW source does: refuse
// (with the error, and why it refused and the way out) when there is evidence the
// switch is or was on, else warn once and return nil. loadedOn is true when a
// layer that did load says on.
func keyholderNewSourceFailure(err error, loadedOn bool) error {
	if err == nil {
		return nil
	}
	if loadedOn {
		return fmt.Errorf("%w; refused because a sweatfile layer that did load says the key holder is on: fix what failed above", err)
	}
	if has, root := keyholderEvidenceFn(); has {
		return fmt.Errorf("%w; refused because the key holder is or was on for this host (it has left state under %s): fix what failed above, or, with no key holder in use, run `sc keyholder-stop --all` and `sc clean` and remove %s to launch as before", err, root, root)
	}
	keyholderWarnOnce(err.Error())
	return nil
}
