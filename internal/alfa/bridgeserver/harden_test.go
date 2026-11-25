package bridgeserver

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

func TestDirModeRuleIsNoGroupOrOtherBits(t *testing.T) {
	for mode, wantOK := range map[os.FileMode]bool{
		0o700: true, 0o500: true, 0o600: true,
		0o750: false, 0o710: false, 0o705: false, 0o701: false, 0o770: false, 0o755: false,
	} {
		dir := shortDir(t)
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
		if mode == 0o500 || mode == 0o600 {
			// A directory that cannot be written (0500) or entered (0600) cannot
			// hold the socket; the mode rule itself accepts them.
			if err != nil && strings.Contains(err.Error(), "must be") {
				t.Errorf("mode %o refused by the mode rule: %v", mode, err)
			}
			if s != nil {
				_ = s.Close()
			}
			continue
		}
		if wantOK && err != nil {
			t.Errorf("mode %o refused: %v", mode, err)
		}
		if !wantOK && err == nil {
			t.Errorf("mode %o accepted", mode)
		}
		if s != nil {
			_ = s.Close()
		}
	}
}

func TestDirOwnerComesFromTheStatUIDSeam(t *testing.T) {
	orig := statUID
	t.Cleanup(func() { statUID = orig })

	statUID = func(fi fs.FileInfo) (uint32, bool) { return uint32(os.Getuid()) + 1, true }
	if _, err := Listen(shortDir(t), testUnit, Options{PeerCheck: allowAll}); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Errorf("a directory owned by another uid: err = %v, want an owner refusal", err)
	}
	statUID = func(fi fs.FileInfo) (uint32, bool) { return 0, false }
	if _, err := Listen(shortDir(t), testUnit, Options{PeerCheck: allowAll}); err == nil {
		t.Error("an unreadable owner must refuse")
	}
	statUID = func(fi fs.FileInfo) (uint32, bool) { return uint32(os.Getuid()), true }
	s, err := Listen(shortDir(t), testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatalf("own uid refused: %v", err)
	}
	_ = s.Close()
}

func TestSocketPathLengthIsCheckedBeforeAnythingOnDisk(t *testing.T) {
	origStat, origDial := statUID, dialStale
	t.Cleanup(func() { statUID, dialStale = origStat, origDial })
	touched := false
	statUID = func(fs.FileInfo) (uint32, bool) { touched = true; return uint32(os.Getuid()), true }
	dialStale = func(string, string, time.Duration) (net.Conn, error) { touched = true; return nil, errors.New("no") }

	// The directory does not exist: a disk check would say "no such file", the
	// length check must say ErrOff first.
	long := "/tmp/" + strings.Repeat("d", 120)
	_, err := Listen(long, testUnit, Options{})
	if !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v, want ErrOff before any disk access", err)
	}
	if touched {
		t.Error("the disk was consulted before the length check")
	}
	if _, serr := os.Lstat(long); !os.IsNotExist(serr) {
		t.Errorf("Listen created or found %s: %v", long, serr)
	}
}

// staleSocket leaves a socket file nobody listens on in a fresh directory.
func staleSocket(t *testing.T) (dir, path string) {
	t.Helper()
	dir = shortDir(t)
	path = filepath.Join(dir, bridge.SocketName)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	return dir, path
}

func TestStaleProbeUsesAOneSecondTimeout(t *testing.T) {
	orig := dialStale
	t.Cleanup(func() { dialStale = orig })
	var got time.Duration
	dialStale = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		got = timeout
		return orig(network, addr, timeout)
	}
	dir, _ := staleSocket(t)
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if got != time.Second {
		t.Errorf("stale probe timeout = %v, want 1s", got)
	}
}

func TestStaleProbeErrorsOtherThanRefusedLeaveTheSocketAlone(t *testing.T) {
	orig := dialStale
	t.Cleanup(func() { dialStale = orig })
	for name, dialErr := range map[string]error{
		"timeout":     &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded},
		"EAGAIN":      &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EAGAIN)},
		"EACCES":      &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EACCES)},
		"other error": errors.New("something else"),
	} {
		dir, path := staleSocket(t)
		dialStale = func(string, string, time.Duration) (net.Conn, error) { return nil, dialErr }
		if _, err := Listen(dir, testUnit, Options{PeerCheck: allowAll}); err == nil {
			t.Errorf("%s: Listen replaced a socket it could not prove stale", name)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("%s: the socket file was removed: %v", name, err)
		}
	}
	// ECONNREFUSED is the one answer that proves it stale.
	dir, _ := staleSocket(t)
	dialStale = func(string, string, time.Duration) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatalf("a refused connect must replace the stale socket: %v", err)
	}
	_ = s.Close()
}

func TestCloseLeavesASuccessorsSocketAlone(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, bridge.SocketName)
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(context.Background()) }()

	// A successor takes the path over (the old file is replaced by a new inode).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	succ, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = succ.Close() })

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("Close removed the successor's socket: %v", err)
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the successor is no longer reachable: %v", err)
	}
	_ = c.Close()
}

func TestCloseStillRemovesItsOwnSocket(t *testing.T) {
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(context.Background()) }()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, bridge.SocketName)); !os.IsNotExist(err) {
		t.Errorf("own socket left behind: %v", err)
	}
}

func TestAnUnreadablePeerUIDRefusesBeforeTheExactCheck(t *testing.T) {
	orig := peerUID
	t.Cleanup(func() { peerUID = orig })
	peerUID = func(net.Conn) (int, error) { return 0, errors.New("no creds") }
	fake := &fakeExact{}
	dir, _, _ := startExact(t, writeCgroupFile(t, testCgroup), fake.check)
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readAll(t, c); !strings.HasPrefix(got, "ERR refused ") {
		t.Errorf("reply %q, want ERR refused", got)
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("the exact checker ran %d times for a peer whose uid is unknown", n)
	}
}

func TestListenRejectsUnitsThatAreNotPlainServices(t *testing.T) {
	for _, unit := range []string{"a/b.service", "../../x.service", "/x.service", "agent.service/", "x.service/y", "agent.scope", "agent"} {
		if _, err := Listen(shortDir(t), unit, Options{}); err == nil {
			t.Errorf("Listen accepted unit %q", unit)
		}
	}
}
