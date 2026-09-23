package statsd

import (
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
	Count("merge.landed")
	got, ok := recv(t, conn)
	if !ok {
		t.Fatal("no datagram received")
	}
	if want := "spinclass.merge.landed:1|c"; got != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestCountDisabledKnob(t *testing.T) {
	conn := listen(t)
	t.Setenv("SPINCLASS_DISABLE_STATSD", "1")
	Count("merge.landed")
	if got, ok := recv(t, conn); ok {
		t.Errorf("SPINCLASS_DISABLE_STATSD=1 still sent %q", got)
	}
}

func TestEndpointOptIn(t *testing.T) {
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	t.Setenv("STATSD_HOST", "")
	t.Setenv("STATSD_PORT", "")
	if addr, ok := endpoint(); !ok || addr != "127.0.0.1:8125" {
		t.Errorf("present-but-empty env: endpoint = %q, %v; want 127.0.0.1:8125, true", addr, ok)
	}
}

func TestEndpointOffWithoutEnv(t *testing.T) {
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	for _, k := range []string{"STATSD_HOST", "STATSD_PORT"} {
		t.Setenv(k, "x") // registers restore
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := endpoint(); ok {
		t.Error("emission must be off when neither STATSD_HOST nor STATSD_PORT is set")
	}
}
