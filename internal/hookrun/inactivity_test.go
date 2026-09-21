package hookrun_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	. "code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func TestPreMergeContextInactivityKill(t *testing.T) {
	wt := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{
		PreMerge:          sptr("sleep 10"), // never produces output
		InactivityTimeout: sptr("1s"),
	}}
	var buf bytes.Buffer
	start := time.Now()
	err := PreMergeContext(context.Background(), sf, wt, &buf)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "inactivity-timeout") {
		t.Fatalf("expected inactivity-timeout error, got %v (elapsed %v)", err, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("hook not killed promptly: elapsed %v", elapsed)
	}
}

func TestPreMergeContextStaysAliveWhileActive(t *testing.T) {
	wt := t.TempDir()
	// Emits every 300ms for ~1.8s; under a 1s inactivity budget it never goes
	// silent long enough to be killed, so it completes successfully.
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{
		PreMerge:          sptr("for i in 1 2 3 4 5 6; do echo tick; sleep 0.3; done"),
		InactivityTimeout: sptr("1s"),
	}}
	var buf bytes.Buffer
	if err := PreMergeContext(context.Background(), sf, wt, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "tick") {
		t.Errorf("expected hook output, got %q", buf.String())
	}
}

func TestPreMergeContextNoTimeoutUnchanged(t *testing.T) {
	wt := t.TempDir()
	// No inactivity-timeout → the watchdog is bypassed; a quiet sleep runs to
	// completion (kept short for the test).
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("sleep 0.2; echo done")}}
	var buf bytes.Buffer
	if err := PreMergeContext(context.Background(), sf, wt, &buf); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !strings.Contains(buf.String(), "done") {
		t.Errorf("expected hook output, got %q", buf.String())
	}
}
