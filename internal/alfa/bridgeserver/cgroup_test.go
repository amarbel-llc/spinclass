package bridgeserver

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

const testCgroup = "/user.slice/user-1000.slice/user@1000.service/clown-agent.slice/agent-test.service"

// writeCgroupFile writes content as a 0600 file in a fresh directory outside
// any bridge dir, and returns its path.
func writeCgroupFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(shortDir(t), "bridge.cgroup")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeExact is a recording stand-in for peercheck.CheckPeerExact.
type fakeExact struct {
	mu    sync.Mutex
	wants []string
	err   error
}

func (f *fakeExact) check(_ net.Conn, want string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wants = append(f.wants, want)
	return f.err
}

func (f *fakeExact) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.wants...)
}

// startExact serves a listener with the exact-check seam, a cgroup file path
// and a hand-driven tick.
func startExact(t *testing.T, cgFile string, check func(net.Conn, string) error) (dir string, ticks chan time.Time, done chan error) {
	t.Helper()
	dir = shortDir(t)
	ticks = make(chan time.Time)
	s, err := Listen(dir, testUnit, Options{
		CheckPeer:  check,
		CgroupFile: cgFile,
		Tick:       func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	done = make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })
	return dir, ticks, done
}

func TestListenWithoutAnExactPeerCheckRefusesToStart(t *testing.T) {
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{})
	if err == nil {
		_ = s.Close()
		t.Fatal("Listen started with no peer check wired")
	}
	if !errors.Is(err, ErrNoPeerCheck) {
		t.Errorf("err = %v, want ErrNoPeerCheck", err)
	}
	if !strings.Contains(err.Error(), "the bridge needs peercheck.CheckPeerExact from a merged clown; none is wired in this build") {
		t.Errorf("the reason is not the named one: %v", err)
	}
	if _, serr := os.Lstat(filepath.Join(dir, bridge.SocketName)); !os.IsNotExist(serr) {
		t.Errorf("a socket was bound without a peer check: %v", serr)
	}
	// A cgroup file without a checker is still no bridge.
	if s, err := Listen(shortDir(t), testUnit, Options{CgroupFile: writeCgroupFile(t, testCgroup)}); err == nil {
		_ = s.Close()
		t.Fatal("Listen started with a cgroup file but no checker")
	}
}

func TestListenRefusesACgroupFileInsideTheBridgeDir(t *testing.T) {
	dir := shortDir(t)
	for _, f := range []string{"", "relative/file", filepath.Join(dir, "bridge.cgroup"), filepath.Join(dir, "sub", "x"), dir + "/../" + filepath.Base(dir) + "/c"} {
		s, err := Listen(dir, testUnit, Options{CheckPeer: (&fakeExact{}).check, CgroupFile: f})
		if err == nil {
			_ = s.Close()
			t.Errorf("Listen accepted the cgroup file %q", f)
		}
	}
	if _, serr := os.Lstat(filepath.Join(dir, bridge.SocketName)); !os.IsNotExist(serr) {
		t.Errorf("a socket was bound for a refused configuration: %v", serr)
	}
}

func TestReadCgroupFileAcceptsOnlyTheExactPath(t *testing.T) {
	good := writeCgroupFile(t, testCgroup)
	got, err := ReadCgroupFile(good)
	if err != nil || got != testCgroup {
		t.Fatalf("ReadCgroupFile = %q, %v", got, err)
	}
	// One trailing newline is tolerated on read (the shared admission vectors);
	// writers MUST NOT write one, and clown writes none.
	if got, err := ReadCgroupFile(writeCgroupFile(t, testCgroup+"\n")); err != nil || got != testCgroup {
		t.Errorf("a file with one trailing newline: ReadCgroupFile = %q, %v; want the path", got, err)
	}
	for name, content := range map[string]string{
		"empty":             "",
		"root":              "/",
		"two newlines":      testCgroup + "\n\n",
		"only a newline":    "\n",
		"newline then text": testCgroup + "\n/x",
		"trailing slash":    testCgroup + "/",
		"relative":          "user.slice/x.service",
		"dot dot":           "/a/../b",
		"control character": "/a/b\x00c",
		"two lines":         "/a\n/b",
		"too long":          "/" + strings.Repeat("a", 5000),
	} {
		if _, err := ReadCgroupFile(writeCgroupFile(t, content)); err == nil {
			t.Errorf("%s: accepted %q", name, content)
		}
	}
	if _, err := ReadCgroupFile(filepath.Join(shortDir(t), "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file must wrap ErrNotExist: %v", err)
	}
	loose := writeCgroupFile(t, testCgroup)
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCgroupFile(loose); err == nil {
		t.Error("a 0644 cgroup file was accepted")
	}
	link := filepath.Join(shortDir(t), "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCgroupFile(link); err == nil {
		t.Error("a symlinked cgroup file was followed")
	}
	if _, err := ReadCgroupFile(shortDir(t)); err == nil {
		t.Error("a directory was accepted as the cgroup file")
	}
}

