package hookrun_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func TestPostMergeRuns(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMerge: sptr("echo deployed")}}
	var buf bytes.Buffer
	if err := PostMerge(context.Background(), sf, dir, nil, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if got := strings.TrimSpace(buf.String()); got != "deployed" {
		t.Errorf("expected hook output 'deployed', got %q", got)
	}
}

func TestPostMergeInactiveIsNoop(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := PostMerge(context.Background(), sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{}}, dir, nil, &buf); err != nil {
		t.Fatalf("inactive post-merge should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("inactive post-merge wrote output: %q", buf.String())
	}

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMerge: sptr("echo nope"), DisablePostMerge: bptr(true)}}
	buf.Reset()
	if err := PostMerge(context.Background(), sf, dir, nil, &buf); err != nil {
		t.Fatalf("disabled post-merge should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("disabled post-merge ran: %q", buf.String())
	}
}

func TestPostMergePropagatesFailure(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMerge: sptr("echo boom >&2; exit 1")}}
	var buf bytes.Buffer
	if err := PostMerge(context.Background(), sf, dir, nil, &buf); err == nil {
		t.Fatalf("expected nonzero post-merge to error, got nil (output: %q)", buf.String())
	}
	// The runner reports the failure; deciding it is non-fatal is the merge
	// package's job (runPostMergePhase), not the runner's.
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("expected hook stderr captured, got %q", buf.String())
	}
}

// extraEnv must reach the hook process, and must win over an inherited value
// of the same key (it is appended last).
func TestPostMergeExtraEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPINCLASS_MERGED_SHA", "inherited-and-should-lose")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMerge: sptr("echo sha=$SPINCLASS_MERGED_SHA pushed=$SPINCLASS_MERGE_PUSHED")}}
	var buf bytes.Buffer
	err := PostMerge(context.Background(), sf, dir, []string{
		"SPINCLASS_MERGED_SHA=abc123",
		"SPINCLASS_MERGE_PUSHED=1",
	}, &buf)
	if err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if got := strings.TrimSpace(buf.String()); got != "sha=abc123 pushed=1" {
		t.Errorf("extraEnv not applied: got %q", got)
	}
}

// A hook that outruns the cap is killed, with a message naming the knob so the
// operator can raise or disable it.
func TestPostMergeEnforcesTimeout(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{
		PostMerge:        sptr("sleep 30"),
		PostMergeTimeout: sptr("150ms"),
	}}

	var buf bytes.Buffer
	start := time.Now()
	err := PostMerge(context.Background(), sf, dir, nil, &buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the capped hook to error")
	}
	if !strings.Contains(err.Error(), "post-merge-timeout") {
		t.Errorf("error should name the knob, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("hook ran %s; the cap should have killed it near 150ms", elapsed)
	}
}

// The cap must bound WALL-CLOCK time even when the hook leaves a child holding
// its output pipe — the common shape, since a deploy script's subprocesses
// inherit stdout by default.
//
// This is the property the cap exists for, and the one that was broken when
// post-merge-timeout first landed: cancelling the context kills the shell on
// time, but os/exec's Wait keeps draining until every holder of the pipe's
// write end closes it, so a surviving descendant held the merge lock for its
// full lifetime regardless of the cap. Measured then: a 200ms cap with a 3s
// pipe-holding child returned after 3.0s. cmd.WaitDelay is the fix.
//
// A cap that only works when the hook has no subprocesses is not a cap, so
// this asserts the real bound rather than the happy path.
func TestPostMergeCapBoundsWallClockWithPipeHoldingChild(t *testing.T) {
	dir := t.TempDir()
	// Child inherits stdout/stderr (deliberately no redirect) and outlives the
	// cap by a wide margin. `wait` keeps the shell from exiting early, so the
	// only way out is the cap.
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{
		PostMerge:        sptr("sh -c 'sleep 60' &\nwait\n"),
		PostMergeTimeout: sptr("200ms"),
	}}

	var buf bytes.Buffer
	start := time.Now()
	err := PostMerge(context.Background(), sf, dir, nil, &buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the capped hook to error")
	}
	// Cap + WaitDelay, with slack for a loaded machine — the point is that it
	// is bounded at all, nowhere near the child's 60s.
	if elapsed > 30*time.Second {
		t.Fatalf("hook returned after %s despite a 200ms cap: the cap is not "+
			"bounding wall-clock time, so a wedged hook still holds the merge lock", elapsed)
	}
}

// The cap is a wall-clock bound, not an inactivity one: a hook that is
// completely silent but finishes inside the cap must succeed. (A deploy can
// legitimately produce no output for minutes.)
func TestPostMergeSilentHookWithinCapSucceeds(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{
		PostMerge:        sptr("sleep 0.3"),
		PostMergeTimeout: sptr("10s"),
	}}
	var buf bytes.Buffer
	if err := PostMerge(context.Background(), sf, dir, nil, &buf); err != nil {
		t.Fatalf("silent-but-prompt hook should pass a wall-clock cap, got %v", err)
	}
}

