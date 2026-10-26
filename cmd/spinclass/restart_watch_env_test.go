package main

import (
	"slices"
	"testing"
	"time"
)

// The watchdog timer's service has the user manager's environment, not the
// frontend's. Its never-returned wake is sent under the same gate as the start
// and back wakes, which accepts CLOWN_SESSION_ID (the frontend has that, not
// CLOWN_BIN), so the timer must carry it or the wake is silently dropped.
func TestRestartWatchArgvCarriesTheClownSessionID(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "restarted-session-principal")
	argv := restartWatchArgv("/bin/spinclass", "worker/kid", time.Unix(1_700_000_000, 0), time.Unix(1_700_000_001, 0))
	if want := "--setenv=CLOWN_SESSION_ID=restarted-session-principal"; !slices.Contains(argv, want) {
		t.Errorf("argv = %q, want it to carry %q", argv, want)
	}
}
