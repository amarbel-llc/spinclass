package hookrun_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/embeds"
	. "code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func sptr(s string) *string { return &s }
func bptr(b bool) *bool     { return &b }

// pinDirenv writes an executable fake direnv with the given shell body and pins
// it as the build-time direnv via embeds.Set, restoring the prior pin on
// cleanup. Pinning (rather than relying on PATH) sidesteps direnv.Resolve's
// embeds-over-PATH precedence so the fake is used deterministically.
func pinDirenv(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "direnv")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing fake direnv: %v", err)
	}
	prevMadder, prevDirenv, prevDodder := embeds.MadderBin(), embeds.DirenvBin(), embeds.DodderBin()
	embeds.Set(prevMadder, path, prevDodder)
	t.Cleanup(func() { embeds.Set(prevMadder, prevDirenv, prevDodder) })
}

// processAlive reports whether pid exists. Signal 0 performs the permission
// and existence checks without delivering anything. A zombie still counts as
// alive here, which only makes the assertion stricter.
func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// waitForChildPID polls pidFile until it holds a parseable, positive pid.
func waitForChildPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child pid file never appeared")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestCreateExecutes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook-ran")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Create: sptr(fmt.Sprintf("touch %s", marker))}}

	if err := Create(sf, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected create hook to run and create marker file")
	}
}

func TestCreateReceivesWorktreeEnv(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "worktree-path")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Create: sptr(fmt.Sprintf("echo $WORKTREE > %s", output))}}

	if err := Create(sf, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(output)
	got := strings.TrimSpace(string(data))
	if got != dir {
		t.Errorf("WORKTREE env: got %q, want %q", got, dir)
	}
}

func TestCreateFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Create: sptr("exit 1")}}

	if err := Create(sf, dir, io.Discard); err == nil {
		t.Error("expected error from failing create hook")
	}
}

func TestCreateNilIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := Create(sweatfile.Sweatfile{}, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateEmptyStringIsNoop(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Create: sptr("")}}

	if err := Create(sf, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateMultilineWithEmptyLines(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook-ran")

	// A multiline create hook with empty lines between commands should
	// execute correctly — empty lines must not be fed to the shell as
	// separate commands or cause parse failures.
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Create: sptr(fmt.Sprintf("echo first\n\ntouch %s\n", marker))}}

	if err := Create(sf, dir, io.Discard); err != nil {
		t.Fatalf("multiline create hook with empty lines should not error: %v", err)
	}

	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected multiline create hook to execute and create marker file")
	}
}

func TestPreMergeExecutes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pre-merge-ran")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("touch " + marker)}}

	if err := PreMerge(sf, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected pre-merge hook to run and create marker file")
	}
}

// With a job id in ctx (the pre-merge hook's #25 scope signal) but the scope
// tier disabled, the hook must run BARE — the systemd-run wrap is a no-op when
// ScopeArgv reports unavailable, so a host without a systemd user bus (or with
// RINGMASTER_DISABLE_SCOPE) still runs the hook normally. Guards against a
// scopeJobID-set path accidentally prepending a prefix that isn't runnable. The
// wrap-active path needs a live user bus and is dogfooded, not covered here.
func TestPreMergeScopeDisabledRunsBare(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	dir := t.TempDir()
	marker := filepath.Join(dir, "pre-merge-ran")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("touch " + marker)}}

	ctx := clown.WithJobID(context.Background(), "merge-9f3c1a2b")
	if err := PreMergeContext(ctx, sf, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected pre-merge hook to run bare (scope disabled) and create marker")
	}
}

// TestPreMergeScopeActiveWrapsInCgroup validates the wrap-ACTIVE path on a host
// with a systemd user bus: the pre-merge hook runs inside its
// ringmaster-<id>.scope, so the hook process's own cgroup carries that unit
// name. Skips when the scope tier is unavailable (the checkPhase sandbox and
// macOS have no user bus), so it exercises the real path on a Linux dev host and
// is a clean no-op in CI. This is the only automated coverage of the wrap
// actually taking effect.
func TestPreMergeScopeActiveWrapsInCgroup(t *testing.T) {
	jobID := "merge-scopetest1"
	if _, ok := clown.ScopeArgv(jobID); !ok {
		t.Skip("scope tier unavailable (no systemd user bus); active-path test skipped")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "cgroup")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("cat /proc/self/cgroup > " + marker)}}

	ctx := clown.WithJobID(context.Background(), jobID)
	if err := PreMergeContext(ctx, sf, dir, io.Discard); err != nil {
		t.Fatalf("scoped pre-merge hook: %v", err)
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading cgroup marker: %v", err)
	}
	want := clown.ScopeUnitName(jobID)
	if !strings.Contains(string(content), want) {
		t.Errorf("hook cgroup %q does not contain the scope unit %q",
			strings.TrimSpace(string(content)), want)
	}
}

