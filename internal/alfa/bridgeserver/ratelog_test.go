package bridgeserver

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOneKindOfListenerLogLineIsWrittenOncePerWindowAndTheRestCounted(t *testing.T) {
	var out []string
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	logf := rateLimitedLogf(func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }, 10*time.Second, func() time.Time { return now })
	for i := 0; i < 100; i++ {
		logf("bridge refused a peer outside unit %s: %v", "u.service", i)
	}
	logf("bridge hello refused: %v", "bad hello")
	if len(out) != 2 {
		t.Fatalf("%d lines inside a window, want 2 (one per kind): %v", len(out), out)
	}
	now = now.Add(11 * time.Second)
	logf("bridge refused a peer outside unit %s: %v", "u.service", 100)
	if len(out) != 4 || !strings.Contains(out[3], "99 similar log lines were suppressed") {
		t.Fatalf("after the window: %v", out)
	}
}

func TestTheLogKindTableIsBoundedAndNeverHidesALine(t *testing.T) {
	var out []string
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	logf := rateLimitedLogf(func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }, time.Hour, func() time.Time { return now })
	for i := 0; i < maxLogKinds+10; i++ {
		logf(fmt.Sprintf("kind-%d", i))
	}
	if len(out) != maxLogKinds+10 {
		t.Errorf("%d lines written, want %d: a full table must write, not drop", len(out), maxLogKinds+10)
	}
}
