package sweatfile

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func sptr(s string) *string { return &s }

func TestParseHooksInactivityTimeout(t *testing.T) {
	doc, err := Parse([]byte("[hooks]\ninactivity-timeout = \"180s\"\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.InactivityTimeout == nil || *sf.Hooks.InactivityTimeout != "180s" {
		t.Fatalf("hooks.inactivity-timeout: got %+v", sf.Hooks)
	}
	// The regenerated tommy decoder must consume the key, else `sc validate`
	// would flag it as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("inactivity-timeout left undecoded: %v", u)
	}
}

func TestInactivityTimeoutValue(t *testing.T) {
	cases := []struct {
		name string
		in   *string
		want time.Duration
	}{
		{"nil", nil, 0},
		{"empty", sptr(""), 0},
		{"valid-seconds", sptr("180s"), 180 * time.Second},
		{"valid-minutes", sptr("3m"), 3 * time.Minute},
		{"invalid", sptr("nope"), 0},
		{"negative", sptr("-5s"), 0},
	}
	for _, c := range cases {
		sf := Sweatfile{Hooks: &Hooks{InactivityTimeout: c.in}}
		if got := sf.InactivityTimeoutValue(); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	// No Hooks at all → 0.
	if got := (Sweatfile{}).InactivityTimeoutValue(); got != 0 {
		t.Errorf("nil Hooks: got %v want 0", got)
	}
}

func TestMergeHooksInactivityTimeoutOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{InactivityTimeout: sptr("60s")}}
	repo := Sweatfile{Hooks: &Hooks{InactivityTimeout: sptr("180s")}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.InactivityTimeout == nil ||
		*merged.Hooks.InactivityTimeout != "180s" {
		t.Errorf("expected override 180s, got %+v", merged.Hooks)
	}
}

func TestMergeHooksInactivityTimeoutInherit(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{InactivityTimeout: sptr("60s")}}
	merged := base.MergeWith(Sweatfile{})
	if merged.Hooks == nil || merged.Hooks.InactivityTimeout == nil ||
		*merged.Hooks.InactivityTimeout != "60s" {
		t.Errorf("expected inherited 60s, got %+v", merged.Hooks)
	}
}

func TestRunPreMergeHookContextInactivityKill(t *testing.T) {
	wt := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{
		PreMerge:          sptr("sleep 10"), // never produces output
		InactivityTimeout: sptr("1s"),
	}}
	var buf bytes.Buffer
	start := time.Now()
	err := sf.RunPreMergeHookContext(context.Background(), wt, &buf)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "inactivity-timeout") {
		t.Fatalf("expected inactivity-timeout error, got %v (elapsed %v)", err, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("hook not killed promptly: elapsed %v", elapsed)
	}
}

func TestRunPreMergeHookContextStaysAliveWhileActive(t *testing.T) {
	wt := t.TempDir()
	// Emits every 300ms for ~1.8s; under a 1s inactivity budget it never goes
	// silent long enough to be killed, so it completes successfully.
	sf := Sweatfile{Hooks: &Hooks{
		PreMerge:          sptr("for i in 1 2 3 4 5 6; do echo tick; sleep 0.3; done"),
		InactivityTimeout: sptr("1s"),
	}}
	var buf bytes.Buffer
	if err := sf.RunPreMergeHookContext(context.Background(), wt, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "tick") {
		t.Errorf("expected hook output, got %q", buf.String())
	}
}

func TestRunPreMergeHookContextNoTimeoutUnchanged(t *testing.T) {
	wt := t.TempDir()
	// No inactivity-timeout → the watchdog is bypassed; a quiet sleep runs to
	// completion (kept short for the test).
	sf := Sweatfile{Hooks: &Hooks{PreMerge: sptr("sleep 0.2; echo done")}}
	var buf bytes.Buffer
	if err := sf.RunPreMergeHookContext(context.Background(), wt, &buf); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !strings.Contains(buf.String(), "done") {
		t.Errorf("expected hook output, got %q", buf.String())
	}
}
