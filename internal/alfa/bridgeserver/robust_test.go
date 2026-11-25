package bridgeserver

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

// scriptedAcceptor fails n times with a temporary error, then accepts for real.
type scriptedAcceptor struct {
	real acceptor
	mu   sync.Mutex
	left int
}

func (a *scriptedAcceptor) Accept() (net.Conn, error) {
	a.mu.Lock()
	if a.left > 0 {
		a.left--
		a.mu.Unlock()
		return nil, &net.OpError{Op: "accept", Net: "unix", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	}
	a.mu.Unlock()
	return a.real.Accept()
}
func (a *scriptedAcceptor) Close() error { return a.real.Close() }

func TestNextBackoffGrowsFromFiveMillisecondsAndStopsAtOneSecond(t *testing.T) {
	var got []time.Duration
	d := time.Duration(0)
	for i := 0; i < 11; i++ {
		d = nextBackoff(d)
		got = append(got, d)
	}
	want := []time.Duration{5, 10, 20, 40, 80, 160, 320, 640, 1000, 1000, 1000}
	for i := range want {
		if got[i] != want[i]*time.Millisecond {
			t.Fatalf("step %d = %v, want %v (all: %v)", i, got[i], want[i]*time.Millisecond, got)
		}
	}
}

func TestTemporaryAcceptErrorsAreWaitedOutNotFatal(t *testing.T) {
	var mu sync.Mutex
	var slept []time.Duration
	orig := sleepFor
	sleepFor = func(_ context.Context, d time.Duration) bool {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return true
	}
	t.Cleanup(func() { sleepFor = orig })

	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatal(err)
	}
	s.l = &scriptedAcceptor{real: s.l, left: 10}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })

	// The listener is still serving once the temporary failures are past.
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Fatalf("reply %q after temporary accept errors, want OK", got)
	}
	select {
	case err := <-done:
		t.Fatalf("Serve returned %v on temporary accept errors", err)
	default:
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) != 10 || slept[0] != 5*time.Millisecond || slept[8] != time.Second || slept[9] != time.Second {
		t.Errorf("the pauses were %v, want 5ms doubling to a 1s cap, ten of them", slept)
	}
}

func TestConnectionsOverTheCapAreAnsweredBusyAndObservedOnce(t *testing.T) {
	obs := &recordingObserver{}
	s, dir := start(t, Options{Observer: obs, HelloTimeout: time.Minute})
	var held []net.Conn
	for i := 0; i < MaxConnections; i++ {
		held = append(held, dial(t, dir))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.conns)
		s.mu.Unlock()
		if n == MaxConnections {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the listener holds %d of %d connections", n, MaxConnections)
		}
		time.Sleep(5 * time.Millisecond)
	}
	extra := dial(t, dir)
	got := readAll(t, extra)
	if !strings.HasPrefix(got, "ERR busy ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("the connection over the cap got %q, want ERR busy", got)
	}
	n := waitEventCount(t, obs, 1)
	obs.mu.Lock()
	events := append([]Event(nil), obs.events...)
	obs.mu.Unlock()
	if n != 1 || len(events) != 1 || events[0].Outcome != bridge.CodeBusy {
		t.Errorf("events = %+v, want exactly one busy", events)
	}
	// Freeing a slot makes room again.
	_ = held[0].Close()
	deadline = time.Now().Add(5 * time.Second)
	for {
		c := dial(t, dir)
		_, _ = c.Write([]byte("BRIDGE/1 control\n")) // a still-full server may already have closed
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		b := make([]byte, 3)
		_, rerr := c.Read(b)
		_ = c.Close()
		if rerr == nil && string(b) == "OK\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no connection was served after a slot was freed")
		}
	}
}

// waitEventCount waits until the observer holds at least n events, then waits a
// moment more for strays, and returns the count.
func waitEventCount(t *testing.T, o *recordingObserver, n int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		o.mu.Lock()
		c := len(o.events)
		o.mu.Unlock()
		if c >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.events)
}

type panicHandler struct{}

func (panicHandler) Serve(context.Context, net.Conn, Caller) { panic("handler bug") }

