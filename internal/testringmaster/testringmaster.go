// Package testringmaster starts ringmaster's in-process host service for a
// test (RFC-0020 §7.3). Since the S7 flag day every jobwake client call —
// jobs, locks, presence — is a socket call to that service, so a test that
// reaches jobwake needs one running in its own isolated environment.
package testringmaster

import (
	"os"
	"testing"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// shortRuntimeBase hosts the per-test runtime dir. A unix socket path is
// limited to about 108 bytes, and t.TempDir() (named after the test) can blow
// that, so the runtime dir is created directly under /tmp.
const shortRuntimeBase = "/tmp"

// Start points XDG_RUNTIME_DIR at a fresh short directory and XDG_STATE_HOME
// at a fresh temp dir, then runs the in-process service there for the test's
// lifetime. jobwake's shared client rebuilds when the socket path changes, so
// each test gets its own service and store. The service is stopped before the
// environment is restored and the runtime dir removed (cleanups run LIFO).
// It returns the state dir (the store root's parent) for tests that inspect
// the journal on disk.
func Start(t *testing.T) (stateHome string) {
	t.Helper()
	base := shortRuntimeBase
	if _, err := os.Stat(base); err != nil {
		base = os.TempDir()
	}
	runtimeDir, err := os.MkdirTemp(base, "rm")
	if err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	stateHome = t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	// A developer shell under clown may set this; the service tests need the
	// real wire.
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")

	stop, err := jobwake.StartService(nil)
	if err != nil {
		t.Fatalf("in-process ringmaster service: %v", err)
	}
	t.Cleanup(stop)
	return stateHome
}
