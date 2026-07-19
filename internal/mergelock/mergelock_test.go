package mergelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// shortIntervals shrinks the poll/notify intervals for the duration of a
// test. Tests mutating package state must not run in parallel.
func shortIntervals(t *testing.T) {
	t.Helper()
	origPoll, origNotify := pollInterval, waitNotifyInterval
	pollInterval = 5 * time.Millisecond
	waitNotifyInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		pollInterval = origPoll
		waitNotifyInterval = origNotify
	})
}

func TestAcquireUncontended(t *testing.T) {
	dir := t.TempDir()

	lock, err := Acquire(context.Background(), dir, "session-a", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	path := filepath.Join(dir, LockFileName)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}
	if got := string(contents); got != "session-a" {
		t.Errorf("lock file contents = %q, want %q", got, "session-a")
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// The lock file must survive release (never unlinked).
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file after release: %v", err)
	}
}

func TestContention(t *testing.T) {
	shortIntervals(t)
	dir := t.TempDir()

	lockA, err := Acquire(context.Background(), dir, "holder-a", nil)
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}

	var (
		mu          sync.Mutex
		waitHolders []string
		waitCalls   int
	)
	firstWait := make(chan struct{})
	acquiredB := make(chan error, 1)

	go func() {
		lockB, err := Acquire(context.Background(), dir, "holder-b", func(holder string, elapsed time.Duration) {
			mu.Lock()
			waitHolders = append(waitHolders, holder)
			waitCalls++
			if waitCalls == 1 {
				close(firstWait)
			}
			mu.Unlock()
		})
		if err == nil {
			defer func() { _ = lockB.Release() }()
		}
		acquiredB <- err
	}()

	// B must enter the wait state (A holds the lock).
	select {
	case <-firstWait:
	case err := <-acquiredB:
		t.Fatalf("B acquired while A held the lock (err=%v)", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for B's onWait callback")
	}

	// Let a couple of notify intervals elapse so the periodic re-notify
	// path is exercised, then confirm B is still waiting.
	time.Sleep(3 * waitNotifyInterval)
	select {
	case err := <-acquiredB:
		t.Fatalf("B acquired while A still held the lock (err=%v)", err)
	default:
	}

	if err := lockA.Release(); err != nil {
		t.Fatalf("Release A: %v", err)
	}

	select {
	case err := <-acquiredB:
		if err != nil {
			t.Fatalf("B Acquire after A released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B did not acquire after A released")
	}

	mu.Lock()
	defer mu.Unlock()
	if waitCalls < 2 {
		t.Errorf("onWait called %d times, want >= 2 (immediate + periodic)", waitCalls)
	}
	sawHolderA := false
	for _, h := range waitHolders {
		if h == "holder-a" {
			sawHolderA = true
			break
		}
	}
	if !sawHolderA {
		t.Errorf("onWait never observed holder-a; holders seen: %q", waitHolders)
	}
}

func TestAcquireContextCancelled(t *testing.T) {
	shortIntervals(t)
	dir := t.TempDir()

	lockA, err := Acquire(context.Background(), dir, "holder-a", nil)
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	defer func() { _ = lockA.Release() }()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		lock, err := Acquire(ctx, dir, "holder-b", nil)
		if err == nil {
			_ = lock.Release()
		}
		result <- err
	}()

	// Give B time to enter the wait loop, then cancel.
	time.Sleep(5 * pollInterval)
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire did not return promptly after cancellation")
	}
}

func TestReleaseIdempotent(t *testing.T) {
	dir := t.TempDir()

	lock, err := Acquire(context.Background(), dir, "session-a", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v (want nil no-op)", err)
	}

	// The lock must actually be free after release: a fresh acquire
	// succeeds immediately.
	lock2, err := Acquire(context.Background(), dir, "session-b", nil)
	if err != nil {
		t.Fatalf("re-Acquire after release: %v", err)
	}
	if err := lock2.Release(); err != nil {
		t.Fatalf("Release lock2: %v", err)
	}
}

func TestReleaseClearsHolder(t *testing.T) {
	dir := t.TempDir()

	lock, err := Acquire(context.Background(), dir, "session-a", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	contents, err := os.ReadFile(filepath.Join(dir, LockFileName))
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}
	if len(contents) != 0 {
		t.Errorf("lock file after release = %q, want empty", contents)
	}
}
