package bridgeserver

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

const testUnit = "agent-test.service"

// shortDir is a 0700 directory with a path short enough for a unix socket.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "bs")
	if err != nil {
		d, err = os.MkdirTemp("", "bs")
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func allowAll(net.Conn, string) error { return nil }

// recordingHandler hands each served connection to fn.
type recordingHandler struct {
	fn func(ctx context.Context, conn net.Conn, caller Caller)
}

func (h recordingHandler) Serve(ctx context.Context, conn net.Conn, caller Caller) {
	h.fn(ctx, conn, caller)
}

func start(t *testing.T, opts Options) (*Server, string) {
	t.Helper()
	dir := shortDir(t)
	if opts.PeerCheck == nil {
		opts.PeerCheck = allowAll
	}
	s, err := Listen(dir, testUnit, opts)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = s.Serve(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func dial(t *testing.T, dir string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", filepath.Join(dir, bridge.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func send(t *testing.T, c net.Conn, s string) {
	t.Helper()
	if _, err := c.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

// readAll reads until EOF (the server closes after an ERR) with a deadline.
func readAll(t *testing.T, c net.Conn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v (got %q)", err, b)
	}
	return string(b)
}

func readN(t *testing.T, c net.Conn, n int) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatalf("read %d: %v (got %q)", n, err, b)
	}
	return string(b)
}

func TestListenCreatesSocket0600InPrivateDir(t *testing.T) {
	_, dir := start(t, Options{})
	fi, err := os.Lstat(filepath.Join(dir, bridge.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("not a socket: %v", fi.Mode())
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode %o, want 600", perm)
	}
}

func TestListenRefusesLiveSocket(t *testing.T) {
	_, dir := start(t, Options{})
	_, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err == nil || !strings.Contains(err.Error(), "live") {
		t.Fatalf("second Listen: err = %v, want a live-socket refusal", err)
	}
	// The first listener's socket must survive the refused second attempt.
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Errorf("first listener broken after refusal: %q", got)
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, bridge.SocketName)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close() // leaves the socket file behind, nobody listening
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("stale socket not left behind: %v", err)
	}
	s, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	_ = s.Close()
}

func TestListenRefusesNonSocketAtPath(t *testing.T) {
	dir := shortDir(t)
	if err := os.WriteFile(filepath.Join(dir, bridge.SocketName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(dir, testUnit, Options{}); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
}

func TestListenRefusesBadDirectories(t *testing.T) {
	base := shortDir(t)
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"group-readable": loose,
		"symlink":        link,
		"relative":       "relative/dir",
		"missing":        filepath.Join(base, "missing"),
		"regular file":   file,
		"empty":          "",
	} {
		if _, err := Listen(dir, testUnit, Options{}); err == nil {
			t.Errorf("%s: Listen accepted %q", name, dir)
		} else if errors.Is(err, ErrOff) {
			t.Errorf("%s: refusal must not read as the path-length off switch: %v", name, err)
		}
	}
}

func TestListenOffWhenSocketPathTooLong(t *testing.T) {
	dir := shortDir(t)
	long := filepath.Join(dir, strings.Repeat("d", 100))
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Listen(long, testUnit, Options{})
	if !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v, want ErrOff", err)
	}
	if !strings.Contains(err.Error(), "107") {
		t.Errorf("message should name the limit: %v", err)
	}
}

func TestListenRefusesBadUnitName(t *testing.T) {
	for _, unit := range []string{"", "agent.scope", "../x.service", "a b.service", "-x.service", ".service", "a/b.service"} {
		if _, err := Listen(shortDir(t), unit, Options{}); err == nil {
			t.Errorf("Listen accepted unit %q", unit)
		}
	}
}

func TestCloseRemovesSocket(t *testing.T) {
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
		t.Errorf("socket still there after Close: %v", err)
	}
}

func TestHelloTimeoutClosesWithNoReply(t *testing.T) {
	_, dir := start(t, Options{HelloTimeout: 100 * time.Millisecond})
	c := dial(t, dir)
	if got := readAll(t, c); got != "" {
		t.Errorf("reply to a silent peer: %q", got)
	}
}

