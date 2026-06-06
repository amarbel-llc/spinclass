package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestEmitWakeWithoutClownBinIsNoop(t *testing.T) {
	// Emit capability is gated on CLOWN_BIN presence (the producer-may-emit
	// contract) — without clown there is nothing to emit to, and chat-read
	// polling covers delivery.
	t.Setenv("CLOWN_BIN", "")
	os.Unsetenv("CLOWN_BIN")

	err := EmitWake(context.Background(), Message{From: "spinclass/a", To: "clown/b", Body: "hi"})
	if err != nil {
		t.Fatalf("emit without clown: %v", err)
	}
}

func TestEmitWakeEmitsUnderClown(t *testing.T) {
	// The emit is presence-gated: running under clown (CLOWN_BIN set) is
	// the only condition.
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	err := EmitWake(context.Background(), Message{From: "spinclass/a", To: "clown/b", Body: "hi"})
	if err != nil {
		t.Fatalf("emit under clown: %v", err)
	}
	if _, statErr := os.Stat(argsFile); statErr != nil {
		t.Fatalf("sender under clown did not emit: %v", statErr)
	}
}

func TestEmitWakeDirectMessageArgv(t *testing.T) {
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

func TestEmitWakeCarriesSubjectNotBody(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	m := Message{From: "a/b", To: "c/d", Subject: "short summary", Body: "very long body\nacross lines"}
	if err := EmitWake(context.Background(), m); err != nil {
		t.Fatalf("emit: %v", err)
	}

	got := recordedArgs(t, argsFile)
	for i, a := range got {
		if a == "--message" {
			if got[i+1] != "short summary" {
				t.Fatalf("--message: got %q, want the subject", got[i+1])
			}
			return
		}
	}
	t.Fatalf("no --message in argv: %q", got)
}

func TestEmitWakeBroadcastTargetsStar(t *testing.T) {
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
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, false))

	err := EmitWake(context.Background(), Message{From: "a/b", To: "c/d", Body: "x"})
	if err == nil {
		t.Fatal("emit with failing clown: want error, got nil")
	}
}

func TestEmitWakeMissingBinaryErrors(t *testing.T) {
	t.Setenv("CLOWN_BIN", filepath.Join(t.TempDir(), "no-such-clown"))

	err := EmitWake(context.Background(), Message{From: "a/b", To: "c/d", Body: "x"})
	if err == nil {
		t.Fatal("emit with missing clown binary: want error, got nil")
	}
}
