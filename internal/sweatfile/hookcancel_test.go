package sweatfile_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
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
func TestRunPreMergeHookCancelTearsDownChildren(t *testing.T) {
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

	sf := Sweatfile{Hooks: &Hooks{PreMerge: sptr(script)}}
	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- sf.RunPreMergeHookContext(ctx, dir, &buf) }()

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
}

// A hook that swallows SIGTERM must still not wedge the cancel forever: the
// WaitDelay escalation closes its pipes and SIGKILLs it. This is the residual
// path the doc comment calls out, so pin that it terminates rather than hangs.
func TestRunPreMergeHookCancelEscalatesPastIgnoredSIGTERM(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")

	sf := Sweatfile{Hooks: &Hooks{PreMerge: sptr(fmt.Sprintf(
		"trap '' TERM\ntouch %s\nsleep 600\n", started,
	))}}
	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- sf.RunPreMergeHookContext(ctx, dir, &buf) }()

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

// processAlive reports whether pid exists. Signal 0 performs the permission
// and existence checks without delivering anything. A zombie still counts as
// alive here, which only makes the assertion stricter.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
