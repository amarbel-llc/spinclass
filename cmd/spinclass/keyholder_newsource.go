package main

import (
	"errors"
	"fmt"
	"io"
	"os"
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

// keyholderEvidenceFn says whether the key holder has left state under the state
// root: the evidence a new failure source needs before it may refuse a launch. A
// state root that cannot be read counts as evidence (not knowing is not "no").
// A seam for tests.
var keyholderEvidenceFn = func() bool {
	has, _ := keyholder.HasAnyState(keyholderStateBaseFn())
	return has
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
// (with the error) when there is evidence the switch is or was on, else warn once
// and return nil. loadedOn is true when a layer that did load says on.
func keyholderNewSourceFailure(err error, loadedOn bool) error {
	if err == nil {
		return nil
	}
	if loadedOn || keyholderEvidenceFn() {
		return err
	}
	keyholderWarnOnce(err.Error())
	return nil
}
