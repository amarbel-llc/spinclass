package hookrun

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

const (
	fallbackWarning = "running unscoped"
	busRefusal      = "Failed to connect to bus"
)

// Stand-ins for clown.ScopeArgv's `systemd-run … --` prefix. Each receives the
// scoped payload as trailing argv, exactly as systemd-run would.
var (
	// Refuses like systemd-run with no user bus: complains on stderr, exits 1,
	// never runs its payload.
	refusingScopePrefix = []string{"sh", "-c", "echo '" + busRefusal + "' >&2; exit 1", "fake-systemd-run"}
	// Sets the "scope" up and runs the payload.
	workingScopePrefix = []string{"env"}
)

// fakeScope swaps the scope prefix for the test and returns a ctx carrying a
// scope id, as every pre-merge gate's ctx does. RINGMASTER_DISABLE_SCOPE keeps
// the cancel path's ScopeStop away from the host's systemd.
func fakeScope(t *testing.T, prefix []string) context.Context {
	t.Helper()
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	prev := scopeArgv
	scopeArgv = func(string) ([]string, bool) { return prefix, true }
	t.Cleanup(func() { scopeArgv = prev })
	return clown.WithScopeID(context.Background(), "sync-check-test")
}

func preMergeSweatfile(script string, requireHookScope bool) sweatfile.Sweatfile {
	return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: &script, RequireHookScope: &requireHookScope}}
}

// hookRuns counts the lines the hook appended to its run log: one per run.
func hookRuns(t *testing.T, runLog string) int {
	t.Helper()
	data, err := os.ReadFile(runLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("reading run log: %v", err)
	}
	return strings.Count(string(data), "\n")
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertFellBackOnce(t *testing.T, err error, runLog, output string) {
	t.Helper()
	if err != nil {
		t.Fatalf("gate failed instead of falling back to the bare hook: %v\n%s", err, output)
	}
	if got := hookRuns(t, runLog); got != 1 {
		t.Errorf("hook ran %d times, want exactly 1", got)
	}
	if !strings.Contains(output, "[spinclass] hook scope unavailable (") || !strings.Contains(output, fallbackWarning) {
		t.Errorf("no fallback warning in hook output: %q", output)
	}
	if strings.Contains(output, "require-hook-scope") {
		t.Errorf("default fallback output mentions require-hook-scope: %q", output)
	}
}

func TestScopeSetupFailureFallsBackToBareHookWithWarn(t *testing.T) {
	ctx := fakeScope(t, refusingScopePrefix)
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")

	var buf bytes.Buffer
	err := PreMergeContext(ctx, preMergeSweatfile("echo ran >> "+runLog, false), dir, &buf)

	assertFellBackOnce(t, err, runLog, buf.String())
	if !strings.Contains(buf.String(), busRefusal) {
		t.Errorf("warning does not carry the scope's own complaint: %q", buf.String())
	}
}

func TestScopeSpawnErrorFallsBack(t *testing.T) {
	ctx := fakeScope(t, []string{filepath.Join(t.TempDir(), "no-such-systemd-run")})
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")

	var buf bytes.Buffer
	err := PreMergeContext(ctx, preMergeSweatfile("echo ran >> "+runLog, false), dir, &buf)

	assertFellBackOnce(t, err, runLog, buf.String())
}

func TestScopeSetupFailureFailsGateWhenRequireHookScope(t *testing.T) {
	ctx := fakeScope(t, refusingScopePrefix)
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")

	var buf bytes.Buffer
	err := PreMergeContext(ctx, preMergeSweatfile("echo ran >> "+runLog, true), dir, &buf)

	if err == nil {
		t.Fatalf("gate passed although the scope could not be set up and require-hook-scope is set\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "require-hook-scope") || !strings.Contains(err.Error(), busRefusal) {
		t.Errorf("error must name the knob and the cause, got: %v", err)
	}
	if got := hookRuns(t, runLog); got != 0 {
		t.Errorf("hook ran %d times, want 0", got)
	}
	if strings.Contains(buf.String(), fallbackWarning) {
		t.Errorf("fallback warning printed although the gate failed: %q", buf.String())
	}
}

// The invariant the fallback must never break: a hook that started is not run
// a second time, whatever it exited with.
func TestScopedHookFailureIsNotRetried(t *testing.T) {
	ctx := fakeScope(t, workingScopePrefix)
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")

	var buf bytes.Buffer
	err := PreMergeContext(ctx, preMergeSweatfile("echo ran >> "+runLog+"; exit 3", false), dir, &buf)

	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("want the hook's own exit status 3, got: %v", err)
	}
	if got := hookRuns(t, runLog); got != 1 {
		t.Errorf("hook ran %d times, want exactly 1", got)
	}
	if strings.Contains(buf.String(), fallbackWarning) {
		t.Errorf("a hook failure was reported as a scope failure: %q", buf.String())
	}
}

// A hook that wipes the temp dir takes the start marker's directory entry with
// it; that must not read as "never started".
func TestScopedHookThatDeletesItsStartMarkerIsNotRetried(t *testing.T) {
	ctx := fakeScope(t, workingScopePrefix)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")

	var buf bytes.Buffer
	script := fmt.Sprintf("echo ran >> %s; rm -rf %s/*; exit 3", runLog, tmp)
	err := PreMergeContext(ctx, preMergeSweatfile(script, false), dir, &buf)

	if err == nil {
		t.Fatalf("want the hook's failure, got nil\n%s", buf.String())
	}
	if got := hookRuns(t, runLog); got != 1 {
		t.Errorf("hook ran %d times, want exactly 1", got)
	}
}

func TestCancelDuringScopedHookDoesNotFallBack(t *testing.T) {
	ctx, cancel := context.WithCancel(fakeScope(t, workingScopePrefix))
	defer cancel()
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")
	started := filepath.Join(dir, "started")

	// exec, so the cancelled process is the sleep itself and nothing outlives it.
	script := fmt.Sprintf("echo ran >> %s; touch %s; exec sleep 600", runLog, started)
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- PreMergeContext(ctx, preMergeSweatfile(script, false), dir, &buf) }()

	waitForFile(t, started)
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("cancelled hook did not return within 30s")
	}

	if err == nil {
		t.Error("a cancelled hook reported success")
	}
	if got := hookRuns(t, runLog); got != 1 {
		t.Errorf("hook ran %d times, want exactly 1", got)
	}
	if strings.Contains(buf.String(), fallbackWarning) {
		t.Errorf("a cancel was reported as a scope failure: %q", buf.String())
	}
}

