package main

// J-H1: the bridge's start order. Clown starts serve first and waits for the
// socket; the unit record and the principal mapping exist only after the unit
// is up. So serve binds at once, control is admitted on identity alone, and the
// mapping and the record are readiness of the session service, per call. None
// of these tests seeds the mapping before starting the listener, which is what
// hid the bug.

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// startOrderFixture is one staged session whose frontend principal mapping is
// NOT written (a first launch: the unit is not up yet), and a listener started
// for it.
type startOrderFixture struct {
	s        stagedSession
	state    string
	dir      string
	stop     func()
	failures *[]string
}

func startOrder(t *testing.T, withMapping bool) startOrderFixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", e2ePrincipal)
	t.Setenv("SPINCLASS_SESSION_ID", sessionTestKey)
	f := startOrderFixture{state: os.Getenv("XDG_STATE_HOME")}
	f.s = stageSessionKey(t, sessionTestKey, e2ePrincipal)
	if !withMapping {
		if err := confinement.RemovePrincipalMapping(f.state, e2ePrincipal); err != nil {
			t.Fatal(err)
		}
	}
	f.failures = stubBridgeFailureHook(t)
	cg := wireTestPeerCheck(t, nil)
	f.dir = shortBridgeDir(t)
	old := bridgeResolve
	bridgeResolve = func() bridgePlan { return bridgePlan{Dir: f.dir, Unit: e2eUnit, CgroupFile: cg} }
	t.Cleanup(func() { bridgeResolve = old })
	f.stop = startBridge(context.Background(), e2eHandlerFactory())
	if f.stop == nil {
		t.Fatalf("the bridge did not bind (failures %v): it must bind at once, whatever the mapping and record say", *f.failures)
	}
	t.Cleanup(f.stop)
	return f
}

func (f startOrderFixture) writeMapping(t *testing.T, key string) {
	t.Helper()
	if err := confinement.RecordUnconfinedPrincipal(f.state, e2ePrincipal, key, f.s.wt); err != nil {
		t.Fatal(err)
	}
}

// hello dials the bridge and says hello for service; it returns what the
// listener answered first: "OK\n", or the whole ERR line.
func (f startOrderFixture) hello(t *testing.T, service string) string {
	t.Helper()
	c, err := net.Dial("unix", filepath.Join(f.dir, bridge.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("BRIDGE/1 " + service + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, 3)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatalf("hello %s: %v", service, err)
	}
	if string(b) == "OK\n" {
		return "OK\n"
	}
	rest, _ := io.ReadAll(c)
	return string(b) + string(rest)
}

func TestFirstLaunchBindsAndAdmitsControlBeforeAnyMappingExists(t *testing.T) {
	f := startOrder(t, false)
	if len(*f.failures) != 0 {
		t.Fatalf("a first launch with no mapping yet is not a failure: %v", *f.failures)
	}
	if got := f.hello(t, "control"); got != "OK\n" {
		t.Fatalf("control before the mapping got %q, want OK (clown's provider starts only after control is ready)", got)
	}
}

func TestAVerbCallBeforeTheMappingIsRefusedThenAdmittedOnceItIsWritten(t *testing.T) {
	f := startOrder(t, false)
	got := f.hello(t, "session")
	if !strings.HasPrefix(got, "ERR refused ") || strings.Count(got, "\n") != 1 || !strings.Contains(got, "mapping") {
		t.Fatalf("session before the mapping got %q, want one ERR refused line naming the mapping", got)
	}
	if len(*f.failures) != 0 {
		t.Fatalf("not ready is not a failure: %v", *f.failures)
	}
	f.writeMapping(t, sessionTestKey)
	if got := f.hello(t, "session"); got != "OK\n" {
		t.Fatalf("session after the mapping got %q, want OK", got)
	}
}

// A relaunch: the record on disk is the previous launch's and names the old
// unit. It was written before this serve started, so it is ignored.
func TestARelaunchIgnoresAStaleRecordThatNamesTheOldUnit(t *testing.T) {
	f := startOrder(t, true)
	orig := bridgeRecordedUnit
	t.Cleanup(func() { bridgeRecordedUnit = orig })
	bridgeRecordedUnit = func(string) (string, time.Time, bool) {
		return "clown-agent-previous-launch.service", time.Now().Add(-time.Hour), true
	}
	if got := f.hello(t, "session"); got != "OK\n" {
		t.Fatalf("session with a stale record naming the old unit got %q, want OK", got)
	}
	if len(*f.failures) != 0 {
		t.Fatalf("a stale record is not a failure: %v", *f.failures)
	}
}

func waitFailure(t *testing.T, failures *[]string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(*failures) > 0 {
			return (*failures)[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the listener never reported a failure")
	return ""
}

// A record written after this serve started that names another unit is a
// bridge for someone else's unit: hard failure, dead listener.
func TestAFreshRecordNamingAnotherUnitEndsTheListener(t *testing.T) {
	f := startOrder(t, true)
	orig := bridgeRecordedUnit
	t.Cleanup(func() { bridgeRecordedUnit = orig })
	bridgeRecordedUnit = func(string) (string, time.Time, bool) {
		return "clown-agent-someone-else.service", time.Now().Add(time.Minute), true
	}
	if got := f.hello(t, "session"); !strings.HasPrefix(got, "ERR refused ") {
		t.Fatalf("session with a fresh foreign record got %q, want ERR refused", got)
	}
	msg := waitFailure(t, f.failures)
	if !strings.HasPrefix(msg, sessionTestKey+"|") || !strings.Contains(msg, "clown-agent-someone-else.service") || !strings.Contains(msg, e2eUnit) {
		t.Errorf("failure %q, want one for %s naming both units", msg, sessionTestKey)
	}
}

// A mapping that names ANOTHER session is a hard failure at any time.
func TestAMappingNamingAnotherSessionEndsTheListener(t *testing.T) {
	f := startOrder(t, false)
	f.writeMapping(t, "beta-repo/b-branch")
	if got := f.hello(t, "session"); !strings.HasPrefix(got, "ERR refused ") {
		t.Fatalf("session with a foreign mapping got %q, want ERR refused", got)
	}
	msg := waitFailure(t, f.failures)
	if !strings.HasPrefix(msg, sessionTestKey+"|") || !strings.Contains(msg, "beta-repo/b-branch") {
		t.Errorf("failure %q, want one for %s naming the foreign session", msg, sessionTestKey)
	}
}

// No record at all is ready: the cgroup file, not the record, admits a peer.
func TestAMissingRecordIsNotAFailure(t *testing.T) {
	f := startOrder(t, true)
	orig := bridgeRecordedUnit
	t.Cleanup(func() { bridgeRecordedUnit = orig })
	bridgeRecordedUnit = func(string) (string, time.Time, bool) { return "", time.Time{}, false }
	if got := f.hello(t, "session"); got != "OK\n" {
		t.Fatalf("session with no record got %q, want OK", got)
	}
}