func TestOverLongHelloRefused(t *testing.T) {
	_, dir := start(t, Options{})
	c := dial(t, dir)
	// Exactly the bound with no newline: the server reads no further, so
	// nothing is left unread to turn its close into a reset.
	send(t, c, strings.Repeat("a", bridge.MaxHelloBytes))
	if got, want := readAll(t, c), string(bridge.MalformedReply()); got != want {
		t.Errorf("reply %q, want %q", got, want)
	}
}

func TestUnknownServiceRefused(t *testing.T) {
	_, dir := start(t, Options{})
	for _, svc := range []string{"egress", "nope"} {
		c := dial(t, dir)
		send(t, c, "BRIDGE/1 "+svc+"\n")
		if got, want := readAll(t, c), "ERR unknown-service "+svc+"\n"; got != want {
			t.Errorf("%s: reply %q, want %q", svc, got, want)
		}
	}
}

func TestVersionMismatchReplyBytes(t *testing.T) {
	_, dir := start(t, Options{})
	c := dial(t, dir)
	send(t, c, "BRIDGE/2 session\n")
	want := "ERR version server=1 shim=2: rebuild or restart the session so the shim and the frontend match\n"
	if got := readAll(t, c); got != want {
		t.Errorf("reply %q, want %q", got, want)
	}
}

func TestReplyIsExactlyOKBeforeAnyHandlerByte(t *testing.T) {
	h := recordingHandler{fn: func(_ context.Context, conn net.Conn, _ Caller) {
		_, _ = conn.Write([]byte("svc-byte"))
	}}
	_, dir := start(t, Options{Handler: h})
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 session\n")
	if got := readAll(t, c); got != "OK\nsvc-byte" {
		t.Errorf("stream %q, want OK\\n then the handler's bytes", got)
	}
}

