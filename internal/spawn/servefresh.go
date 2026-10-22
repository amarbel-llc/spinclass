package spawn

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A long-lived serve keeps running the binary it started with after a deploy
// replaces the one on PATH, and would keep writing unit specs with the old
// code (spinclass#399). A confined spawn compares the two first.

// Seams for tests.
var (
	runningExecutable = os.Executable
	lookPathSpinclass = func() (string, error) { return exec.LookPath("spinclass") }
)

// ServeBinary is the binary this process runs and the spinclass it would exec
// now, both with symlinks resolved (a profile path resolves to its store path).
type ServeBinary struct {
	Running, OnPath string
}

// Stale reports whether the two differ.
func (b ServeBinary) Stale() bool { return b.Running != b.OnPath }

// CurrentServeBinary resolves both paths; an error means freshness cannot be
// judged. A running binary that was deleted (the old store path collected)
// resolves with its " (deleted)" marker kept, so it reads as stale.
func CurrentServeBinary() (ServeBinary, error) {
	running, err := runningExecutable()
	if err != nil {
		return ServeBinary{}, fmt.Errorf("resolving the running binary: %w", err)
	}
	if !strings.HasSuffix(running, " (deleted)") {
		if running, err = filepath.EvalSymlinks(running); err != nil {
			return ServeBinary{}, fmt.Errorf("resolving the running binary: %w", err)
		}
	}
	onPath, err := lookPathSpinclass()
	if err != nil {
		return ServeBinary{}, fmt.Errorf("finding spinclass on PATH: %w", err)
	}
	if onPath, err = filepath.EvalSymlinks(onPath); err != nil {
		return ServeBinary{}, fmt.Errorf("resolving spinclass on PATH: %w", err)
	}
	return ServeBinary{Running: running, OnPath: onPath}, nil
}

// refuseStaleServe fails closed: a confined spawn proceeds only when the
// running binary is the one on PATH.
func refuseStaleServe() error {
	b, err := CurrentServeBinary()
	if err != nil {
		return fmt.Errorf("cannot spawn confined: cannot tell whether this serve runs the current spinclass (%v); restart the session (spinclass#399)", err)
	}
	if b.Stale() {
		return fmt.Errorf("cannot spawn confined: this serve is stale: it runs %s but spinclass on PATH is %s; "+
			"restart the session (or reconnect its spinclass MCP) so the unit spec is written by the current code (spinclass#399)",
			b.Running, b.OnPath)
	}
	return nil
}
