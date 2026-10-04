package hookrun_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	. "code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// Cancelling a hook must tear down its CHILDREN, not merely stop waiting on
// them (spinclass#188).
//
// exec.CommandContext's default Cancel is Process.Kill() — SIGKILL, which the
// hook cannot trap, so it gets no chance to stop what it spawned. Measured on
// a real pre-merge gate: the orphaned `nix` kept the inherited stdout pipe
// open and Wait did not return for 224 seconds. Overriding Cancel to SIGTERM
// lets the hook propagate teardown; the same probe then freed the pipe in
// under a second.
//
// The shell here mimics that shape: a child that would outlive its parent by
// far, and a parent that forwards SIGTERM the way `just` does. Two distinct
// things are asserted, because passing only the first is exactly the illusion
// WaitDelay alone would have produced:
//
//  1. the call returns promptly, and
//  2. the CHILD is actually gone — not merely abandoned still running.
func TestPreMergeCancelTearsDownChildren(t *testing.T) {
	dir := t.TempDir()
	childPID := filepath.Join(dir, "child.pid")
	started := filepath.Join(dir, "started")

	// trap forwards SIGTERM to the child, as a well-behaved runner does. The
	// child sleeps far longer than the test tolerates, so if it survives the
	// cancel the assertion below sees it alive.
	//
	// The parent records the child's pid itself (from $!) BEFORE touching the
	// ready marker, so `started` can only appear once child.pid is already on
	// disk. Letting the backgrounded child self-report its own $$ raced the
	// parent's `touch`: the marker could win, and readPID would then open a
	// child.pid that did not exist yet (spinclass#271).
	script := fmt.Sprintf(`
sleep 600 &
child=$!
echo $child > %s
trap 'kill -TERM $child 2>/dev/null; exit 143' TERM
touch %s
wait $child
`, childPID, started)

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr(script)}}
	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- PreMergeContext(ctx, sf, dir, &buf) }()

	waitFor(t, started, 30*time.Second, "hook never started")
	pid := readPID(t, childPID)
	if !processAlive(pid) {
		t.Fatalf("child %d not alive before cancel; the fixture proves nothing", pid)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("cancelled hook did not return within 30s")
	}

	// Property 2: the child is gone. Allow a moment for signal delivery and
	// reaping; the point is that it dies at all, not the exact instant.
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived the cancel — the hook's process tree was "+
				"abandoned, not torn down, so an orphaned build outlives the merge", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A hook that honoured SIGTERM needed no escalation, so it is not warned about.
	if strings.Contains(buf.String(), unscopedWarning) {
		t.Errorf("unexpected escalation warning for a hook that honoured SIGTERM: %q", buf.String())
	}
}

// unscopedWarning is the phrase the operator-facing line carries when a
// cancelled hook needed SIGKILL and no systemd scope reaped its children.
const unscopedWarning = "may still be running"

// With no scope tier, a hook that swallows SIGTERM is SIGKILLed alone after
// cancelGrace and its descendants survive; the operator must be told (#188).
func TestPreMergeCancelWarnsWhenUnscopedEscalationFires(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	dir := t.TempDir()
	childPID := filepath.Join(dir, "child.pid")
	started := filepath.Join(dir, "started")

	// The background child inherits the ignored SIGTERM, so it survives the
	// top process's SIGKILL: exactly the case the warning is about.
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr(fmt.Sprintf(
		"trap '' TERM\nsleep 600 &\necho $! > %s\ntouch %s\nwait\n", childPID, started,
	))}}
	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- PreMergeContext(ctx, sf, dir, &buf) }()

	waitFor(t, started, 30*time.Second, "hook never started")
	t.Cleanup(func() { _ = syscall.Kill(readPID(t, childPID), syscall.SIGKILL) })
	cancel()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("a SIGTERM-ignoring hook wedged the cancel; WaitDelay escalation did not fire")
	}
	if !strings.Contains(buf.String(), unscopedWarning) {
		t.Errorf("no escalation warning in hook output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "ignored SIGTERM") {
		t.Errorf("warning does not say the hook ignored SIGTERM: %q", buf.String())
	}
}

// The top process exits on SIGTERM but a descendant (e.g. a sanctioned FDR 0023
// detached child) still holds the output pipe past the grace period. The hook
// did NOT ignore SIGTERM, and the warning must not claim it did.
func TestPreMergeCancelWarnsAccuratelyWhenOnlyAPipeHolderOutlivesTheHook(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	dir := t.TempDir()
	childPID := filepath.Join(dir, "child.pid")
	started := filepath.Join(dir, "started")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr(fmt.Sprintf(
		"trap 'exit 0' TERM\nsleep 600 &\necho $! > %s\ntouch %s\nwait\n", childPID, started,
	))}}
	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- PreMergeContext(ctx, sf, dir, &buf) }()

	waitFor(t, started, 30*time.Second, "hook never started")
	t.Cleanup(func() { _ = syscall.Kill(readPID(t, childPID), syscall.SIGKILL) })
	cancel()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("a lingering pipe holder wedged the cancel; WaitDelay did not fire")
	}
	out := buf.String()
	if !strings.Contains(out, unscopedWarning) {
		t.Errorf("no warning that descendants may still be running: %q", out)
	}
	if strings.Contains(out, "ignored SIGTERM") {
		t.Errorf("warning claims the hook ignored SIGTERM, but it exited on it: %q", out)
	}
	if !strings.Contains(out, "output pipe") {
		t.Errorf("warning does not name the held output pipe: %q", out)
	}
}

// A hook that swallows SIGTERM must still not wedge the cancel forever: the
// WaitDelay escalation closes its pipes and SIGKILLs it. This is the residual
// path the doc comment calls out, so pin that it terminates rather than hangs.
func TestPreMergeCancelEscalatesPastIgnoredSIGTERM(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr(fmt.Sprintf(
		"trap '' TERM\ntouch %s\nsleep 600\n", started,
	))}}
	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- PreMergeContext(ctx, sf, dir, &buf) }()

	waitFor(t, started, 30*time.Second, "hook never started")
	cancel()

	// cancelGrace is 10s; allow generous slack for a loaded machine. The
	// assertion is that it is bounded at all, versus the 224s a real
	// orphaned build held the pipe before #188.
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("a SIGTERM-ignoring hook wedged the cancel; WaitDelay escalation did not fire")
	}
}

func waitFor(t *testing.T, path string, within time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading child pid: %v", err)
	}
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("unparseable child pid %q: %v", data, err)
	}
	return pid
}
