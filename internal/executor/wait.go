package executor

import (
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
)

// WaitForExit polls every 50ms until either the PID has exited (kill -0
// fails) or the timeout elapses. Returns nil in both cases — this is a
// best-effort helper for shutdown sequences where we'd like the process
// to be gone but don't want to hang forever if it isn't.
//
// Callers that need to know whether the process is actually dead should
// re-check session.IsAlive after WaitForExit returns.
func WaitForExit(pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !session.IsAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