func TestEveryConnectionIsObservedExactlyOnce(t *testing.T) {
	orig := controlProbeInterval
	controlProbeInterval = 20 * time.Millisecond
	t.Cleanup(func() { controlProbeInterval = orig })

	obs := &recordingObserver{}
	var refuse atomic.Bool
	_, dir := start(t, Options{
		Observer:     obs,
		HelloTimeout: 150 * time.Millisecond,
		Handler:      panicHandler{},
		PeerCheck: func(net.Conn, string) error {
			if refuse.Load() {
				return errors.New("not the unit")
			}
			return nil
		},
	})

	steps := []struct {
		name string
		want string
		run  func(t *testing.T)
	}{
		{"a peer that connects and leaves", bridge.CodeError, func(t *testing.T) {
			c := dial(t, dir)
			_ = c.Close()
		}},
		{"a malformed hello", bridge.CodeError, func(t *testing.T) {
			c := dial(t, dir)
			send(t, c, "nonsense\n")
			_ = readAll(t, c)
		}},
		{"no hello", "hello-timeout", func(t *testing.T) {
			c := dial(t, dir)
			_ = readAll(t, c)
		}},
		{"an unknown service", bridge.CodeUnknownService, func(t *testing.T) {
			c := dial(t, dir)
			send(t, c, "BRIDGE/1 nope\n")
			_ = readAll(t, c)
		}},
		{"a refused peer", bridge.CodeRefused, func(t *testing.T) {
			refuse.Store(true)
			defer refuse.Store(false)
			c := dial(t, dir)
			send(t, c, "BRIDGE/1 control\n")
			_ = readAll(t, c)
		}},
		{"a served control stream the peer closes", "ok", func(t *testing.T) {
			c := dial(t, dir)
			send(t, c, "BRIDGE/1 control\n")
			if got := readN(t, c, 3); got != "OK\n" {
				t.Fatalf("reply %q", got)
			}
			_ = c.Close()
		}},
		{"a handler that panics", bridge.CodeError, func(t *testing.T) {
			c := dial(t, dir)
			send(t, c, "BRIDGE/1 session\n")
			_ = readN(t, c, 3)
			_, _ = c.Read(make([]byte, 1))
		}},
	}
	for i, st := range steps {
		st.run(t)
		if n := waitEventCount(t, obs, i+1); n != i+1 {
			t.Fatalf("%s: %d events, want exactly %d", st.name, n, i+1)
		}
		obs.mu.Lock()
		got := obs.events[i].Outcome
		obs.mu.Unlock()
		if got != st.want {
			t.Errorf("%s: outcome %q, want %q", st.name, got, st.want)
		}
	}
	// The listener survived the panic.
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Errorf("after a handler panic the listener answered %q", got)
	}
}

func TestControlStreamSurvivesThePeersWriteEOFAndEndsWhenThePeerIsGone(t *testing.T) {
	orig := controlProbeInterval
	controlProbeInterval = 20 * time.Millisecond
	t.Cleanup(func() { controlProbeInterval = orig })
	s, dir := start(t, Options{})

	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Fatalf("reply %q", got)
	}
	if err := c.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond) // many probe intervals
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err = s.Push(Frame{Type: "ping"}); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the control stream ended on the peer's write-EOF: %v", err)
	}
	want := `{"type":"ping"}` + "\n"
	if got := readN(t, c, len(want)); got != want {
		t.Errorf("frame %q, want %q", got, want)
	}

	// A peer that is gone frees the control slot.
	_ = c.Close()
	deadline = time.Now().Add(3 * time.Second)
	for {
		c2 := dial(t, dir)
		// While the slot is still held the server answers ERR busy and closes,
		// so this write may meet a closed peer; that is the retry signal, not a failure.
		_, _ = c2.Write([]byte("BRIDGE/1 control\n"))
		_ = c2.SetReadDeadline(time.Now().Add(time.Second))
		b := make([]byte, 3)
		_, rerr := c2.Read(b)
		_ = c2.Close()
		if rerr == nil && string(b) == "OK\n" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the control slot was never freed after the peer closed")
		}
	}
}

func TestCloseIsBoundedWhenAHandlerIgnoresItsContextAndItsConnection(t *testing.T) {
	orig := closeWait
	closeWait = 150 * time.Millisecond
	t.Cleanup(func() { closeWait = orig })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	entered := make(chan struct{})
	var once sync.Once
	s, dir := start(t, Options{Handler: recordingHandler{fn: func(context.Context, net.Conn, Caller) {
		once.Do(func() { close(entered) })
		<-release
	}}})
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 session\n")
	_ = readN(t, c, 3)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never ran")
	}
	started := time.Now()
	err := s.Close()
	if d := time.Since(started); d > 3*time.Second {
		t.Errorf("Close took %v with a stuck handler", d)
	}
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("Close = %v, want the error that handlers were still running", err)
	}
	if _, serr := os.Lstat(s.Path()); !os.IsNotExist(serr) {
		t.Errorf("the socket remains after a bounded close: %v", serr)
	}
}
