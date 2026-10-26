package clown

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The restart wakes are sent from processes that clown starts without
// CLOWN_BIN: the frontend that runs `spinclass restart-preflight`, and the
// relaunched agent whose SessionStart hook sends the "back" wake (observed on
// circus/solid-elm, 2026-10-26: both had CLOWN_SESSION_ID and no CLOWN_BIN, so
// NotifyPrincipal silently sent nothing). CLOWN_SESSION_ID is the signal those
// processes do carry.

// Without CLOWN_BIN but with CLOWN_SESSION_ID the wake is sent, shaped exactly
// like NotifyPrincipal's.
func TestNotifyPrincipalUnderClownSendsWithOnlyASessionID(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "exit-9f3c1a2b", true))
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	t.Setenv("CLOWN_SESSION_ID", "restarted-session-principal")

	if err := NotifyPrincipalUnderClown(context.Background(), "holder-1", "", "kid is restarting now"); err != nil {
		t.Fatalf("NotifyPrincipalUnderClown: %v", err)
	}
	assertArgv(t, recordedArgs(t, argsFile), []string{
		"start", "--target", "holder-1", "--label", "exit", "--source", "spinclass",
		"done", "exit-9f3c1a2b", "--target", "holder-1", "--state", "succeeded",
		"--message", "kid is restarting now",
	})
}

// With neither signal it stays a no-op that never runs ringmaster, so a plain
// terminal or a CI run does not emit wakes.
func TestNotifyPrincipalUnderClownIsNoopWithoutAnySignal(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	t.Setenv("CLOWN_SESSION_ID", "")
	_ = os.Unsetenv("CLOWN_SESSION_ID")
	t.Setenv("RINGMASTER_BIN", filepath.Join(t.TempDir(), "no-such-ringmaster"))

	if err := NotifyPrincipalUnderClown(context.Background(), "holder-1", "", "msg"); err != nil {
		t.Fatalf("with no clown signal: want nil, got %v", err)
	}
}

// CLOWN_BIN alone still enables it, as NotifyPrincipal does.
func TestNotifyPrincipalUnderClownStillHonoursClownBin(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "exit-abc", true))
	t.Setenv("CLOWN_BIN", "/some/clown")
	t.Setenv("CLOWN_SESSION_ID", "")
	_ = os.Unsetenv("CLOWN_SESSION_ID")

	if err := NotifyPrincipalUnderClown(context.Background(), "holder-1", "", "hello"); err != nil {
		t.Fatalf("NotifyPrincipalUnderClown: %v", err)
	}
	if got := recordedArgs(t, argsFile); len(got) == 0 {
		t.Fatal("nothing was sent with CLOWN_BIN set")
	}
}

// An unresolvable recipient is an error under either signal, never swallowed.
func TestNotifyPrincipalUnderClownRequiresTarget(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "restarted-session-principal")
	if err := NotifyPrincipalUnderClown(context.Background(), "", "", "msg"); err == nil {
		t.Fatal("empty target: want error, got nil")
	}
}
