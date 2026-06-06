package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain strips CLOWN_BIN for the whole package: the dev/CI environment
// runs under clown, and without this every pre-existing Start test would emit
// REAL job-wakeup events at the developer's session. Wake tests opt back in
// with a stub via t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("CLOWN_BIN")
	os.Exit(m.Run())
}

// stubClown writes an executable shell script that appends each invocation's
// argv (one element per line, terminated by a "--" line) to argsFile,
// printing a fixed job id for `job start` calls. ok=false makes every call
// exit 1.
func stubClown(t *testing.T, argsFile string, ok bool) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "clown")
	exit := "1"
	if ok {
		exit = "0"
	}
	body := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; echo --; } >> " + argsFile + "\n" +
		"if [ \"$2\" = start ]; then echo job-deadbeef; fi\n" +
		"exit " + exit + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub clown: %v", err)
	}
	return script
}

// recordedInvocations splits the stub's args file back into one argv slice
// per invocation.
func recordedInvocations(t *testing.T, argsFile string) [][]string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	var out [][]string
	var cur []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "--" {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return out
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

// runWaked starts fn as a job with the stub installed and blocks until the
// terminal record is persisted and emits have run.
func runWaked(t *testing.T, wt, kind string, fn Func) {
	t.Helper()
	if _, err := Start(wt, kind, false, "test-job", fn); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-WaitDone(wt):
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish in time")
	}
}

func TestStartEmitsClownLifecycleOnSuccess(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	runWaked(t, wt, KindMerge, func(ctx context.Context, w io.Writer) (string, bool) {
		return "ok 1 - hook\n1..1", false
	})

	inv := recordedInvocations(t, argsFile)
	if len(inv) != 2 {
		t.Fatalf("clown invocations: got %d (%q), want 2 (start + done)", len(inv), inv)
	}
	assertArgv(t, inv[0], []string{"job", "start", "--label", "merge", "--source", "spinclass"})
	assertArgv(t, inv[1], []string{
		"job", "done", "job-deadbeef",
		"--state", "succeeded",
		"--message", "merge succeeded",
		"--result-ref", "spinclass session-job-status",
	})

	j, err := Read(wt)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if j.ClownJobID != "job-deadbeef" {
		t.Fatalf("ClownJobID: got %q, want %q", j.ClownJobID, "job-deadbeef")
	}
}

func TestStartEmitsFailedStateWithFailureLine(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	runWaked(t, wt, KindCheck, func(ctx context.Context, w io.Writer) (string, bool) {
		return "ok 1 - rebase\nnot ok 2 - pre-merge hook\n1..2", true
	})

	inv := recordedInvocations(t, argsFile)
	if len(inv) != 2 {
		t.Fatalf("clown invocations: got %d, want 2", len(inv))
	}
	assertArgv(t, inv[1], []string{
		"job", "done", "job-deadbeef",
		"--state", "failed",
		"--message", "check failed: not ok 2 - pre-merge hook",
		"--result-ref", "spinclass session-job-status",
	})
}

func TestStartEmitsCancelledState(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	started := make(chan struct{})
	if _, err := Start(wt, KindMerge, false, "cancel-job", func(ctx context.Context, w io.Writer) (string, bool) {
		close(started)
		<-ctx.Done()
		return "", true
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	Cancel(wt)
	select {
	case <-WaitDone(wt):
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after cancel")
	}

	inv := recordedInvocations(t, argsFile)
	if len(inv) != 2 {
		t.Fatalf("clown invocations: got %d, want 2", len(inv))
	}
	assertArgv(t, inv[1], []string{
		"job", "done", "job-deadbeef",
		"--state", "cancelled",
		"--message", "merge cancelled",
		"--result-ref", "spinclass session-job-status",
	})
}

func TestStartNoEmitWhenClownAbsent(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	// CLOWN_BIN deliberately unset (TestMain stripped it).

	runWaked(t, wt, KindMerge, func(ctx context.Context, w io.Writer) (string, bool) {
		return "ok 1 - hook", false
	})

	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("clown invoked despite CLOWN_BIN being unset")
	}
	j, err := Read(wt)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if j.ClownJobID != "" {
		t.Fatalf("ClownJobID without clown: got %q, want empty", j.ClownJobID)
	}
	if j.Status != StatusSucceeded {
		t.Fatalf("status: got %q, want succeeded", j.Status)
	}
}

func TestStartReturnsImmediatelyDespiteSlowClown(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	// A clown that hangs for 3s: the async tool's contract is to return a
	// job id immediately, so the start emit must not be on Start's path.
	dir := t.TempDir()
	script := filepath.Join(dir, "clown")
	body := "#!/bin/sh\nsleep 3\n{ printf '%s\\n' \"$@\"; echo --; } >> " + argsFile + "\n" +
		"if [ \"$2\" = start ]; then echo job-deadbeef; fi\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub clown: %v", err)
	}
	t.Setenv("CLOWN_BIN", script)

	began := time.Now()
	if _, err := Start(wt, KindMerge, false, "fast-job", func(ctx context.Context, w io.Writer) (string, bool) {
		return "ok 1 - hook", false
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if elapsed := time.Since(began); elapsed > 1*time.Second {
		t.Fatalf("Start blocked %v on a slow clown; want immediate return", elapsed)
	}
	select {
	case <-WaitDone(wt):
	case <-time.After(10 * time.Second):
		t.Fatal("job did not finish in time")
	}
}

func TestStartReturnsSnapshotNotSharedPointer(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, true))

	ret, err := Start(wt, KindMerge, false, "snap-job", func(ctx context.Context, w io.Writer) (string, bool) {
		return "ok 1 - hook", false
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-WaitDone(wt):
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish in time")
	}

	// The returned record is a point-in-time snapshot: the goroutine's later
	// mutations (status, clown job id, result) must not be visible through
	// it — reading them concurrently would be a data race otherwise.
	if ret.Status != StatusRunning {
		t.Fatalf("returned job mutated by goroutine: status %q, want snapshot %q", ret.Status, StatusRunning)
	}
	if ret.ClownJobID != "" {
		t.Fatalf("returned job mutated by goroutine: clown id %q, want empty snapshot", ret.ClownJobID)
	}
	j, err := Read(wt)
	if err != nil || j.Status != StatusSucceeded {
		t.Fatalf("persisted record: status=%v err=%v, want succeeded", j, err)
	}
}

func TestStartEmitFailureDoesNotAffectJob(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClown(t, argsFile, false))

	runWaked(t, wt, KindMerge, func(ctx context.Context, w io.Writer) (string, bool) {
		return "ok 1 - hook", false
	})

	j, err := Read(wt)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if j.Status != StatusSucceeded {
		t.Fatalf("emit failure changed job status: got %q, want succeeded", j.Status)
	}
	if log := TailLog(wt, 10); !strings.Contains(log, "[clown]") {
		t.Fatalf("emit failure not logged to job.log: %q", log)
	}
}
