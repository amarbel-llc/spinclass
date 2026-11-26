package keyholder

import (
	"context"
	"os"
	"testing"
)

// An operator's stop keeps the key history, marked stopped; the session's close
// ends it, so a later session with the same key starts clean.
func TestForgetSessionEndsAHistoryAnOperatorStopKept(t *testing.T) {
	l, _, _ := launcher(t)
	const key = "spinclass/reused"
	if _, err := l.Start(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := l.StopFoundWith(context.Background(), Found{Hash: Hash(key), Session: key}, StopOptions{KeepHistory: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(LastPath(l.Base, key)); err != nil {
		t.Fatalf("the operator's stop did not keep the history: %v", err)
	}
	if err := l.ForgetSession(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(LastPath(l.Base, key)); err == nil {
		t.Error("the key history survived ForgetSession")
	}
	if _, err := os.Lstat(lockPathHash(l.Base, Hash(key))); err == nil {
		t.Error("the lock file survived ForgetSession")
	}
	if hasHistory(l.Base, key) {
		t.Error("a session with a reused key would inherit a stale key history")
	}
}

func hasHistory(base, key string) bool { _, ok := readLast(LastPath(base, key)); return ok }

// Find saw a lock file and no directory; a launch made the directory before the
// sweep took the lock: the sweep leaves everything the launch made alone.
func TestStopFoundOfALockOnlyEntryLeavesAPairALaunchMadeSince(t *testing.T) {
	l, _, _ := launcher(t)
	const key = "spinclass/racing"
	h := Hash(key)
	if err := os.MkdirAll(l.Base, 0o700); err != nil {
		t.Fatal(err)
	}
	hist := LastPath(l.Base, key)
	if err := writeLast(hist, last{Fingerprint: "SHA256:live"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPathHash(l.Base, h), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	found := Found{Hash: h, LockOnly: true} // as Find saw it, before the launch
	if err := os.Mkdir(SessionDir(l.Base, key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := l.StopFound(context.Background(), found); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLast(hist); !ok {
		t.Error("the sweep ended the key history of a session whose launch made a directory since")
	}
	if _, err := os.Lstat(SessionDir(l.Base, key)); err != nil {
		t.Errorf("the sweep removed the directory a launch made: %v", err)
	}
}

// A session with nothing on record costs three stats and creates nothing.
func TestForgetSessionWithNothingOnRecordCreatesNothing(t *testing.T) {
	l, _, _ := launcher(t)
	if err := l.ForgetSession(context.Background(), "spinclass/none"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(l.Base); err == nil {
		t.Error("ForgetSession created the state directory for a session with nothing on record")
	}
}
