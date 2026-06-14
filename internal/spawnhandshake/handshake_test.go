package spawnhandshake

import (
	"strings"
	"testing"
	"time"
)

// isolate pins XDG_STATE_HOME at a temp dir so the handshake store is
// per-test.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestHelloRoundTrip(t *testing.T) {
	isolate(t)
	const (
		driver = "spinclass/bright-cedar"
		worker = "otherrepo/calm-willow"
	)

	if err := SendHello(worker, driver); err != nil {
		t.Fatalf("SendHello: %v", err)
	}

	start := time.Now()
	if err := WaitForHello(driver, worker, time.Now().Add(-time.Second), 2*time.Second); err != nil {
		t.Fatalf("WaitForHello: %v", err)
	}
	// The hello was already present, so the immediate pre-tick check must
	// satisfy the wait well before the first poll tick.
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("WaitForHello took %v; expected the immediate check to succeed", elapsed)
	}
}

func TestWaitForHelloTimesOut(t *testing.T) {
	isolate(t)
	const (
		driver = "spinclass/bright-cedar"
		worker = "otherrepo/calm-willow"
	)

	deadline := 300 * time.Millisecond
	start := time.Now()
	err := WaitForHello(driver, worker, time.Now(), deadline)
	if err == nil {
		t.Fatal("WaitForHello: expected deadline error, got nil")
	}
	if !strings.Contains(err.Error(), deadline.String()) {
		t.Errorf("error %q does not mention the deadline %s", err, deadline)
	}
	if !strings.Contains(err.Error(), worker) {
		t.Errorf("error %q does not mention the worker key %q", err, worker)
	}
	if elapsed := time.Since(start); elapsed > deadline+500*time.Millisecond {
		t.Errorf("WaitForHello took %v; expected prompt return near the %s deadline", elapsed, deadline)
	}
}

func TestWaitForHelloIgnoresOlder(t *testing.T) {
	isolate(t)
	const (
		driver = "spinclass/bright-cedar"
		worker = "otherrepo/calm-willow"
	)

	if err := SendHello(worker, driver); err != nil {
		t.Fatalf("SendHello: %v", err)
	}

	// since is in the future of the send: the existing hello must not satisfy
	// the gate, so the wait times out.
	since := time.Now().Add(time.Hour)
	if err := WaitForHello(driver, worker, since, 300*time.Millisecond); err == nil {
		t.Fatal("WaitForHello: a pre-since hello must not satisfy the gate")
	}
}

func TestWaitForHelloPairScoped(t *testing.T) {
	isolate(t)
	const worker = "otherrepo/calm-willow"

	if err := SendHello(worker, "spinclass/bright-cedar"); err != nil {
		t.Fatalf("SendHello: %v", err)
	}

	// Same worker, a different driver → different pair key → no match.
	err := WaitForHello("spinclass/other-driver", worker, time.Now().Add(-time.Second), 300*time.Millisecond)
	if err == nil {
		t.Fatal("WaitForHello: a hello for a different (worker,driver) pair must not satisfy the gate")
	}
}
