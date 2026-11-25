package keyholder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Two launches of one session at once (a resume in two terminals, a resume racing
// a spawn retry) make ONE pair and ONE key: the second waits for the first and
// then finds a live pair. Without the lock both see no record, both start the
// units and both mint, and the loser's failure path tears the winner's pair down.
func TestConcurrentStartsOfOneSessionMakeOnePairAndMintOnce(t *testing.T) {
	l, f, _ := launcher(t)
	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	pairs := make([]Pair, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			pairs[i], errs[i] = l.Start(context.Background(), "s/race")
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if got := f.count("start "); got != 2 {
		t.Errorf("units started = %d, want 2 (one fibby, one holder)", got)
	}
	if f.mintN != 1 {
		t.Errorf("mints = %d, want 1", f.mintN)
	}
	created := 0
	for i := range pairs {
		if pairs[i].PublicKey != pairs[0].PublicKey {
			t.Errorf("start %d got another key than start 0", i)
		}
		if pairs[i].Created {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d starts report they launched the pair, want exactly 1", created)
	}
	if !f.active["spinclass-keyholder-agent-"+Hash("s/race")+".service"] {
		t.Error("the pair is not running after the race")
	}
}

// A close racing a resume is serialised: afterwards the session either has a
// whole running pair or no directory at all, never a half-removed one.
func TestAStopRacingAStartLeavesAWholePairOrNone(t *testing.T) {
	for round := 0; round < 12; round++ {
		l, f, _ := launcher(t)
		if _, err := l.Start(context.Background(), "s/a"); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		gate := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-gate; _ = l.Stop(context.Background(), "s/a") }()
		go func() { defer wg.Done(); <-gate; _, _ = l.Start(context.Background(), "s/a") }()
		close(gate)
		wg.Wait()
		dir := filepath.Join(l.Base, Hash("s/a"))
		_, derr := os.Lstat(dir)
		agentUp := f.active["spinclass-keyholder-agent-"+Hash("s/a")+".service"]
		fibbyUp := f.active["spinclass-keyholder-fibby-"+Hash("s/a")+".service"]
		switch {
		case os.IsNotExist(derr):
			if agentUp || fibbyUp {
				t.Fatalf("round %d: no directory but a unit is running (agent %v, fibby %v)", round, agentUp, fibbyUp)
			}
		case derr == nil:
			if !agentUp || !fibbyUp {
				t.Fatalf("round %d: a directory without a whole running pair (agent %v, fibby %v)", round, agentUp, fibbyUp)
			}
			if _, err := os.Stat(filepath.Join(dir, "record.json")); err != nil {
				t.Fatalf("round %d: a running pair without its record: %v", round, err)
			}
		default:
			t.Fatalf("round %d: %v", round, derr)
		}
	}
}

// The lock lives beside the session's directory, so Stop removing the directory
// does not remove the file someone else may hold.
func TestTheSessionLockSurvivesAStopAndIsOwnerOnly(t *testing.T) {
	l, _, _ := launcher(t)
	if _, err := l.Start(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	if err := l.Stop(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(LockPath(l.Base, "s/a"))
	if err != nil {
		t.Fatalf("the lock file is gone after Stop: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("lock mode = %o, want 600", fi.Mode().Perm())
	}
	if filepath.Dir(LockPath(l.Base, "s/a")) != l.Base || strings.HasPrefix(LockPath(l.Base, "s/a"), filepath.Join(l.Base, Hash("s/a"))+string(filepath.Separator)) {
		t.Errorf("the lock %s is inside the session directory", LockPath(l.Base, "s/a"))
	}
}

// A lock that is held and never released fails the second launch with a message
// naming the lock, after lockWait, instead of waiting for ever.
func TestAHeldLockFailsTheNextStartWithANamedReason(t *testing.T) {
	prev := lockWait
	lockWait = 200 * time.Millisecond
	t.Cleanup(func() { lockWait = prev })
	l, _, _ := launcher(t)
	unlock, err := l.lockSession(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, err = l.Start(context.Background(), "s/a")
	if err == nil || !strings.Contains(err.Error(), LockPath(l.Base, "s/a")) {
		t.Errorf("Start with the lock held = %v, want an error naming %s", err, LockPath(l.Base, "s/a"))
	}
	if err := l.Stop(context.Background(), "s/a"); err == nil {
		t.Error("Stop with the lock held returned nil")
	}
}

// A symlink at the lock path is not followed.
func TestTheSessionLockRefusesASymlink(t *testing.T) {
	l, _, _ := launcher(t)
	if err := os.MkdirAll(l.Base, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, LockPath(l.Base, "s/a")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(context.Background(), "s/a"); err == nil {
		t.Error("Start followed a symlinked lock file")
	} else if !strings.Contains(err.Error(), "lock") && !strings.Contains(err.Error(), syscall.ELOOP.Error()) {
		t.Errorf("error does not name the lock: %v", err)
	}
}
