package shop

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// probeAlive runs argv with the inherited env, bounded by timeout.
// Returns true iff the probe exits 0 within the deadline.
//
// Empty argv (no probe configured) returns false: without a probe we
// can't distinguish "user detached but multiplexer still alive" from
// "process truly gone," so the conservative answer is "dead." Callers
// that want a different default should configure
// `[session-entry].liveness-probe`.
//
// Errors that don't yield a clean exit code (binary missing, signal,
// timeout) all map to false. The probe is exec'd directly — wrap in
// `sh -c` if you need shell features.
func probeAlive(argv []string, timeout time.Duration) bool {
	if len(argv) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Env = os.Environ()
	if err := c.Run(); err != nil {
		return false
	}
	return c.ProcessState.ExitCode() == 0
}
