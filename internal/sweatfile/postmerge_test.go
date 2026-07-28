package sweatfile_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestParseHooksPostMerge(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(
		"[hooks]\npost-merge = \"deploy.sh\"\ndisable-post-merge = true\n",
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.PostMerge == nil || *sf.Hooks.PostMerge != "deploy.sh" {
		t.Fatalf("hooks.post-merge: got %+v", sf.Hooks)
	}
	if sf.Hooks.DisablePostMerge == nil || !*sf.Hooks.DisablePostMerge {
		t.Fatalf("hooks.disable-post-merge: got %+v", sf.Hooks)
	}
	// The regenerated tommy decoder must consume both keys, else `sc validate`
	// would flag them as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("post-merge/disable-post-merge left undecoded: %v", u)
	}
}

func TestPostMergeActive(t *testing.T) {
	cases := []struct {
		name    string
		cmd     *string
		disable *bool
		want    bool
	}{
		{"unset", nil, nil, false},
		{"empty", sptr(""), nil, false},
		{"whitespace-only", sptr("  \n\t\n"), nil, false},
		{"set", sptr("deploy.sh"), nil, true},
		{"set-but-disabled", sptr("deploy.sh"), bptr(true), false},
		{"set-disable-false", sptr("deploy.sh"), bptr(false), true},
	}
	for _, c := range cases {
		sf := Sweatfile{Hooks: &Hooks{PostMerge: c.cmd, DisablePostMerge: c.disable}}
		if got := sf.PostMergeActive(); got != c.want {
			t.Errorf("%s: PostMergeActive() = %v, want %v", c.name, got, c.want)
		}
	}
	if (Sweatfile{}).PostMergeActive() {
		t.Error("nil Hooks: PostMergeActive() = true, want false")
	}
}

func TestPostMergeDisabled(t *testing.T) {
	if (Sweatfile{}).PostMergeDisabled() {
		t.Error("nil Hooks: PostMergeDisabled() = true, want false")
	}
	if (Sweatfile{Hooks: &Hooks{}}).PostMergeDisabled() {
		t.Error("nil DisablePostMerge: PostMergeDisabled() = true, want false")
	}
	if !(Sweatfile{Hooks: &Hooks{DisablePostMerge: bptr(true)}}).PostMergeDisabled() {
		t.Error("DisablePostMerge=true: PostMergeDisabled() = false, want true")
	}
}

func TestMergeHooksPostMergeOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy-a")}}
	repo := Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy-b")}}
	merged := base.MergeWith(repo)
	if merged.Hooks.PostMerge == nil || *merged.Hooks.PostMerge != "deploy-b" {
		t.Errorf("expected scalar override to deploy-b, got %+v", merged.Hooks)
	}
}

func TestMergeHooksPostMergeInherit(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy-a")}}
	merged := base.MergeWith(Sweatfile{})
	if merged.Hooks == nil || merged.Hooks.PostMerge == nil || *merged.Hooks.PostMerge != "deploy-a" {
		t.Errorf("expected inherited post-merge, got %+v", merged.Hooks)
	}
}

// A child sweatfile can suppress an inherited post-merge command without
// clearing the string (the disable-* opt-out shape).
func TestMergeHooksDisablePostMergeOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy-a")}}
	repo := Sweatfile{Hooks: &Hooks{DisablePostMerge: bptr(true)}}
	merged := base.MergeWith(repo)
	if merged.PostMergeActive() {
		t.Errorf("expected post-merge suppressed by disable-post-merge; hooks=%+v", merged.Hooks)
	}
	if merged.Hooks.PostMerge == nil || *merged.Hooks.PostMerge != "deploy-a" {
		t.Errorf("expected command still inherited (suppression, not clearing), got %+v", merged.Hooks)
	}
}

func TestRunPostMergeHookContextRuns(t *testing.T) {
	dir := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo deployed")}}
	var buf bytes.Buffer
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if got := strings.TrimSpace(buf.String()); got != "deployed" {
		t.Errorf("expected hook output 'deployed', got %q", got)
	}
}

func TestRunPostMergeHookContextInactiveIsNoop(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := (Sweatfile{Hooks: &Hooks{}}).RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
		t.Fatalf("inactive post-merge should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("inactive post-merge wrote output: %q", buf.String())
	}

	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo nope"), DisablePostMerge: bptr(true)}}
	buf.Reset()
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
		t.Fatalf("disabled post-merge should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("disabled post-merge ran: %q", buf.String())
	}
}

func TestRunPostMergeHookContextPropagatesFailure(t *testing.T) {
	dir := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo boom >&2; exit 1")}}
	var buf bytes.Buffer
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err == nil {
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
func TestRunPostMergeHookContextExtraEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPINCLASS_MERGED_SHA", "inherited-and-should-lose")

	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo sha=$SPINCLASS_MERGED_SHA pushed=$SPINCLASS_MERGE_PUSHED")}}
	var buf bytes.Buffer
	err := sf.RunPostMergeHookContext(context.Background(), dir, []string{
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
func TestRunPostMergeHookContextDetachedChildOutlivesHook(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "deployed")
	childLog := filepath.Join(dir, "deploy.log")

	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr(fmt.Sprintf(
		"sh -c 'sleep 1; echo done > %s' </dev/null >%s 2>&1 &\necho trigger-fired",
		marker, childLog,
	))}}

	var buf bytes.Buffer
	start := time.Now()
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
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
func TestRunPostMergeHookContextWrapsWithDirenvWhenEnvrcPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}
	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\nshift 2\nexec \"$@\"\n")

	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo deployed")}}
	var buf bytes.Buffer
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
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
