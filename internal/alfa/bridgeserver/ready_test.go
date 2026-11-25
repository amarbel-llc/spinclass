package bridgeserver

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

// readyGate is a Ready that a test flips by hand.
type readyGate struct {
	state atomic.Value // error
	calls atomic.Int64
}

func newReadyGate(initial error) *readyGate {
	g := &readyGate{}
	g.set(initial)
	return g
}

func (g *readyGate) set(err error) { g.state.Store(struct{ err error }{err}) }

func (g *readyGate) ready() error {
	g.calls.Add(1)
	return g.state.Load().(struct{ err error }).err
}

func serveOK() SessionHandler {
	return recordingHandler{fn: func(context.Context, net.Conn, Caller) {}}
}

// J-H1: clown writes the unit record only after the provider starts, and the
// provider starts only after the shim's control stream is ready. So control is
// admitted on identity alone, before any mapping or record exists.
func TestControlIsAdmittedBeforeTheSessionServiceIsReady(t *testing.T) {
	g := newReadyGate(&NotReadyError{Reason: "the principal mapping is not written yet"})
	_, dir := start(t, Options{Ready: g.ready, Handler: serveOK()})

	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Fatalf("control before readiness got %q, want OK", got)
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("control consulted the readiness check %d times; it must not", n)
	}
}

// A verb call before readiness is refused with ERR refused and nothing else,
// then admitted once the condition holds.
func TestTheSessionServiceIsRefusedUntilReadyThenAdmitted(t *testing.T) {
	g := newReadyGate(&NotReadyError{Reason: "the principal mapping is not written yet"})
	_, dir := start(t, Options{Ready: g.ready, Handler: serveOK()})

	c := dial(t, dir)
	send(t, c, "BRIDGE/1 session\n")
	want := string(bridge.FormatErr(bridge.CodeRefused, NotReadyReasonText("the principal mapping is not written yet")))
	if got := readAll(t, c); got != want {
		t.Fatalf("reply %q, want exactly %q", got, want)
	}
	if !strings.HasPrefix(want, "ERR refused ") || strings.Count(want, "\n") != 1 {
		t.Fatalf("the refusal is not one ERR refused line: %q", want)
	}

	g.set(nil)
	c2 := dial(t, dir)
	send(t, c2, "BRIDGE/1 session\n")
	if got := readN(t, c2, 3); got != "OK\n" {
		t.Fatalf("session after readiness got %q, want OK", got)
	}
}

// Readiness is consulted only for a peer the exact check already admitted: a
// foreign peer learns nothing about the session's state.
func TestAForeignPeerIsRefusedWithoutAskingReadiness(t *testing.T) {
	g := newReadyGate(&NotReadyError{Reason: "x"})
	file := writeCgroupFile(t, testCgroup)
	fake := &fakeExact{err: errors.New("not in the unit")}
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{CheckPeer: fake.check, CgroupFile: file, Ready: g.ready, Handler: serveOK()})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })

	c := dial(t, dir)
	send(t, c, "BRIDGE/1 session\n")
	want := string(bridge.FormatErr(bridge.CodeRefused, RefusedPeerText))
	if got := readAll(t, c); got != want {
		t.Errorf("reply %q, want the plain peer refusal %q", got, want)
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("readiness was consulted %d times for a peer that is not in the unit", n)
	}
}

// A hard failure (the mapping names another session, a fresh record names
// another unit) ends the listener; it does not wait for a connection.
func TestAHardReadinessFailureOnATickEndsTheListener(t *testing.T) {
	g := newReadyGate(&NotReadyError{Reason: "waiting"})
	ticks := make(chan time.Time)
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{
		PeerCheck: allowAll, Ready: g.ready, Handler: serveOK(),
		Tick: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })

	ticks <- time.Now() // not ready is not death
	select {
	case err := <-done:
		t.Fatalf("a listener that is only not ready died: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	g.set(errors.New("the principal's mapping names beta/b, not alpha/a"))
	ticks <- time.Now()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "names beta/b") {
			t.Errorf("Serve returned %v, want the failure naming the foreign session", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the listener survived a mapping that names another session")
	}
}

// The same hard failure met by a verb call refuses that call and ends the
// listener.
func TestAHardReadinessFailureOnAVerbCallRefusesItAndEndsTheListener(t *testing.T) {
	g := newReadyGate(errors.New("a record written after this serve started names another unit"))
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll, Ready: g.ready, Handler: serveOK()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })

	c := dial(t, dir)
	send(t, c, "BRIDGE/1 session\n")
	if got := readAll(t, c); !strings.HasPrefix(got, "ERR refused ") {
		t.Errorf("reply %q, want ERR refused", got)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "another unit") {
			t.Errorf("Serve returned %v, want the failure", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the listener survived a record naming another unit")
	}
}

// The unit check from the environment alone: the cgroup file's value must name
// CLOWN_BRIDGE_UNIT; no unit record is read.
func TestACgroupFileThatDoesNotNameTheUnitAdmitsNobody(t *testing.T) {
	file := writeCgroupFile(t, "/user.slice/clown-agent.slice/some-other.service")
	fake := &fakeExact{}
	dir, _, _ := startExact(t, file, fake.check)
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	want := string(bridge.FormatErr(bridge.CodeRefused, RefusedPeerText))
	if got := readAll(t, c); got != want {
		t.Errorf("reply %q, want %q", got, want)
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("the exact checker ran %d times against a cgroup that is not the unit's", n)
	}
}
