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
// with a stub via installStub.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CLOWN_BIN")
	os.Exit(m.Run())
}

// installStub writes an executable shell script that appends each invocation's
// argv (one element per line, terminated by a "--" line) to argsFile, printing
// a fixed job id for `ringmaster start` calls. It points $RINGMASTER_BIN at the
// stub (clown RFC-0015 resolution) and sets $CLOWN_BIN so clown.Enabled()
// reports true (the emit gate). ok=false makes every call exit 1.
func installStub(t *testing.T, argsFile string, ok bool) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ringmaster")
	exit := "1"
	if ok {
		exit = "0"
	}
	body := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; echo --; } >> " + argsFile + "\n" +
		"if [ \"$1\" = start ]; then echo job-deadbeef; fi\n" +
		"exit " + exit + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub ringmaster: %v", err)
	}
	t.Setenv("CLOWN_BIN", filepath.Join(dir, "clown"))
	t.Setenv("RINGMASTER_BIN", script)
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
	installStub(t, argsFile, true)

	runWaked(t, wt, KindMerge, func(ctx context.Context, w io.Writer) (string, bool) {
		return "✓ hook", false
	})

	inv := recordedInvocations(t, argsFile)
	if len(inv) != 2 {
		t.Fatalf("ringmaster invocations: got %d (%q), want 2 (start + done)", len(inv), inv)
	}
	assertArgv(t, inv[0], []string{"start", "--label", "merge", "--source", "spinclass"})
	assertArgv(t, inv[1], []string{
		"done", "job-deadbeef",
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
	installStub(t, argsFile, true)

	runWaked(t, wt, KindCheck, func(ctx context.Context, w io.Writer) (string, bool) {
		return "✓ rebase\n✗ pre-merge hook", true
	})

	inv := recordedInvocations(t, argsFile)
	if len(inv) != 2 {
		t.Fatalf("ringmaster invocations: got %d, want 2", len(inv))
	}
	assertArgv(t, inv[1], []string{
		"done", "job-deadbeef",
		"--state", "failed",
		"--message", "check failed: ✗ pre-merge hook",
		"--result-ref", "spinclass session-job-status",
	})
}

func TestStartEmitsCancelledState(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	installStub(t, argsFile, true)

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
		t.Fatalf("ringmaster invocations: got %d, want 2", len(inv))
	}
	assertArgv(t, inv[1], []string{
		"done", "job-deadbeef",
		"--state", "cancelled",
		"--message", "merge cancelled",
		"--result-ref", "spinclass session-job-status",
	})
}

