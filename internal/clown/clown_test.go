package clown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubRingmaster writes an executable shell script that records its argv (one
// element per line) into argsFile, prints stdout, and exits successfully iff
// ok. It returns the script path for $RINGMASTER_BIN injection.
func stubRingmaster(t *testing.T, argsFile, stdout string, ok bool) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ringmaster")
	exit := "1"
	if ok {
		exit = "0"
	}
	// Append (>>) so a future multi-invocation test cannot silently lose
	// earlier calls — same contract as internal/job's stub.
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argsFile + "\n"
	if stdout != "" {
		body += "echo " + stdout + "\n"
	}
	body += "exit " + exit + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub ringmaster: %v", err)
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

func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv length: got %d (%q), want %d (%q)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRingmasterBinHonorsOverride(t *testing.T) {
	t.Setenv("RINGMASTER_BIN", "/nix/store/abc/bin/ringmaster")
	if got := ringmasterBin(); got != "/nix/store/abc/bin/ringmaster" {
		t.Fatalf("got %q, want RINGMASTER_BIN value", got)
	}
}

func TestRingmasterBinDefaultsToPathLookup(t *testing.T) {
	t.Setenv("RINGMASTER_BIN", "")
	_ = os.Unsetenv("RINGMASTER_BIN")
	if got := ringmasterBin(); got != "ringmaster" {
		t.Fatalf("unset RINGMASTER_BIN: got %q, want %q", got, "ringmaster")
	}
}

func TestEnabledRequiresClownBin(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	if Enabled() {
		t.Fatal("Enabled with CLOWN_BIN unset: got true, want false")
	}
	t.Setenv("CLOWN_BIN", "/some/clown")
	if !Enabled() {
		t.Fatal("Enabled with CLOWN_BIN set: got false, want true")
	}
}

func TestStartJobArgvAndID(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "merge-9f3c1a2b", true))

	id, err := StartJob(context.Background(), "merge", "spinclass")
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if id != "merge-9f3c1a2b" {
		t.Fatalf("job id: got %q, want %q", id, "merge-9f3c1a2b")
	}
	assertArgv(t, recordedArgs(t, argsFile), []string{
		"start",
		"--label", "merge",
		"--source", "spinclass",
	})
}

func TestStartJobEmptyStdoutErrors(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "", true))

	if _, err := StartJob(context.Background(), "merge", "spinclass"); err == nil {
		t.Fatal("StartJob with empty stdout: want error, got nil")
	}
}

func TestFinishJobArgv(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "", true))

	err := FinishJob(context.Background(), "merge-9f3c1a2b", "succeeded", "merge landed", "spinclass session-job-status")
	if err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	assertArgv(t, recordedArgs(t, argsFile), []string{
		"done", "merge-9f3c1a2b",
		"--state", "succeeded",
		"--message", "merge landed",
		"--result-ref", "spinclass session-job-status",
	})
}

func TestFinishJobOmitsEmptyOptionals(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "", true))

	if err := FinishJob(context.Background(), "check-1", "failed", "", ""); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	assertArgv(t, recordedArgs(t, argsFile), []string{
		"done", "check-1",
		"--state", "failed",
	})
}

func TestFailureSurfacesStderr(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	dir := t.TempDir()
	script := filepath.Join(dir, "ringmaster")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho 'boom detail' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub ringmaster: %v", err)
	}
	t.Setenv("RINGMASTER_BIN", script)

	err := FinishJob(context.Background(), "j-1", "succeeded", "", "")
	if err == nil {
		t.Fatal("failing ringmaster: want error, got nil")
	}
	if !strings.Contains(err.Error(), "boom detail") {
		t.Fatalf("error missing stderr detail: %v", err)
	}
}
