package bridgeserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

func TestListenStaysOffWhileAnotherHoldsTheBridgeLock(t *testing.T) {
	dir := shortDir(t)
	f, err := os.OpenFile(filepath.Join(dir, ".bridge.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err == nil {
		_ = s.Close()
		t.Fatal("Listen succeeded while another process held the bridge lock")
	}
	if !strings.Contains(err.Error(), ".bridge.lock") {
		t.Errorf("the reason does not name the lock: %v", err)
	}
	if _, serr := os.Lstat(filepath.Join(dir, bridge.SocketName)); !os.IsNotExist(serr) {
		t.Errorf("a socket was bound without the lock: %v", serr)
	}
}

func TestConcurrentListenOnOneDirExactlyOneWins(t *testing.T) {
	dir := shortDir(t)
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []*Server
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
			if err == nil {
				mu.Lock()
				winners = append(winners, s)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("%d Listen calls won, want exactly 1", len(winners))
	}
	if err := winners[0].Close(); err != nil {
		t.Fatal(err)
	}
	// The lock dies with the server: a successor gets it, and the stale lock
	// file left in the directory is harmless.
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatalf("Listen after Close: %v", err)
	}
	_ = s.Close()
}

func TestLockFileIs0600(t *testing.T) {
	s, dir := start(t, Options{})
	_ = s
	fi, err := os.Lstat(filepath.Join(dir, ".bridge.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode %o, want 600", fi.Mode().Perm())
	}
}

func TestSocketUnlinkedUnderARunningServerIsReportedAsDeath(t *testing.T) {
	ticks := make(chan time.Time)
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{
		PeerCheck: allowAll,
		Tick:      func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()

	// Nothing changed: a tick is not a death.
	ticks <- time.Now()
	select {
	case err := <-done:
		t.Fatalf("Serve ended on an intact socket: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := os.Remove(filepath.Join(dir, bridge.SocketName)); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil after its socket was unlinked; it must report a dead listener")
		}
		if !strings.Contains(err.Error(), "socket") {
			t.Errorf("the error does not name the socket: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a server on an unlinked inode kept running")
	}
}

func TestSocketReplacedByAnotherFileIsReportedAsDeath(t *testing.T) {
	ticks := make(chan time.Time)
	dir := shortDir(t)
	path := filepath.Join(dir, bridge.SocketName)
	s, err := Listen(dir, testUnit, Options{
		PeerCheck: allowAll,
		Tick:      func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil after its socket was replaced")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a server whose socket was replaced kept running")
	}
	// The replacement is not ours: Close leaves it.
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("the replacement was removed: %v", err)
	}
}

func TestSymlinkPlantedAfterBindIsRefusedAndNothingIsChmodded(t *testing.T) {
	orig := afterBind
	t.Cleanup(func() { afterBind = orig })
	victim := filepath.Join(shortDir(t), "victim")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(victim, 0o644); err != nil {
		t.Fatal(err)
	}
	afterBind = func(path string) {
		_ = os.Remove(path)
		if err := os.Symlink(victim, path); err != nil {
			t.Error(err)
		}
	}
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err == nil {
		_ = s.Close()
		t.Fatal("Listen accepted a symlink at the socket path")
	}
	fi, serr := os.Stat(victim)
	if serr != nil {
		t.Fatal(serr)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("the symlink's target was chmodded to %o", fi.Mode().Perm())
	}
}

func TestSocketBoundWithModeSetBeforeBind(t *testing.T) {
	// A permissive process umask must not leave a window: the mode is right at
	// bind, and the process umask is restored after.
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	_, dir := start(t, Options{})
	fi, err := os.Lstat(filepath.Join(dir, bridge.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("socket mode %o under umask 0", fi.Mode().Perm())
	}
	if got := syscall.Umask(0); got != 0 {
		t.Errorf("the umask was left at %o, want it restored to 0", got)
	}
}