// "0" means no cap: a hook that would blow the default must run to completion.
func TestPostMergeZeroDisablesCap(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "done")
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{
		PostMerge:        sptr(fmt.Sprintf("sleep 0.3; echo ok > %s", marker)),
		PostMergeTimeout: sptr("0"),
	}}
	var buf bytes.Buffer
	if err := PostMerge(context.Background(), sf, dir, nil, &buf); err != nil {
		t.Fatalf("uncapped hook should run to completion, got %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("hook did not complete: %v", err)
	}
}

// A caller cancel (session-job-cancel) must NOT be misreported as a cap kill —
// the two have different remedies, so they must read differently.
func TestPostMergeCallerCancelIsNotReportedAsTimeout(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{
		PostMerge:        sptr("sleep 30"),
		PostMergeTimeout: sptr("30s"), // generous; the caller cancels first
	}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	var buf bytes.Buffer
	err := PostMerge(ctx, sf, dir, nil, &buf)
	if err == nil {
		t.Fatal("expected an error when the caller cancels")
	}
	if strings.Contains(err.Error(), "post-merge-timeout") {
		t.Errorf("caller cancel misreported as a cap kill: %v", err)
	}
}

// A post-merge hook may detach a child that outlives it. This is the
// recommended shape for a deploy trigger (FDR 0023): the hook backgrounds the
// slow work and returns immediately, so the per-repo merge lock — which the
// post-merge hook runs UNDER — is not held for the deploy's duration.
//
// Two properties are load-bearing for that consumer, so pin both:
//
//  1. The hook returns promptly rather than waiting out the child. os/exec's
//     Wait blocks until every holder of the stdout pipe's write end closes
//     it, so a detached child that inherits the pipe would stall the hook for
//     its full duration. Redirecting the child's stdout+stderr away from the
//     pipe is what avoids that — hence the `>log 2>&1` below, which is not
//     cosmetic.
//  2. The child survives the hook's exit. spinclass sets no process group on
//     hook subprocesses and does not reap the tree, so it does today. If
//     spinclass#188's process-tree kill lands and is applied to the
//     post-merge hook, this test fails loudly rather than letting a
//     consumer's deploys be silently dropped.
//
// Uses plain `&` (same process group) deliberately: it is the strictest probe
// for "does spinclass reap descendants". A consumer wanting to survive a
// future process-GROUP kill should additionally `setsid`.
//
// Note this runs under the DEFAULT post-merge cap (#246), so it also pins a
// third property that is easy to break by accident: the cap's
// `defer cancel()` fires the moment the hook returns, and that must not reap
// the detached child. It does not, because exec's cancel kills only the
// direct child (the shell, already exited) and no process group is set.
func TestPostMergeDetachedChildOutlivesHook(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "deployed")
	childLog := filepath.Join(dir, "deploy.log")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMerge: sptr(fmt.Sprintf(
		"sh -c 'sleep 1; echo done > %s' </dev/null >%s 2>&1 &\necho trigger-fired",
		marker, childLog,
	))}}

	var buf bytes.Buffer
	start := time.Now()
	if err := PostMerge(context.Background(), sf, dir, nil, &buf); err != nil {
		t.Fatalf("hook failed: %v\noutput: %s", err, buf.String())
	}
	elapsed := time.Since(start)

	if !strings.Contains(buf.String(), "trigger-fired") {
		t.Errorf("hook output not captured: %q", buf.String())
	}
	// Property 1: returned without waiting out the ~1s child.
	if elapsed > 500*time.Millisecond {
		t.Errorf("hook blocked %s on its detached child; a backgrounded deploy "+
			"would still hold the merge lock for the deploy's duration", elapsed)
	}
	// Guard: if the child had already finished, the survival check below would
	// pass vacuously.
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("detached child finished before the hook returned; test cannot " +
			"distinguish survival from inline execution")
	}

	// Property 2: the child still completes after the hook returned.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("detached child did not survive the hook's exit — a " +
				"backgrounded deploy would be silently dropped, not fired")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The post-merge hook runs in the devshell of its directory when a .envrc is
// present, matching every other lifecycle hook (spinclass#198).
func TestPostMergeWrapsWithDirenvWhenEnvrcPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}
	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\nshift 2\nexec \"$@\"\n")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMerge: sptr("echo deployed")}}
	var buf bytes.Buffer
	if err := PostMerge(context.Background(), sf, dir, nil, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "DIRENV_WRAPPED") {
		t.Errorf("expected hook to run via direnv exec, got %q", out)
	}
	if !strings.Contains(out, "deployed") {
		t.Errorf("expected wrapped script output 'deployed', got %q", out)
	}
}