// TestPreMergeScopeReapsSubtreeOnCancel is the decisive #25 test: it proves the
// scope's control-group kill reaps a hook subtree that IGNORES SIGTERM — the
// exact residual #188's SIGTERM + WaitDelay-SIGKILL + no-Setpgid teardown
// leaves behind. The hook traps SIGTERM and backgrounds a `sleep` that holds
// the inherited pipe; under #188 alone that child is reparented and survives
// the top's SIGKILL, but #25's ScopeStop (`systemctl --user stop
// ringmaster-<id>.scope`) reaps the whole cgroup. Self-skips without a systemd
// user bus (checkPhase sandbox, macOS); runs for real on a Linux dev host.
func TestPreMergeScopeReapsSubtreeOnCancel(t *testing.T) {
	jobID := fmt.Sprintf("merge-scopekill-%d", time.Now().UnixNano())
	if _, ok := clown.ScopeArgv(jobID); !ok {
		t.Skip("scope tier unavailable (no systemd user bus)")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// Top ignores SIGTERM; the backgrounded sleep is the stubborn subtree that
	// survives #188's teardown but must not survive #25's scope reap.
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("trap '' TERM; sleep 300 & echo $! > " + pidFile + "; wait")}}

	ctx, cancel := context.WithCancel(clown.WithJobID(context.Background(), jobID))
	var hookOut bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- PreMergeContext(ctx, sf, dir, &hookOut) }()

	pid := waitForChildPID(t, pidFile)
	// Belt-and-suspenders: never leak the stubborn child (or its scope) past the
	// test, whatever the outcome.
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = clown.ScopeStop(context.Background(), jobID)
	})
	if !processAlive(pid) {
		t.Fatalf("child %d not alive after the hook started", pid)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("PreMergeContext did not return within 30s of cancel")
	}
	t.Logf("scope unit: %s\nhook output after cancel:\n%s", clown.ScopeUnitName(jobID), hookOut.String())

	// The hook has returned, so ScopeStop has run; the subtree must be gone.
	deadline := time.Now().Add(15 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived cancel — the scope did not reap the subtree (#25 regression)", pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestPreMergeReceivesWorktreeEnv(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "worktree-env")

	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("printenv WORKTREE > " + marker)}}

	if err := PreMerge(sf, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading marker: %v", err)
	}
	if strings.TrimSpace(string(content)) != dir {
		t.Errorf("expected WORKTREE=%s, got %q", dir, string(content))
	}
}

func TestPreMergeFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("exit 1")}}

	if err := PreMerge(sf, dir, io.Discard); err == nil {
		t.Error("expected error from failing pre-merge hook")
	}
}

func TestPreMergeNilIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := PreMerge(sweatfile.Sweatfile{}, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreMergeEmptyStringIsNoop(t *testing.T) {
	dir := t.TempDir()
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("")}}

	if err := PreMerge(sf, dir, io.Discard); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Regression test for spinclass#27: the hook MUST NOT write to os.Stdout.
// In `spinclass serve` mode, os.Stdout is the JSON-RPC transport; any byte
// the hook emits there corrupts the protocol and the MCP client closes the
// connection. The hook must write to the caller-provided writer instead.
func TestHookWritesToWriterNotStdout(t *testing.T) {
	dir := t.TempDir()

	// Swap os.Stdout for a pipe so we can observe whether anything is
	// written to it during the hook.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	var hookOut bytes.Buffer
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: sptr("echo STDOUT_LINE; echo STDERR_LINE 1>&2")}}

	if err := PreMerge(sf, dir, &hookOut); err != nil {
		t.Fatalf("hook: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	leaked, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if len(leaked) != 0 {
		t.Errorf("hook leaked %d bytes to os.Stdout: %q", len(leaked), string(leaked))
	}

	got := hookOut.String()
	if !strings.Contains(got, "STDOUT_LINE") {
		t.Errorf("writer missing STDOUT_LINE; got %q", got)
	}
	if !strings.Contains(got, "STDERR_LINE") {
		t.Errorf("writer missing STDERR_LINE; got %q", got)
	}
}