func TestHandlerReceivesTheCallerTheListenerBuilt(t *testing.T) {
	got := make(chan Caller, 1)
	h := recordingHandler{fn: func(_ context.Context, _ net.Conn, caller Caller) { got <- caller }}
	_, dir := start(t, Options{Handler: h, SessionKey: "repo/branch"})
	c := dial(t, dir)
	// Identity claimed on the stream must not matter.
	send(t, c, "BRIDGE/1 session\n{\"session\":\"evil/other\"}\n")
	select {
	case caller := <-got:
		want := Caller{SessionKey: "repo/branch", Unit: testUnit, Transport: "bridge"}
		if caller != want {
			t.Errorf("caller %+v, want %+v", caller, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler never ran")
	}
}

func TestDefaultServedSetIsControlAndSession(t *testing.T) {
	_, dir := start(t, Options{})
	for _, svc := range []string{"hook", "chat", "model", "keys", "status"} {
		c := dial(t, dir)
		send(t, c, "BRIDGE/1 "+svc+"\n")
		if got := readAll(t, c); got != "ERR unknown-service "+svc+"\n" {
			t.Errorf("%s: %q", svc, got)
		}
	}
	_, dir2 := start(t, Options{Served: func() map[string]bool { return map[string]bool{"session": true} }})
	c := dial(t, dir2)
	send(t, c, "BRIDGE/1 control\n")
	if got := readAll(t, c); got != "ERR unknown-service control\n" {
		t.Errorf("custom served set ignored: %q", got)
	}
}

func TestStreamCapAnswersBusyOn65th(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h := recordingHandler{fn: func(ctx context.Context, _ net.Conn, _ Caller) {
		select {
		case <-release:
		case <-ctx.Done():
		}
	}}
	_, dir := start(t, Options{Handler: h})
	for i := 0; i < bridge.MaxStreamsPerSession; i++ {
		c := dial(t, dir)
		send(t, c, "BRIDGE/1 session\n")
		if got := readN(t, c, 3); got != "OK\n" {
			t.Fatalf("stream %d: %q", i+1, got)
		}
	}
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 session\n")
	if got := readAll(t, c); !strings.HasPrefix(got, "ERR busy ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("65th stream: %q, want ERR busy", got)
	}
}

func TestSecondControlStreamAnswersBusy(t *testing.T) {
	_, dir := start(t, Options{})
	first := dial(t, dir)
	send(t, first, "BRIDGE/1 control\n")
	if got := readN(t, first, 3); got != "OK\n" {
		t.Fatal(got)
	}
	second := dial(t, dir)
	send(t, second, "BRIDGE/1 control\n")
	if got := readAll(t, second); !strings.HasPrefix(got, "ERR busy ") {
		t.Errorf("second control: %q", got)
	}
	// Once the first closes, the slot frees.
	_ = first.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c := dial(t, dir)
		send(t, c, "BRIDGE/1 control\n")
		got := readN(t, c, 3)
		if got == "OK\n" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("control slot never freed: %q", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestControlStaysOpenUntilCloseAndEOFReachesPeer(t *testing.T) {
	s, dir := start(t, Options{})
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Fatal(got)
	}
	_ = c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("control stream was not left open: err=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(b[:]); !errors.Is(err, io.EOF) {
		t.Errorf("after Close the peer saw %v, want EOF", err)
	}
}

func TestPushWritesAnNDJSONFrameToTheControlStream(t *testing.T) {
	s, dir := start(t, Options{})
	if err := s.Push(Frame{Type: "wake"}); !errors.Is(err, ErrNoControl) {
		t.Errorf("Push without a control stream: %v, want ErrNoControl", err)
	}
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Fatal(got)
	}
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err = s.Push(Frame{Type: "restart", Payload: []byte(`{"in":5}`)}); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond) // the control slot registers just after OK
	}
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"restart","payload":{"in":5}}` + "\n"
	if got := readN(t, c, len(want)); got != want {
		t.Errorf("frame %q, want %q", got, want)
	}
}

func TestPeerOutsideTheUnitIsRefusedAndCounted(t *testing.T) {
	var sawUnit string
	check := func(_ net.Conn, unit string) error {
		sawUnit = unit
		return errors.New("pid 4242 is not in the cgroup")
	}
	s, dir := start(t, Options{PeerCheck: check})
	c := dial(t, dir)
	// Even a perfect hello gets nothing but the refusal.
	send(t, c, "BRIDGE/1 session\n")
	want := "ERR refused refused: this connection does not come from this session's unit; the bridge answers only the unit it was created for\n"
	if got := readAll(t, c); got != want {
		t.Errorf("reply %q, want %q", got, want)
	}
	if sawUnit != testUnit {
		t.Errorf("check got unit %q, want %q", sawUnit, testUnit)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.Refused() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.Refused(); n != 1 {
		t.Errorf("Refused() = %d, want 1", n)
	}
}

type recordingObserver struct {
	mu     sync.Mutex
	events []Event
}

func (o *recordingObserver) Crossing(e Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

func TestObserverSeesOutcomes(t *testing.T) {
	obs := &recordingObserver{}
	_, dir := start(t, Options{Observer: obs})
	c := dial(t, dir)
	send(t, c, "BRIDGE/1 nope\n")
	_ = readAll(t, c)
	deadline := time.Now().Add(3 * time.Second)
	for {
		obs.mu.Lock()
		n := len(obs.events)
		obs.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.events) != 1 || obs.events[0].Outcome != bridge.CodeUnknownService {
		t.Errorf("events = %+v", obs.events)
	}
}

func TestEnvConfig(t *testing.T) {
	env := map[string]string{bridge.EnvDir: "/run/x", bridge.EnvUnit: "u.service"}
	get := func(k string) string { return env[k] }
	if d, u, ok := EnvConfig(get); !ok || d != "/run/x" || u != "u.service" {
		t.Errorf("both set: %q %q %v", d, u, ok)
	}
	delete(env, bridge.EnvUnit)
	if _, _, ok := EnvConfig(get); ok {
		t.Error("unit unset must read as not configured")
	}
	env[bridge.EnvUnit] = "u.service"
	delete(env, bridge.EnvDir)
	if _, _, ok := EnvConfig(get); ok {
		t.Error("dir unset must read as not configured")
	}
}
