package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubUnconfinedGate points the gate at a fake terminal answer and a temp state
// dir, and returns the log path and the captured stderr.
func stubUnconfinedGate(t *testing.T, isTerminal bool, typed string) (string, *bytes.Buffer) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	var stderr bytes.Buffer
	oldTTY, oldIn, oldErr := unconfinedSpawnIsTerminal, unconfinedSpawnConfirmIn, unconfinedSpawnStderr
	t.Cleanup(func() {
		unconfinedSpawnIsTerminal, unconfinedSpawnConfirmIn, unconfinedSpawnStderr = oldTTY, oldIn, oldErr
	})
	unconfinedSpawnIsTerminal = func() bool { return isTerminal }
	unconfinedSpawnConfirmIn = func() io.Reader { return strings.NewReader(typed) }
	unconfinedSpawnStderr = func() io.Writer { return &stderr }
	return filepath.Join(state, "spinclass", unconfinedSpawnLogName), &stderr
}

func readUnconfinedSpawnLog(t *testing.T, path string) []unconfinedSpawnRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	var recs []unconfinedSpawnRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec unconfinedSpawnRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

// `sc spawn --unconfined` with no terminal (stdin /dev/null in a recipe) is
// refused before anything is spawned, and the attempt is logged.
func TestSpawnCLIUnconfinedWithoutATerminalIsRefused(t *testing.T) {
	logPath, _ := stubUnconfinedGate(t, false, "unconfined\n")
	args, _ := json.Marshal(map[string]any{"repo": "nowhere-such-repo", "brief": "b", "unconfined": true})
	err := runSpawnCLI(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "--unconfined needs a human at a terminal") {
		t.Fatalf("got %v, want the terminal refusal", err)
	}
	recs := readUnconfinedSpawnLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != "refused: no terminal" || recs[0].Repo != "nowhere-such-repo" {
		t.Errorf("log = %+v", recs)
	}
	if info, err := os.Stat(logPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("log mode: %v %v, want 0600", info, err)
	}
}

// At a terminal, anything but the exact confirmation word declines: "y", an
// empty line or EOF, a different case. Each is logged.
func TestSpawnCLIUnconfinedWrongAnswerIsRefused(t *testing.T) {
	for _, typed := range []string{"y\n", "\n", "", "Unconfined\n", "yes unconfined\n"} {
		logPath, stderr := stubUnconfinedGate(t, true, typed)
		args, _ := json.Marshal(map[string]any{"repo": "nowhere-such-repo", "brief": "b", "unconfined": true})
		err := runSpawnCLI(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "not confirmed") {
			t.Errorf("typed %q: got %v, want declined", typed, err)
		}
		if !strings.Contains(stderr.String(), "type unconfined to confirm") {
			t.Errorf("typed %q: prompt missing from stderr %q", typed, stderr.String())
		}
		if recs := readUnconfinedSpawnLog(t, logPath); len(recs) != 1 || recs[0].Outcome != "declined" {
			t.Errorf("typed %q: log = %+v", typed, recs)
		}
	}
}

// The exact word confirms (surrounding whitespace aside) and is logged.
func TestUnconfinedSpawnConfirmedByTheWord(t *testing.T) {
	logPath, _ := stubUnconfinedGate(t, true, "  unconfined \n")
	if err := confirmUnconfinedSpawn("canary"); err != nil {
		t.Fatalf("got %v, want confirmed", err)
	}
	if recs := readUnconfinedSpawnLog(t, logPath); len(recs) != 1 || recs[0].Outcome != "confirmed" {
		t.Errorf("log = %+v", recs)
	}
}

// A log that cannot be written refuses, even at a terminal with the word typed.
func TestUnconfinedSpawnRefusedWhenTheLogIsUnwritable(t *testing.T) {
	stubUnconfinedGate(t, true, "unconfined\n")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocker) // a file, so the spinclass dir cannot be created
	if err := confirmUnconfinedSpawn("canary"); err == nil || !strings.Contains(err.Error(), "log cannot be written") {
		t.Fatalf("got %v, want the log refusal", err)
	}
}

// A spawn that does not opt out never meets the gate.
func TestSpawnCLIWithoutUnconfinedSkipsTheGate(t *testing.T) {
	logPath, _ := stubUnconfinedGate(t, false, "")
	args, _ := json.Marshal(map[string]any{"repo": "nowhere-such-repo"})
	err := runSpawnCLI(context.Background(), args)
	if err == nil || strings.Contains(err.Error(), "terminal") {
		t.Fatalf("got %v, want the ordinary brief-required error", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("the gate logged a spawn that did not opt out: %v", err)
	}
}
