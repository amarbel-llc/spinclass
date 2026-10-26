package clown

import (
	"errors"
	"testing"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

func isolateJobLocks(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "spinclass-hold-lock-test")
}

// HoldJobLock takes nothing while the serve-start verdict is off (#368).
func TestHoldJobLockIsANoOpWhenTheFlockIsDisabled(t *testing.T) {
	isolateJobLocks(t)
	SetFlockEnabled(false)

	release, err := HoldJobLock("job-hold1")
	if err != nil {
		t.Fatalf("HoldJobLock: %v", err)
	}
	defer release()
	rel, err := AcquireJobLock("job-hold1")
	if err != nil {
		t.Fatalf("the lock was taken with the flock disabled: %v", err)
	}
	_ = rel()
}

// With the verdict on, HoldJobLock holds the job's lock until release.
func TestHoldJobLockHoldsUntilReleased(t *testing.T) {
	isolateJobLocks(t)
	SetFlockEnabled(true)
	t.Cleanup(func() { SetFlockEnabled(false) })

	release, err := HoldJobLock("job-hold2")
	if err != nil {
		t.Fatalf("HoldJobLock: %v", err)
	}
	if _, err := AcquireJobLock("job-hold2"); !errors.Is(err, jobwake.ErrAlreadyLocked) {
		t.Fatalf("while held: AcquireJobLock got %v, want ErrAlreadyLocked", err)
	}
	release()
	rel, err := AcquireJobLock("job-hold2")
	if err != nil {
		t.Fatalf("after release: AcquireJobLock got %v", err)
	}
	_ = rel()
}

// An empty job id (no ringmaster job) takes nothing.
func TestHoldJobLockIgnoresAnEmptyJobID(t *testing.T) {
	isolateJobLocks(t)
	SetFlockEnabled(true)
	t.Cleanup(func() { SetFlockEnabled(false) })

	release, err := HoldJobLock("")
	if err != nil {
		t.Fatalf("HoldJobLock: %v", err)
	}
	release()
}
