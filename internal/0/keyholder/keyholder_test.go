package keyholder

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner stands in for systemd and the programs. Starting the fibby unit
// creates its two sockets and starting the agent unit creates its socket, as the
// real programs do, so the launcher's waits are real.
type fakeRunner struct {
	mu        sync.Mutex
	active    map[string]bool
	calls     []string // "start <unit>", "stop <unit>", "output <argv0 argv1>"
	listeners []net.Listener
	mintN     int
	mintErr   error
	stopStuck map[string]bool // units that stay active after Stop
	t         *testing.T
}

func newFake(t *testing.T) *fakeRunner {
	f := &fakeRunner{t: t, active: map[string]bool{}, stopStuck: map[string]bool{}}
	t.Cleanup(func() {
		for _, l := range f.listeners {
			_ = l.Close()
		}
	})
	return f
}

func (f *fakeRunner) sock(path string) {
	l, err := net.Listen("unix", path)
	if err != nil {
		f.t.Fatalf("fake socket %s: %v", path, err)
	}
	f.listeners = append(f.listeners, l)
}

func (f *fakeRunner) Start(_ context.Context, unit string, argv, env []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "start "+unit)
	f.active[unit] = true
	switch {
	case strings.Contains(unit, "fibby"):
		f.sock(argvAfter(argv, "--socket"))
		f.sock(argvAfter(argv, "--control-socket"))
	case strings.Contains(unit, "agent"):
		if !hasEnvPrefix(env, "PCSCLITE_CSOCK_NAME=") {
			f.t.Errorf("the agent unit has no PCSCLITE_CSOCK_NAME in its environment: %v", env)
		}
		f.sock(argvAfter(argv, "-a"))
	}
	return nil
}

func (f *fakeRunner) Active(_ context.Context, unit string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[unit], nil
}

func (f *fakeRunner) Stop(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop "+unit)
	if !f.stopStuck[unit] {
		f.active[unit] = false
	}
	return nil
}

func (f *fakeRunner) Output(_ context.Context, argv, env []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "output "+strings.Join(argv[1:min(3, len(argv))], " "))
	if len(argv) >= 3 && argv[1] == "holder" && argv[2] == "mint" {
		f.mintN++
		if f.mintErr != nil {
			return nil, f.mintErr
		}
		if !hasEnvPrefix(env, "SSH_AUTH_SOCK=") {
			f.t.Errorf("mint ran without SSH_AUTH_SOCK: %v", env)
		}
		return []byte("ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("key-blob-%d", f.mintN))) + " holder:reader\n"), nil
	}
	return nil, nil
}

func (f *fakeRunner) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func argvAfter(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func hasEnvPrefix(env []string, p string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, p) {
			return true
		}
	}
	return false
}

func launcher(t *testing.T) (*Launcher, *fakeRunner, *[]string) {
	t.Helper()
	// Short base: unix socket paths are limited to 107 bytes.
	base, err := os.MkdirTemp("", "kh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	f := newFake(t)
	var logs []string
	return &Launcher{
		Base: filepath.Join(base, "kh"), Tools: Tools{Fibby: "/pin/fibby", Piggy: "/pin/piggy"}, Run: f,
		Log: func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }, Wait: time.Second,
	}, f, &logs
}

func TestStartLaunchesFibbyThenTheHolderMintsOnceAndRecords(t *testing.T) {
	l, f, _ := launcher(t)
	p, err := l.Start(context.Background(), "spinclass/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.PublicKey, "ssh-ed25519 ") || p.AgentSock != filepath.Join(l.Base, Hash("spinclass/alpha"), "agent", "agent.sock") {
		t.Errorf("pair = %+v", p)
	}
	want := []string{"start spinclass-keyholder-fibby-" + Hash("spinclass/alpha") + ".service", "output ctl --socket", "start spinclass-keyholder-agent-" + Hash("spinclass/alpha") + ".service", "output holder mint"}
	if len(f.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, f.calls[i], want[i])
		}
	}
	if fi, err := os.Stat(filepath.Join(l.Base, Hash("spinclass/alpha"), "record.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record.json: %v %v", fi, err)
	}
}

func TestASecondStartWithALivePairStartsNothingAndMintsNothing(t *testing.T) {
	l, f, _ := launcher(t)
	p1, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	starts, mints := f.count("start "), f.mintN
	p2, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if f.count("start ") != starts || f.mintN != mints {
		t.Errorf("a second Start started %d more units and minted %d more keys", f.count("start ")-starts, f.mintN-mints)
	}
	if p1.PublicKey != p2.PublicKey {
		t.Errorf("the key changed across a second Start: %q then %q", p1.PublicKey, p2.PublicKey)
	}
}

