package statsd

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

// listen opens a loopback UDP listener and points STATSD_HOST/PORT at it.
func listen(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	addr := conn.LocalAddr().(*net.UDPAddr)
	t.Setenv("STATSD_HOST", "127.0.0.1")
	t.Setenv("STATSD_PORT", strconv.Itoa(addr.Port))
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	return conn
}

func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	for _, k := range []string{"STATSD_HOST", "STATSD_PORT"} {
		t.Setenv(k, "x") // registers restore
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

func recv(t *testing.T, conn *net.UDPConn) (string, bool) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return "", false
	}
	return string(buf[:n]), true
}

func TestCountSendsPrefixedCounter(t *testing.T) {
	conn := listen(t)
	Resolve("", false).Count("merge.landed")
	got, ok := recv(t, conn)
	if !ok {
		t.Fatal("no datagram received")
	}
	if want := "spinclass.merge.landed:1|c"; got != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestTimingSendsWholeMilliseconds(t *testing.T) {
	conn := listen(t)
	Resolve("", false).Timing("merge.repo.rebase.ok", 1234*time.Millisecond+900*time.Microsecond)
	got, ok := recv(t, conn)
	if !ok {
		t.Fatal("no datagram received")
	}
	if want := "spinclass.merge.repo.rebase.ok:1234|ms"; got != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestGaugeSendsValue(t *testing.T) {
	conn := listen(t)
	Resolve("", false).Gauge("merge.repo.queue_depth", 3)
	got, ok := recv(t, conn)
	if !ok {
		t.Fatal("no datagram received")
	}
	if want := "spinclass.merge.repo.queue_depth:3|g"; got != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestZeroClientEmitsNothing(t *testing.T) {
	conn := listen(t)
	var c Client
	if c.Enabled() {
		t.Error("zero Client must be disabled")
	}
	c.Count("x")
	c.Timing("x", time.Second)
	if got, ok := recv(t, conn); ok {
		t.Errorf("zero Client sent %q", got)
	}
}

func TestResolveEnvKnobDisables(t *testing.T) {
	conn := listen(t)
	t.Setenv("SPINCLASS_DISABLE_STATSD", "1")
	c := Resolve(conn.LocalAddr().String(), false)
	if c.Enabled() {
		t.Error("SPINCLASS_DISABLE_STATSD=1 must beat the sweatfile address")
	}
}

func TestResolveSweatfileDisableBeatsAddressAndEnv(t *testing.T) {
	conn := listen(t)
	if Resolve(conn.LocalAddr().String(), true).Enabled() {
		t.Error("[metrics] disable-statsd must beat the address and the env")
	}
}

func TestResolveSweatfileAddressBeatsEnv(t *testing.T) {
	envConn := listen(t) // env points here
	addrConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = addrConn.Close() })

	Resolve(addrConn.LocalAddr().String(), false).Count("a")
	if _, ok := recv(t, addrConn); !ok {
		t.Fatal("sweatfile address received nothing")
	}
	if got, ok := recv(t, envConn); ok {
		t.Errorf("env endpoint received %q despite the sweatfile address", got)
	}
}

func TestResolveSweatfileAddressWorksWithoutEnv(t *testing.T) {
	clearEnv(t)
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	Resolve(conn.LocalAddr().String(), false).Count("a")
	if _, ok := recv(t, conn); !ok {
		t.Fatal("sweatfile address alone must enable emission")
	}
}

func TestResolveBareHostGetsDefaultPort(t *testing.T) {
	clearEnv(t)
	if got := Resolve("stats.example.com", false).addr; got != "stats.example.com:8125" {
		t.Errorf("bare host addr = %q, want stats.example.com:8125", got)
	}
	if got := Resolve("::1", false).addr; got != "[::1]:8125" {
		t.Errorf("bare ipv6 addr = %q, want [::1]:8125", got)
	}
	if got := Resolve("stats.example.com:9000", false).addr; got != "stats.example.com:9000" {
		t.Errorf("host:port addr = %q, want unchanged", got)
	}
}

func TestResolveEnvOptIn(t *testing.T) {
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	t.Setenv("STATSD_HOST", "")
	t.Setenv("STATSD_PORT", "")
	if got := Resolve("", false).addr; got != "127.0.0.1:8125" {
		t.Errorf("present-but-empty env: addr = %q; want 127.0.0.1:8125", got)
	}
}

func TestResolveOffWithoutAnything(t *testing.T) {
	clearEnv(t)
	if Resolve("", false).Enabled() {
		t.Error("emission must be off when nothing is configured")
	}
}

func TestSegment(t *testing.T) {
	for in, want := range map[string]string{
		"spinclass":    "spinclass",
		"My.Repo":      "my_repo",
		"a b/c:d":      "a_b_c_d",
		"keep-this_ok": "keep-this_ok",
		"":             "unknown",
	} {
		if got := Segment(in); got != want {
			t.Errorf("Segment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOutcome(t *testing.T) {
	live := context.Background()
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	boom := errors.New("boom")
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		err      error
		timedOut bool
		want     string
	}{
		{"ok", live, nil, false, OutcomeOK},
		{"ok beats cancelled ctx", dead, nil, false, OutcomeOK},
		{"failed", live, boom, false, OutcomeFailed},
		{"timeout", live, boom, true, OutcomeTimeout},
		{"cancelled", dead, boom, false, OutcomeCancelled},
		{"cancelled beats timeout", dead, boom, true, OutcomeCancelled},
	} {
		if got := Outcome(tc.ctx, tc.err, tc.timedOut); got != tc.want {
			t.Errorf("%s: Outcome = %q, want %q", tc.name, got, tc.want)
		}
	}
}
