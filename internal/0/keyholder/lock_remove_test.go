package keyholder

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A pair that is stopped leaves no lock file: the operator's rule is that
// stopping a session's pair, and switching off, leave nothing behind. The lock
// is there while the pair lives, owner-only, beside (not inside) its directory.
func TestStopLeavesNoLockFile(t *testing.T) {
	l, _, _ := launcher(t)
	if _, err := l.Start(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	lock := LockPath(l.Base, "s/a")
	fi, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("no lock file while the pair lives: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("lock mode = %o, want 600", fi.Mode().Perm())
	}
	if err := l.Stop(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Errorf("the lock file survived Stop: %v", err)
	}
	// Stopping a session that has no pair is not an error and leaves nothing either.
	if err := l.Stop(context.Background(), "s/none"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(LockPath(l.Base, "s/none")); !os.IsNotExist(err) {
		t.Errorf("a Stop of a session with no pair left a lock file: %v", err)
	}
}

// A Stop that fails (a unit will not stop) leaves the pair, so it leaves the lock.
func TestAFailedStopKeepsTheLockFile(t *testing.T) {
	l, f, _ := launcher(t)
	if _, err := l.Start(context.Background(), "s/stuck"); err != nil {
		t.Fatal(err)
	}
	f.stopStuck["spinclass-keyholder-agent-"+Hash("s/stuck")+".service"] = true
	l.Wait = 50 * time.Millisecond
	if err := l.Stop(context.Background(), "s/stuck"); err == nil {
		t.Fatal("Stop returned nil with a unit that will not stop")
	}
	if _, err := os.Lstat(LockPath(l.Base, "s/stuck")); err != nil {
		t.Errorf("the lock file went with a pair that is still there: %v", err)
	}
}

// Across the unlink no two callers are ever inside the lock together: a waiter
// that held the old inode must notice the file is gone and lock the new one.
// Real flock, real files, many rounds.
func TestAStopRacingAStartAcrossTheLockUnlinkNeverLetsTwoHoldersIn(t *testing.T) {
	l, _, _ := launcher(t)
	hash := Hash("s/race")
	var inside, worst atomic.Int32
	enter := func(hold time.Duration) {
		n := inside.Add(1)
		for {
			w := worst.Load()
			if n <= w || worst.CompareAndSwap(w, n) {
				break
			}
		}
		time.Sleep(hold)
		inside.Add(-1)
	}
	// Each round: A takes the lock, holds it a moment and removes the file (a
	// Stop). B arrives at once and waits on the file A has open: when A lets go B
	// gets a flock on an inode nobody can see any more. C arrives AFTER the unlink
	// and locks the new file, and stays inside for longer than B's poll: if B did
	// not notice its file was unlinked it would enter beside C.
	for round := 0; round < 200; round++ {
		var wg sync.WaitGroup
		step := func(delay, hold time.Duration, remove bool) {
			defer wg.Done()
			time.Sleep(delay)
			h, err := l.acquire(context.Background(), hash, "s/race", 10*time.Second)
			if err != nil {
				t.Errorf("round %d: %v", round, err)
				return
			}
			enter(hold)
			if remove {
				h.Remove()
			} else {
				h.Unlock()
			}
		}
		wg.Add(3)
		go step(0, 2*time.Millisecond, true)                       // A: the Stop
		go step(500*time.Microsecond, 100*time.Microsecond, false) // B: waits on the old inode
		go step(6*time.Millisecond, 40*time.Millisecond, false)    // C: arrives after the unlink
		wg.Wait()
	}
	if worst.Load() != 1 {
		t.Fatalf("up to %d callers were inside the lock at once, want 1", worst.Load())
	}
}

// A lock file with no pair and no holder is removed by the sweep's stop; one
// that a process holds is left, and not waited for.
func TestAStaleLockWithNoPairIsFoundAndRemoved(t *testing.T) {
	l, _, _ := launcher(t)
	hash := Hash("s/stale")
	if err := os.MkdirAll(l.Base, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := lockPathHash(l.Base, hash)
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := l.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got *Found
	for i := range found {
		if found[i].Hash == hash {
			got = &found[i]
		}
	}
	if got == nil || !got.LockOnly || got.Dir != "" {
		t.Fatalf("Find = %+v, want the stale lock as a LockOnly entry", found)
	}
	if err := l.StopFound(context.Background(), *got); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Errorf("the stale lock file survived StopFound: %v", err)
	}
}

func TestALockHeldByAnotherProcessIsNotRemovedByTheSweep(t *testing.T) {
	l, _, _ := launcher(t)
	hash := Hash("s/busy")
	h, err := l.acquire(context.Background(), hash, "s/busy", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lock := lockPathHash(l.Base, hash)
	started := time.Now()
	if err := l.StopFound(context.Background(), Found{Hash: hash, LockOnly: true}); err != nil {
		t.Fatalf("StopFound of a held stale lock = %v, want it skipped quietly", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("the sweep waited %v for a held lock", time.Since(started))
	}
	if _, err := os.Lstat(lock); err != nil {
		t.Fatalf("a held lock file was removed: %v", err)
	}
	h.Unlock()
	if err := l.StopFound(context.Background(), Found{Hash: hash, LockOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Errorf("the released stale lock was not removed: %v", err)
	}
}

// The lock file is never inside the session directory (Stop removes that).
func TestTheSessionLockSitsBesideTheSessionDirectory(t *testing.T) {
	l, _, _ := launcher(t)
	if filepath.Dir(LockPath(l.Base, "s/a")) != l.Base {
		t.Errorf("the lock %s is not in the keyholder root %s", LockPath(l.Base, "s/a"), l.Base)
	}
}
