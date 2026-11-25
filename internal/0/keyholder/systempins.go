package keyholder

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

// The pair launcher runs outside every unit and holds a session's key, and it
// runs in whatever process starts a session: for spawn-session that is a
// driver session's own `spinclass serve`, whose PATH a session can influence.
// A name resolved from that PATH is therefore the hole, so systemd-run and
// systemctl are NEVER looked up on PATH. A production build burns their store
// paths in (-X main.systemdRunBin / main.systemctlBin, set into these pins by
// SetSystemdPins); a build without them (go test, a dev build) refuses to run a
// unit, naming the missing pin, except through OverrideSystemdPins (tests) or a
// runner given RunBin and CtlBin by a person at a terminal (`sc keyholder-pair`).
var (
	pinMu         sync.RWMutex
	pinSystemdRun string
	pinSystemctl  string
)

// SetSystemdPins records the build's pinned absolute paths of systemd-run and
// systemctl. An empty value leaves that tool unpinned.
func SetSystemdPins(run, ctl string) {
	pinMu.Lock()
	defer pinMu.Unlock()
	pinSystemdRun, pinSystemctl = run, ctl
}

// OverrideSystemdPins replaces the pins and returns a function that restores
// them. It exists for tests, which have no build to pin them.
func OverrideSystemdPins(run, ctl string) (restore func()) {
	pinMu.Lock()
	prevRun, prevCtl := pinSystemdRun, pinSystemctl
	pinSystemdRun, pinSystemctl = run, ctl
	pinMu.Unlock()
	return func() { SetSystemdPins(prevRun, prevCtl) }
}

// ErrToolNotPinned is what a tool path that is neither given nor pinned yields.
var ErrToolNotPinned = errors.New("keyholder: the tool is not pinned in this build and no absolute path was given")

// resolveTool returns the absolute path to run for name: the explicit path when
// the caller was handed one, else the build's pin. Never a PATH lookup.
func resolveTool(name, explicit, pinned string) (string, error) {
	p := explicit
	if p == "" {
		p = pinned
	}
	if p == "" {
		return "", fmt.Errorf("%w: %s (a production build burns its store path in; a person at a terminal passes it to `sc keyholder-pair`)", ErrToolNotPinned, name)
	}
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", fmt.Errorf("keyholder: the %s path %q is not an absolute, clean path", name, p)
	}
	return p, nil
}

func (r SystemdRunner) runBin() (string, error) {
	pinMu.RLock()
	defer pinMu.RUnlock()
	return resolveTool("systemd-run", r.RunBin, pinSystemdRun)
}

func (r SystemdRunner) ctlBin() (string, error) {
	pinMu.RLock()
	defer pinMu.RUnlock()
	return resolveTool("systemctl", r.CtlBin, pinSystemctl)
}
