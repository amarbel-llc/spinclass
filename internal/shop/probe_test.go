package shop

import (
	"testing"
	"time"
)

func TestProbeAliveSuccess(t *testing.T) {
	if !probeAlive([]string{"true"}, time.Second) {
		t.Error("expected probeAlive to return true for `true`")
	}
}

func TestProbeAliveFailure(t *testing.T) {
	if probeAlive([]string{"false"}, time.Second) {
		t.Error("expected probeAlive to return false for `false`")
	}
}

func TestProbeAliveEmptyArgvReturnsFalse(t *testing.T) {
	if probeAlive(nil, time.Second) {
		t.Error("expected probeAlive(nil) to return false")
	}
	if probeAlive([]string{}, time.Second) {
		t.Error("expected probeAlive([]) to return false")
	}
}

func TestProbeAliveMissingBinaryReturnsFalse(t *testing.T) {
	if probeAlive([]string{"/no/such/binary/sc-probe-test"}, time.Second) {
		t.Error("expected probeAlive to return false for missing binary")
	}
}

func TestProbeAliveTimesOut(t *testing.T) {
	// `sleep 5` exceeds the 100ms timeout; probe should report dead.
	if probeAlive([]string{"sleep", "5"}, 100*time.Millisecond) {
		t.Error("expected probeAlive to return false on timeout")
	}
}

func TestProbeAliveSlowSucceedsBeforeTimeout(t *testing.T) {
	// `true` exits immediately even though we allow a long timeout.
	if !probeAlive([]string{"sh", "-c", "exit 0"}, time.Second) {
		t.Error("expected probeAlive to return true for fast success")
	}
}
