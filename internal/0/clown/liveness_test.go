package clown

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// resetProtocolCheck clears the memoized CheckProtocol verdict. CheckProtocol is
// process-global by design (one check per serve process), but a single test
// binary must exercise both a match and a mismatch, so tests reset it between
// cases. Every test that calls CheckProtocol must reset first, or it inherits a
// prior test's cache.
func resetProtocolCheck() {
	protocolOnce = sync.Once{}
	protocolOK, protocolWant, protocolGot, protocolErr = false, 0, 0, nil
}

func TestCheckProtocolMatch(t *testing.T) {
	resetProtocolCheck()
	argsFile := filepath.Join(t.TempDir(), "args")
	// Print the SAME version this binary linked, so the compare is a match
	// without hardcoding the integer (which would silently drift from the const
	// on a future bump).
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, fmt.Sprint(jobwake.ProtocolVersion), true))

	ok, want, got, err := CheckProtocol(context.Background())
	if err != nil {
		t.Fatalf("CheckProtocol: %v", err)
	}
	if !ok {
		t.Errorf("match: ok=false (want=%d got=%d)", want, got)
	}
	if want != jobwake.ProtocolVersion || got != jobwake.ProtocolVersion {
		t.Errorf("want=%d got=%d, both expected to be the linked ProtocolVersion %d", want, got, jobwake.ProtocolVersion)
	}
	assertArgv(t, recordedArgs(t, argsFile), []string{"version", "--protocol"})
}

func TestCheckProtocolMismatch(t *testing.T) {
	resetProtocolCheck()
	argsFile := filepath.Join(t.TempDir(), "args")
	mismatch := jobwake.ProtocolVersion + 1
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, fmt.Sprint(mismatch), true))

	ok, want, got, err := CheckProtocol(context.Background())
	if err != nil {
		t.Fatalf("CheckProtocol: %v", err)
	}
	if ok {
		t.Error("mismatch: ok=true, want false")
	}
	if got != mismatch {
		t.Errorf("got=%d, want the CLI's reported %d", got, mismatch)
	}
	if want != jobwake.ProtocolVersion {
		t.Errorf("want=%d, expected the linked const %d", want, jobwake.ProtocolVersion)
	}
}

func TestCheckProtocolUnparseable(t *testing.T) {
	resetProtocolCheck()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "not-a-number", true))

	ok, _, _, err := CheckProtocol(context.Background())
	if ok {
		t.Error("unparseable output: ok=true, want false")
	}
	if err == nil {
		t.Error("unparseable output: err=nil, want a parse error")
	}
}

func TestCheckProtocolCLIError(t *testing.T) {
	// An old ringmaster with no `version --protocol` exits nonzero. Treated as
	// "couldn't determine" => not ok, err set => the caller skips the flock (the
	// safe degrade — an unknown protocol is presumed incompatible).
	resetProtocolCheck()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "", false))

	ok, _, _, err := CheckProtocol(context.Background())
	if ok {
		t.Error("CLI error: ok=true, want false")
	}
	if err == nil {
		t.Error("CLI error: err=nil, want an error")
	}
}

func TestCheckProtocolMemoized(t *testing.T) {
	// The verdict is computed once; a second call must not re-shell. This is the
	// whole reason job.Start reads the FlockEnabled flag instead of calling
	// CheckProtocol per dispatch.
	resetProtocolCheck()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, fmt.Sprint(jobwake.ProtocolVersion), true))

	_, _, _, _ = CheckProtocol(context.Background())
	_, _, _, _ = CheckProtocol(context.Background())

	// One invocation records exactly the two argv lines "version" and
	// "--protocol"; a second invocation would double that.
	if got := recordedArgs(t, argsFile); len(got) != 2 {
		t.Errorf("memoization: recorded %d argv lines %q, want 2 (a single invocation)", len(got), got)
	}
}

func TestFlockEnabledFlag(t *testing.T) {
	SetFlockEnabled(true)
	if !FlockEnabled() {
		t.Error("after SetFlockEnabled(true): FlockEnabled()=false")
	}
	SetFlockEnabled(false)
	if FlockEnabled() {
		t.Error("after SetFlockEnabled(false): FlockEnabled()=true")
	}
}

// TestAcquireJobLockLifecycle drives clown.AcquireJobLock against real jobwake
// path resolution in an isolated state dir (no ringmaster binary needed — the
// flock is a pure in-process library call). It proves the wrapper acquires,
// refuses a concurrent second holder, and re-acquires after release — the flock
// contract #26 relies on to make a live producer distinguishable from a dead one.
func TestAcquireJobLockLifecycle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "spinclass-flock-test")
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	jobID := "merge-deadbeef"

	release, err := AcquireJobLock(jobID)
	if err != nil {
		t.Fatalf("AcquireJobLock: %v", err)
	}

	// While the first holder is live, a second acquire must be refused — the
	// crash-liveness guarantee depends on exactly one live holder per job.
	if _, err2 := AcquireJobLock(jobID); !errors.Is(err2, jobwake.ErrAlreadyLocked) {
		t.Errorf("double-acquire while held: got %v, want ErrAlreadyLocked", err2)
	}

	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// After release the lock is free (the explicit-release counterpart of the
	// OS releasing it on crash) — a re-acquire must succeed.
	release2, err := AcquireJobLock(jobID)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("release2: %v", err)
	}
}
