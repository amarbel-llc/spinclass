package main

import (
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestMain isolates every git invocation in this package's tests from the
// host git configuration (signing agent, hooks, templates) — see
// testgit.SetHermeticEnv.
func TestMain(m *testing.M) {
	cleanup, err := testgit.SetHermeticEnv()
	if err != nil {
		panic(err)
	}
	// Handler tests run as an unconfined caller by default; the gate tests in
	// serve_caller_gate_test.go put the real classifier back (useRealServeClassifier).
	serveCallerConfined = func() (bool, string) { return false, "test default: unconfined" }
	// No test may arm a real systemd timer; the restart watchdog tests stub
	// scheduleRestartWatchFn themselves to see the argv.
	scheduleRestartWatchFn = func([]string) error { return nil }
	code := m.Run()
	cleanup()
	os.Exit(code)
}
