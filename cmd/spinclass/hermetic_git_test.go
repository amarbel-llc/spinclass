package main

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
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
	serveCallerEvidence = func() (confinement.Evidence, string) {
		return confinement.EvidenceUnconfined, "test default: unconfined"
	}
	// No test may arm a real systemd timer; the restart watchdog tests stub
	// scheduleRestartWatchFn themselves to see the argv.
	scheduleRestartWatchFn = func([]string) error { return nil }
	// The key holder switch reads the account's passwd home as well as the home a
	// test passes in (keyholder_switch.go applyPasswdHomeLayers). A real account
	// home has the developer's own sweatfiles in it; stand an empty one in.
	pwHome, err := os.MkdirTemp("", "khpw")
	if err != nil {
		panic(err)
	}
	keyholderPasswdHomeFn = func() (string, error) { return pwHome, nil }
	// Session kind records (driver or worker) are written under the pairs' state
	// root at a first launch with the switch on: stand a throwaway one in, so no
	// test writes the developer's real state.
	khState, err := os.MkdirTemp("", "khst")
	if err != nil {
		panic(err)
	}
	keyholderStateBaseFn = func() string { return filepath.Join(khState, "kh") }
	code := m.Run()
	_ = os.RemoveAll(pwHome)
	_ = os.RemoveAll(khState)
	cleanup()
	os.Exit(code)
}