func TestALostPairIsTornDownAndMintedAgainWithANewKeyAndSaysSo(t *testing.T) {
	l, f, logs := launcher(t)
	p1, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.active[p1.AgentUnit] = false // the holder died (a host reboot loses both)
	f.mu.Unlock()
	p2, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p2.PublicKey == p1.PublicKey {
		t.Error("a lost pair kept its key: keys do not survive a holder restart")
	}
	if f.mintN != 2 {
		t.Errorf("mints = %d, want 2", f.mintN)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "NEW key") {
		t.Errorf("the re-mint was not logged as new: %v", *logs)
	}
}

func TestStopStopsBothUnitsThenRemovesEveryDirectory(t *testing.T) {
	l, f, _ := launcher(t)
	if _, err := l.Start(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Base, Hash("s/a"))
	if err := l.Stop(context.Background(), "s/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the session directory survived Stop: %v", err)
	}
	if f.active["spinclass-keyholder-agent-"+Hash("s/a")+".service"] || f.active["spinclass-keyholder-fibby-"+Hash("s/a")+".service"] {
		t.Error("a unit is still active after Stop")
	}
	// The holder goes first: it holds fibby's directory lock.
	var stops []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "stop ") {
			stops = append(stops, c)
		}
	}
	if len(stops) != 2 || !strings.Contains(stops[0], "agent") || !strings.Contains(stops[1], "fibby") {
		t.Errorf("stop order = %v, want agent then fibby", stops)
	}
}

func TestStopKeepsTheDirectoriesWhenAUnitWillNotStop(t *testing.T) {
	l, f, _ := launcher(t)
	l.Wait = 100 * time.Millisecond
	p, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	f.stopStuck[p.FibbyUnit] = true
	err = l.Stop(context.Background(), "s/a")
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("Stop = %v, want an error that the unit is still running", err)
	}
	if _, serr := os.Lstat(p.Dir); serr != nil {
		t.Errorf("the directory was removed under a running fibby: %v", serr)
	}
}

func TestAFailedMintTearsEverythingDownAndLeavesNothing(t *testing.T) {
	l, f, _ := launcher(t)
	f.mintErr = errors.New("holder refused")
	if _, err := l.Start(context.Background(), "s/a"); err == nil || !strings.Contains(err.Error(), "holder refused") {
		t.Fatalf("Start = %v, want the mint error", err)
	}
	if _, err := os.Lstat(filepath.Join(l.Base, Hash("s/a"))); !os.IsNotExist(err) {
		t.Errorf("a failed start left its directory: %v", err)
	}
	for u, a := range f.active {
		if a {
			t.Errorf("unit %s is still active after a failed start", u)
		}
	}
}

func TestDirectoriesAreOwnerOnlyAndTheAgentDirectoryIsNotFibbys(t *testing.T) {
	l, _, _ := launcher(t)
	p, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.Dir, filepath.Join(p.Dir, "fibby"), filepath.Join(p.Dir, "agent"), filepath.Join(p.Dir, "work")} {
		fi, err := os.Lstat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v, want mode 0700", d, fi, err)
		}
	}
	if filepath.Dir(p.AgentSock) == filepath.Join(p.Dir, "fibby") {
		t.Error("the agent socket shares fibby's directory")
	}
}

func TestUnitNamesDeriveFromTheSessionKeyAndDifferPerSession(t *testing.T) {
	l, _, _ := launcher(t)
	a, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Start(context.Background(), "s/b")
	if err != nil {
		t.Fatal(err)
	}
	if a.AgentUnit == b.AgentUnit || a.FibbyUnit == b.FibbyUnit || a.AgentSock == b.AgentSock {
		t.Errorf("two sessions share a unit or a socket: %+v %+v", a, b)
	}
	if !strings.Contains(a.AgentUnit, Hash("s/a")) {
		t.Errorf("unit %q does not carry the session hash", a.AgentUnit)
	}
}

func TestAStartThatWouldOverflowTheSocketPathIsRefusedBeforeAnythingStarts(t *testing.T) {
	l, f, _ := launcher(t)
	l.Base = filepath.Join(l.Base, strings.Repeat("x", 100))
	_, err := l.Start(context.Background(), "s/a")
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("Start = %v, want a path-length refusal", err)
	}
	if f.count("start ") != 0 {
		t.Error("a unit was started before the path check")
	}
}

func TestRunningReportsOnlyAFullyLivePair(t *testing.T) {
	l, f, _ := launcher(t)
	if _, ok, _ := l.Running(context.Background(), "s/a"); ok {
		t.Error("Running reported a pair that was never started")
	}
	p, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := l.Running(context.Background(), "s/a"); !ok {
		t.Error("Running missed a live pair")
	}
	f.mu.Lock()
	f.active[p.FibbyUnit] = false
	f.mu.Unlock()
	if _, ok, _ := l.Running(context.Background(), "s/a"); ok {
		t.Error("Running reported a pair with a dead fibby")
	}
}
