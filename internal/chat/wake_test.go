package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWakeModeDefaultsToLegacy(t *testing.T) {
	t.Setenv("SPINCLASS_CHAT_WAKE", "")
	os.Unsetenv("SPINCLASS_CHAT_WAKE")
	if got := ResolveWakeMode(); got != WakeModeLegacy {
		t.Fatalf("unset env: got %q, want %q", got, WakeModeLegacy)
	}
}

func TestResolveWakeModeClown(t *testing.T) {
	t.Setenv("SPINCLASS_CHAT_WAKE", "clown")
	if got := ResolveWakeMode(); got != WakeModeClown {
		t.Fatalf("got %q, want %q", got, WakeModeClown)
	}
}

func TestResolveWakeModeUnrecognizedFallsBackToLegacy(t *testing.T) {
	t.Setenv("SPINCLASS_CHAT_WAKE", "bogus")
	if got := ResolveWakeMode(); got != WakeModeLegacy {
		t.Fatalf("unrecognized value: got %q, want %q", got, WakeModeLegacy)
	}
}

// stubClown writes an executable shell script that records its argv (one
// element per line) into argsFile and exits successfully iff ok. It returns
// the script path for CLOWN_BIN injection. Mirrors stubClownBin in
// cmd/spinclass's tests (Go test helpers cannot be shared across packages).
func stubClown(t *testing.T, argsFile string, ok bool) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "clown")
	exit := "1"
	if ok {
		exit = "0"
	}
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nexit " + exit + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub clown: %v", err)
	}
	return script
}

// recordedArgs reads back the argv lines the stub recorded.
func recordedArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func TestEmitWakeLegacyModeIsNoop(t *testing.T) {
	t.Setenv("SPINCLASS_CHAT_WAKE", "legacy")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	err := EmitWake(context.Background(), Message{From: "spinclass/a", To: "clown/b", Body: "hi"})
	if err != nil {
		t.Fatalf("legacy mode emit: %v", err)
	}
	if _, statErr := os.Stat(argsFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy mode invoked clown (args file exists)")
	}
}

func TestEmitWakeDirectMessageArgv(t *testing.T) {
	t.Setenv("SPINCLASS_CHAT_WAKE", "clown")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	m := Message{From: "spinclass/crisp-catalpa", To: "clown/sleek-sumac", Body: "rebase landed"}
	if err := EmitWake(context.Background(), m); err != nil {
		t.Fatalf("emit: %v", err)
	}

	want := []string{
		"job", "message",
		"--target", "clown/sleek-sumac",
		"--from", "spinclass/crisp-catalpa",
		"--source", "spinclass",
		"--message", "rebase landed",
		"--result-ref", "chat-read from=spinclass/crisp-catalpa peek=true",
	}
	got := recordedArgs(t, argsFile)
	if len(got) != len(want) {
		t.Fatalf("argv length: got %d (%q), want %d (%q)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEmitWakeBroadcastTargetsStar(t *testing.T) {
	t.Setenv("SPINCLASS_CHAT_WAKE", "clown")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	m := Message{From: "spinclass/crisp-catalpa", To: Broadcast, Body: "schema migration landed"}
	if err := EmitWake(context.Background(), m); err != nil {
		t.Fatalf("emit: %v", err)
	}

	got := recordedArgs(t, argsFile)
	for i, a := range got {
		if a == "--target" {
			if i+1 >= len(got) {
				t.Fatalf("--target at end of argv with no value: %q", got)
			}
			if got[i+1] != Broadcast {
				t.Fatalf("--target: got %q, want %q", got[i+1], Broadcast)
			}
			return
		}
	}
	t.Fatalf("no --target in argv: %q", got)
}

func TestEmitWakeFailureSurfacesError(t *testing.T) {
	t.Setenv("SPINCLASS_CHAT_WAKE", "clown")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, false))

	err := EmitWake(context.Background(), Message{From: "a/b", To: "c/d", Body: "x"})
	if err == nil {
		t.Fatal("emit with failing clown: want error, got nil")
	}
}

func TestEmitWakeMissingBinaryErrors(t *testing.T) {
	t.Setenv("SPINCLASS_CHAT_WAKE", "clown")
	t.Setenv("CLOWN_BIN", filepath.Join(t.TempDir(), "no-such-clown"))

	err := EmitWake(context.Background(), Message{From: "a/b", To: "c/d", Body: "x"})
	if err == nil {
		t.Fatal("emit with missing clown binary: want error, got nil")
	}
}
