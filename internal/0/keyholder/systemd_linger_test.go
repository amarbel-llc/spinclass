package keyholder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pair is started as a user unit. With no user manager to talk to (no lingering,
// no login session) systemd prints that it cannot reach the bus; the refusal then
// names lingering instead of leaving the operator with a bare exit status.
func TestAStartThatCannotReachTheUserManagerNamesLingering(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	run := fakeTool(t, dir, "systemd-run", log)
	ctl := fakeTool(t, dir, "systemctl", log)
	if err := os.WriteFile(run+".out", []byte("Failed to connect to user scope bus via local transport: No medium found\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(run+".exit", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer OverrideSystemdPins(run, ctl)()

	err := SystemdRunner{}.Start(context.Background(), "u", []string{"/bin/true"}, nil)
	if err == nil {
		t.Fatal("Start succeeded against a systemd-run that failed")
	}
	for _, want := range []string{"No medium found", "loginctl enable-linger"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// Any other failure keeps its own words: lingering is named only when the user
// manager is what is missing.
func TestAStartThatFailedForAnotherReasonDoesNotBlameLingering(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	run := fakeTool(t, dir, "systemd-run", log)
	ctl := fakeTool(t, dir, "systemctl", log)
	if err := os.WriteFile(run+".out", []byte("Unit spinclass-keyholder-fibby-x.service was already loaded or has a fragment file.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(run+".exit", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer OverrideSystemdPins(run, ctl)()

	err := SystemdRunner{}.Start(context.Background(), "u", []string{"/bin/true"}, nil)
	if err == nil || strings.Contains(err.Error(), "linger") {
		t.Errorf("an unrelated failure was blamed on lingering: %v", err)
	}
}

// A systemctl that cannot reach the bus is an error with the same hint, and is
// never read as "the pair is not running".
func TestAnIsActiveThatCannotReachTheUserManagerNamesLingering(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	run := fakeTool(t, dir, "systemd-run", log)
	ctl := fakeTool(t, dir, "systemctl", log)
	// fakeTool prints name.out to stdout; this one must fail on STDERR, so write a
	// dedicated script.
	script := "#!/bin/sh\necho 'Failed to connect to bus: No medium found' >&2\nexit 1\n"
	if err := os.WriteFile(ctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	defer OverrideSystemdPins(run, ctl)()

	active, err := SystemdRunner{}.Active(context.Background(), "u")
	if err == nil || active {
		t.Fatalf("Active = %v, %v; want an error, never a quiet 'not running'", active, err)
	}
	if !strings.Contains(err.Error(), "loginctl enable-linger") {
		t.Errorf("the error does not name lingering: %v", err)
	}
}
