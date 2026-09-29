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

	err := FinishJob(context.Background(), "merge-9f3c1a2b", "succeeded", "merge landed", "ringmaster read merge-9f3c1a2b")
	if err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	assertArgv(t, recordedArgs(t, argsFile), []string{
		"done", "merge-9f3c1a2b",
		"--state", "succeeded",
		"--message", "merge landed",
		"--result-ref", "ringmaster read merge-9f3c1a2b",
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

// TestNotifyPrincipalArgv pins the FDR 0032 D6 wake shape: a start+done pair
// explicitly --target'ed at the recipient (never the caller's own channel),
// since ringmaster carries no standalone "message" verb (see NotifyPrincipal's
// doc comment).
func TestNotifyPrincipalArgv(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "exit-9f3c1a2b", true))
	t.Setenv("CLOWN_BIN", "/some/clown")

	err := NotifyPrincipal(context.Background(), "holder-1", "", "session worker/kid exited (normal); holders: 1 remaining")
	if err != nil {
		t.Fatalf("NotifyPrincipal: %v", err)
	}
	assertArgv(t, recordedArgs(t, argsFile), []string{
		"start", "--target", "holder-1", "--label", "exit", "--source", "spinclass",
		"done", "exit-9f3c1a2b", "--target", "holder-1", "--state", "succeeded",
		"--message", "session worker/kid exited (normal); holders: 1 remaining",
	})
}

// TestNotifyPrincipalFoldsFromIntoMessage: ringmaster's `done` carries no wire
// `from` field outside the retired message verb, so a non-empty from is
// folded into the message text rather than silently dropped.
func TestNotifyPrincipalFoldsFromIntoMessage(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "exit-abc", true))
	t.Setenv("CLOWN_BIN", "/some/clown")

	if err := NotifyPrincipal(context.Background(), "holder-1", "driver/main-oak", "hello"); err != nil {
		t.Fatalf("NotifyPrincipal: %v", err)
	}
	got := recordedArgs(t, argsFile)
	if got[len(got)-1] != "from driver/main-oak: hello" {
		t.Fatalf("message with from: got %q, want folded prefix", got[len(got)-1])
	}
}

// TestNotifyPrincipalRequiresTarget: an empty targetPrincipal is a usage
// error, not a silent no-op — an unresolvable recipient must never be
// swallowed the way a disabled clown legitimately is.
func TestNotifyPrincipalRequiresTarget(t *testing.T) {
	t.Setenv("CLOWN_BIN", "/some/clown")
	if err := NotifyPrincipal(context.Background(), "", "", "msg"); err == nil {
		t.Fatal("NotifyPrincipal with empty target: want error, got nil")
	}
}

// TestNotifyPrincipalDisabledIsNoop: without CLOWN_BIN, NotifyPrincipal must
// not even attempt to resolve or run ringmaster — RINGMASTER_BIN points at a
// nonexistent path to prove it is never invoked.
func TestNotifyPrincipalDisabledIsNoop(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	t.Setenv("RINGMASTER_BIN", filepath.Join(t.TempDir(), "no-such-ringmaster"))

	if err := NotifyPrincipal(context.Background(), "holder-1", "", "msg"); err != nil {
		t.Fatalf("NotifyPrincipal with clown disabled: want nil, got %v", err)
	}
}

// TestEmitExitWakesNotifiesEachHolder: one start+done pair per holder, in
// order, each targeted at that holder.
func TestEmitExitWakesNotifiesEachHolder(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("RINGMASTER_BIN", stubRingmaster(t, argsFile, "exit-1", true))
	t.Setenv("CLOWN_BIN", "/some/clown")

	if err := EmitExitWakes([]string{"holder-1", "holder-2"}, "worker/kid", "shutdown"); err != nil {
		t.Fatalf("EmitExitWakes: %v", err)
	}
	got := recordedArgs(t, argsFile)
	wantMsg := "session worker/kid exited (shutdown); holders: 2 remaining"
	assertArgv(t, got, []string{
		"start", "--target", "holder-1", "--label", "exit", "--source", "spinclass",
		"done", "exit-1", "--target", "holder-1", "--state", "succeeded", "--message", wantMsg,
		"start", "--target", "holder-2", "--label", "exit", "--source", "spinclass",
		"done", "exit-1", "--target", "holder-2", "--state", "succeeded", "--message", wantMsg,
	})
}

// TestEmitExitWakesNoopWhenNoHoldersOrDisabled: neither an empty holder list
// nor a disabled clown must shell out at all.
func TestEmitExitWakesNoopWhenNoHoldersOrDisabled(t *testing.T) {
	t.Setenv("RINGMASTER_BIN", filepath.Join(t.TempDir(), "no-such-ringmaster"))

	t.Setenv("CLOWN_BIN", "/some/clown")
	if err := EmitExitWakes(nil, "worker/kid", "normal"); err != nil {
		t.Fatalf("EmitExitWakes with no holders: want nil, got %v", err)
	}

	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	if err := EmitExitWakes([]string{"holder-1"}, "worker/kid", "normal"); err != nil {
		t.Fatalf("EmitExitWakes with clown disabled: want nil, got %v", err)
	}
}

// TestEmitExitWakesJoinsPerHolderErrors: a failing ringmaster must not stop
// after the first holder — every holder is attempted, and every failure is
// named in the joined error.
func TestEmitExitWakesJoinsPerHolderErrors(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ringmaster")
	body := "#!/bin/sh\necho 'boom' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub ringmaster: %v", err)
	}
	t.Setenv("RINGMASTER_BIN", script)
	t.Setenv("CLOWN_BIN", "/some/clown")

	err := EmitExitWakes([]string{"holder-1", "holder-2"}, "worker/kid", "crash")
	if err == nil {
		t.Fatal("EmitExitWakes with a failing ringmaster: want a joined error, got nil")
	}
	for _, want := range []string{"holder-1", "holder-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("joined error %q is missing failure for %q", err, want)
		}
	}
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