// A cancel that lands before the hook starts leaves the same evidence as a
// refused scope (no start marker). It is still a cancel, not a setup failure.
func TestCancelBeforeScopedHookStartsDoesNotFallBack(t *testing.T) {
	ctx, cancel := context.WithCancel(fakeScope(t, refusingScopePrefix))
	cancel()
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")

	var buf bytes.Buffer
	err := PreMergeContext(ctx, preMergeSweatfile("echo ran >> "+runLog, true), dir, &buf)

	if err == nil {
		t.Error("a cancelled gate reported success")
	} else if strings.Contains(err.Error(), "require-hook-scope") {
		t.Errorf("a cancel was reported as a scope setup failure: %v", err)
	}
	if got := hookRuns(t, runLog); got != 0 {
		t.Errorf("hook ran %d times after a cancel, want 0", got)
	}
	if strings.Contains(buf.String(), fallbackWarning) {
		t.Errorf("a cancel was reported as a scope failure: %q", buf.String())
	}
}

// After a fallback the hook really is unscoped, so the Task 6 escalation
// warning must apply to it.
func TestFallbackHookIsTreatedAsUnscopedOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(fakeScope(t, refusingScopePrefix))
	defer cancel()
	dir := t.TempDir()
	started := filepath.Join(dir, "started")

	// The ignored SIGTERM survives the exec, so the sleep is SIGKILLed at
	// cancelGrace with no descendant left behind.
	script := fmt.Sprintf("trap '' TERM; touch %s; exec sleep 600", started)
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- PreMergeContext(ctx, preMergeSweatfile(script, false), dir, &buf) }()

	waitForFile(t, started)
	cancel()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("a SIGTERM-ignoring hook wedged the cancel")
	}

	if !strings.Contains(buf.String(), "may still be running") {
		t.Errorf("no unscoped-escalation warning after a fallback: %q", buf.String())
	}
}