func TestStartNoEmitWhenClownAbsent(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	// CLOWN_BIN deliberately unset (TestMain stripped it): the emit gate is off.

	runWaked(t, wt, KindMerge, func(ctx context.Context, w io.Writer) (string, bool) {
		return "✓ hook", false
	})

	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("ringmaster invoked despite CLOWN_BIN being unset")
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

// Start's immediate-return contract is about the JOB BODY, not the wakeup
// allocation. Since #243 the allocation is deliberately on Start's path (it is
// what makes the returned id match the wake's, and `ringmaster start` is a
// ~7ms local call built for dispatch-time use), so the property worth pinning
// is that the slow thing — fn, the pre-merge gate — still runs detached.
func TestStartDoesNotWaitForTheJobBody(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	installStub(t, argsFile, true)

	release := make(chan struct{})
	began := time.Now()
	if _, err := Start(wt, KindMerge, false, "fast-job", func(ctx context.Context, w io.Writer) (string, bool) {
		<-release
		return "✓ hook", false
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("Start blocked %v on the job body; want return before fn completes", elapsed)
	}
	close(release)
	select {
	case <-WaitDone(wt):
	case <-time.After(10 * time.Second):
		t.Fatal("job did not finish in time")
	}
}

// The core of #243: under clown the id handed back at dispatch IS the
// ringmaster job id the completion wake will carry. Before this, Start
// returned a locally-minted `<kind>-<unix-ts>` while the wake reported
// ringmaster's `<kind>-<hash>` — same prefix, different suffix, so an agent
// matching them reasonably concluded the wake belonged to another session.
func TestStartReturnsRingmasterJobID(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	installStub(t, argsFile, true)

	ret, err := Start(wt, KindMerge, false, "local-fallback-id", func(ctx context.Context, w io.Writer) (string, bool) {
		return "✓ hook", false
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ret.ID != "job-deadbeef" {
		t.Fatalf("returned id %q, want the ringmaster id %q the wake will carry", ret.ID, "job-deadbeef")
	}
	select {
	case <-WaitDone(wt):
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish in time")
	}
	// The persisted record agrees, so session-job-status reports the same id.
	j, err := Read(wt)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if j.ID != "job-deadbeef" {
		t.Fatalf("persisted id %q, want %q", j.ID, "job-deadbeef")
	}
}

// Without clown there is no ringmaster id to adopt, so the caller's locally
// minted id stands and the job simply runs without a wake.
func TestStartKeepsLocalIDWithoutClown(t *testing.T) {
	wt := t.TempDir()
	// CLOWN_BIN deliberately unset (TestMain stripped it).
	ret, err := Start(wt, KindMerge, false, "local-fallback-id", func(ctx context.Context, w io.Writer) (string, bool) {
		return "✓ hook", false
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ret.ID != "local-fallback-id" {
		t.Fatalf("returned id %q, want the caller's local id", ret.ID)
	}
	select {
	case <-WaitDone(wt):
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish in time")
	}
}

func TestStartReturnsSnapshotNotSharedPointer(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	installStub(t, argsFile, true)

	ret, err := Start(wt, KindMerge, false, "snap-job", func(ctx context.Context, w io.Writer) (string, bool) {
		return "✓ hook", false
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
	// mutations (status, result) must not be visible through it — reading them
	// concurrently would be a data race otherwise. ClownJobID is NOT one of
	// those mutations since #243: it is set before the goroutine launches, so
	// the snapshot legitimately carries it.
	if ret.Status != StatusRunning {
		t.Fatalf("returned job mutated by goroutine: status %q, want snapshot %q", ret.Status, StatusRunning)
	}
	if ret.ClownJobID != "job-deadbeef" {
		t.Fatalf("snapshot clown id %q, want it populated at dispatch (%q)", ret.ClownJobID, "job-deadbeef")
	}
	j, err := Read(wt)
	if err != nil || j.Status != StatusSucceeded {
		t.Fatalf("persisted record: status=%v err=%v, want succeeded", j, err)
	}
}

// A failed ALLOCATION under clown refuses the dispatch outright (#243). The
// wake is how an agent learns an async job finished, so a job dispatched
// without one would complete into silence — worse than not dispatching. The
// error names the local id and worktree so the refusal is debuggable without a
// job record, since none was written.
func TestStartRefusesWhenClownAllocationFails(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	installStub(t, argsFile, false)

	ran := make(chan struct{}, 1)
	_, err := Start(wt, KindMerge, false, "local-fallback-id", func(ctx context.Context, w io.Writer) (string, bool) {
		ran <- struct{}{}
		return "✓ hook", false
	})
	if err == nil {
		t.Fatal("expected Start to refuse when the wakeup allocation fails")
	}
	for _, want := range []string{"local-fallback-id", wt, "no completion wake"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	select {
	case <-ran:
		t.Fatal("job body ran despite the refusal")
	case <-time.After(200 * time.Millisecond):
	}
	// No job record, and the worktree is left free for a retry rather than
	// wedged behind a phantom in-flight entry.
	if _, rerr := Read(wt); rerr == nil {
		t.Error("a job record was written for a refused dispatch")
	}
	if IsRunning(wt) {
		t.Error("running entry leaked after a refused dispatch")
	}
}

// A failed TERMINAL emit is still non-fatal: the job has already done its
// work, spinclass's own record is the system of record, and only the wake is
// lost — so it is logged rather than raised.
func TestFinishEmitFailureDoesNotAffectJob(t *testing.T) {
	wt := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	// Succeed on `start` so dispatch proceeds, fail on `done`.
	dir := t.TempDir()
	script := filepath.Join(dir, "ringmaster")
	body := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; echo --; } >> " + argsFile + "\n" +
		"if [ \"$1\" = start ]; then echo job-deadbeef; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub ringmaster: %v", err)
	}
	t.Setenv("CLOWN_BIN", filepath.Join(dir, "clown"))
	t.Setenv("RINGMASTER_BIN", script)

	runWaked(t, wt, KindMerge, func(ctx context.Context, w io.Writer) (string, bool) {
		return "✓ hook", false
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