func TestPeerIsRefusedWhileTheCgroupFileIsMissingOrInvalid(t *testing.T) {
	loose := writeCgroupFile(t, testCgroup)
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(shortDir(t), "link")
	if err := os.Symlink(writeCgroupFile(t, testCgroup), link); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing":      filepath.Join(shortDir(t), "never-written"),
		"empty":        writeCgroupFile(t, ""),
		"two newlines": writeCgroupFile(t, testCgroup+"\n\n"),
		"relative":     writeCgroupFile(t, "not/absolute"),
		"mode 0644":    loose,
		"symlink":      link,
		"a directory":  shortDir(t),
	}
	want := string(bridge.FormatErr(bridge.CodeRefused, NotReadyText))
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakeExact{}
			dir, _, _ := startExact(t, file, fake.check)
			c := dial(t, dir)
			send(t, c, "BRIDGE/1 control\n")
			if got := readAll(t, c); got != want {
				t.Errorf("reply %q, want exactly %q", got, want)
			}
			if n := len(fake.calls()); n != 0 {
				t.Errorf("the exact checker ran %d times with no valid cgroup file", n)
			}
		})
	}
}

func TestPeerIsCheckedAgainstTheExactCgroupAndRefusedOnError(t *testing.T) {
	file := writeCgroupFile(t, testCgroup)
	fake := &fakeExact{}
	dir, _, _ := startExact(t, file, fake.check)

	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Fatalf("reply %q, want OK", got)
	}
	if got := fake.calls(); len(got) != 1 || got[0] != testCgroup {
		t.Errorf("the checker saw %q, want the file's exact cgroup %q", got, testCgroup)
	}

	// The file is re-read on every connection: a new content is the new want.
	const other = "/another.slice/clown-agent.slice/agent-test.service"
	if err := os.WriteFile(file, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.err = errors.New("pid 7 runs in /elsewhere, not exactly " + other)
	fake.mu.Unlock()
	c2 := dial(t, dir)
	send(t, c2, "BRIDGE/1 session\n")
	wantRefused := string(bridge.FormatErr(bridge.CodeRefused, RefusedPeerText))
	if got := readAll(t, c2); got != wantRefused {
		t.Errorf("reply %q, want %q", got, wantRefused)
	}
	if got := fake.calls(); len(got) != 2 || got[1] != other {
		t.Errorf("the second connection was checked against %q, want %q", got, other)
	}
}

func TestCgroupFileRemovedAfterBeingValidClosesTheListener(t *testing.T) {
	file := writeCgroupFile(t, testCgroup)
	_, ticks, done := startExact(t, file, (&fakeExact{}).check)

	ticks <- time.Now() // a valid file is seen
	select {
	case err := <-done:
		t.Fatalf("Serve ended on a valid cgroup file: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cgroup file") {
			t.Errorf("Serve returned %v, want the dead-listener error naming the cgroup file", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the listener kept running after its cgroup file was removed")
	}
}

func TestCgroupFileNeverWrittenIsNotDeath(t *testing.T) {
	_, ticks, done := startExact(t, filepath.Join(shortDir(t), "never"), (&fakeExact{}).check)
	for i := 0; i < 3; i++ {
		ticks <- time.Now()
	}
	select {
	case err := <-done:
		t.Fatalf("a bridge waiting for its cgroup file died: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSocketRemovedClosesAnExactListenerToo(t *testing.T) {
	dir, ticks, done := startExact(t, writeCgroupFile(t, testCgroup), (&fakeExact{}).check)
	if err := os.Remove(filepath.Join(dir, bridge.SocketName)); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil after its socket was removed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the listener kept running after bridge.sock was removed")
	}
}
