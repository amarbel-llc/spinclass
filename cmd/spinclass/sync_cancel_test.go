package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// syncCancelProbe is a pre-merge hook whose long-lived child runs from the
// hook's cwd (the build worktree). On SIGTERM the child records, in a file
// outside the worktree, whether that cwd still exists, then exits; the hook
// forwards SIGTERM and waits for it, so the probe proves the ORDER (#188).
type syncCancelProbe struct {
	pidFile, startedFile, diedFile string
}

// installSyncCancelHook writes the hook scripts under t.TempDir() and makes the
// hook the pre-merge hook (alongside the attestation gate) of the fixture repo.
func installSyncCancelHook(t *testing.T, repoPath string) syncCancelProbe {
	t.Helper()
	dir := t.TempDir()
	probe := syncCancelProbe{
		pidFile:     filepath.Join(dir, "child.pid"),
		startedFile: filepath.Join(dir, "started"),
		diedFile:    filepath.Join(dir, "died-with-cwd"),
	}
	// Registered first so it also runs when the test fails before the pid is
	// read in awaitHookStarted.
	t.Cleanup(func() {
		raw, err := os.ReadFile(probe.pidFile)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	childScript := "echo $$ > " + probe.pidFile + "\n" +
		"trap 'if [ -d \"$PWD\" ]; then echo cwd-present; else echo cwd-gone; fi > " + probe.diedFile + "; exit 0' TERM\n" +
		"touch " + probe.startedFile + "\n" +
		"while :; do sleep 1 & wait $!; done\n"
	childPath := filepath.Join(dir, "child.sh")
	if err := os.WriteFile(childPath, []byte(childScript), 0o755); err != nil {
		t.Fatal(err)
	}
	hookScript := "trap 'kill -TERM $child 2>/dev/null; wait $child; exit 143' TERM\n" +
		"sh " + childPath + " &\n" +
		"child=$!\n" +
		"wait $child\n"
	scriptPath := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(scriptPath, []byte(hookScript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweatfile(t, repoPath, "[hooks]\npre-merge = \"sh "+scriptPath+"\"\n\n"+gateSweat)
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	return probe
}

func (p syncCancelProbe) awaitHookStarted(t *testing.T) (childPid int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(p.startedFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, err := os.ReadFile(p.pidFile)
	if err != nil {
		t.Fatal(err)
	}
	childPid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parsing child pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(childPid, syscall.SIGKILL) })
	return childPid
}

// requireDiedInsideLiveCwd fails unless the child's TERM trap ran and saw its
// cwd still present. A missing file means the child was killed without running
// its trap (or never ran), which must not pass vacuously.
func (p syncCancelProbe) requireDiedInsideLiveCwd(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(p.diedFile)
	if err != nil {
		t.Fatalf("hook child never recorded its death (killed without running its TERM trap?): %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "cwd-present" {
		t.Errorf("hook child was told to die with its cwd %q; want cwd-present (build worktree removed before the hook tree died)", got)
	}
}

// syncHookProcessGone reports whether pid is dead. kill(pid, 0) succeeds for a
// zombie, so on Linux /proc is consulted: state Z, or no entry, counts as gone.
func syncHookProcessGone(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if _, procErr := os.Stat("/proc/self/stat"); procErr == nil {
			return true
		}
		return syscall.Kill(pid, 0) != nil
	}
	// The state letter follows the ")" that closes the comm field.
	if i := bytes.LastIndexByte(raw, ')'); i >= 0 && i+2 < len(raw) {
		return raw[i+2] == 'Z'
	}
	return false
}

func awaitSyncHookChildGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !syncHookProcessGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("hook child %d still alive after cancel", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func worktreeDirsWithPrefix(t *testing.T, repoPath string, prefixes ...string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoPath, ".worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		for _, prefix := range prefixes {
			if strings.HasPrefix(e.Name(), prefix) {
				found = append(found, e.Name())
			}
		}
	}
	return found
}

// runHandlerUntilHookStarts runs handle in a goroutine, waits for the hook to
// start, cancels the handler's ctx, and returns the handler's result once it
// returns. Fails the test if the handler outlives the cancel by 30s.
func runHandlerUntilHookStarts(
	t *testing.T,
	probe syncCancelProbe,
	handle func(ctx context.Context) (isErr bool, text string),
) (childPid int, isErr bool, text string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		isErr, text = handle(ctx)
	}()

	childPid = probe.awaitHookStarted(t)
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("handler did not return within 30s of cancel")
	}
	return childPid, isErr, text
}

func TestHandleCheckThisSessionCancelReapsHook(t *testing.T) {
	_, repoPath, branch := gatedWorktreeFixture(t, gateSweat)
	probe := installSyncCancelHook(t, repoPath)

	childPid, isErr, text := runHandlerUntilHookStarts(t, probe, func(ctx context.Context) (bool, string) {
		res, err := handleCheckThisSession(ctx, json.RawMessage(`{}`), nil)
		if err != nil {
			t.Errorf("transport error: %v", err)
			return true, ""
		}
		return res.IsErr, res.Text
	})

	if !isErr {
		t.Errorf("cancelled check should be an error result, got: %s", text)
	}
	awaitSyncHookChildGone(t, childPid)
	probe.requireDiedInsideLiveCwd(t)
	if left := worktreeDirsWithPrefix(t, repoPath, ".merge-"); len(left) != 0 {
		t.Errorf("leftover build worktrees: %v", left)
	}
	a := readAttestation(t, repoPath, branch)
	if a == nil {
		t.Fatal("attestation consumed by a cancelled check")
	}
	if a.Claim != nil {
		t.Errorf("claim not released after cancel: %+v", a.Claim)
	}
}

func TestHandleMergeThisSessionCancelReapsHookAndLandsNothing(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, gateSweat)
	probe := installSyncCancelHook(t, repoPath)
	commitFile(t, cwd, "x.txt")
	defaultBranch, err := merge.ResolveDefaultBranch(repoPath)
	if err != nil {
		t.Fatalf("resolve default branch: %v", err)
	}
	shaBefore := testgit.MustGit(t, repoPath, "rev-parse", defaultBranch)

	childPid, isErr, text := runHandlerUntilHookStarts(t, probe, func(ctx context.Context) (bool, string) {
		res, err := handleMergeThisSession(ctx, json.RawMessage(`{"local_only":true}`), nil)
		if err != nil {
			t.Errorf("transport error: %v", err)
			return true, ""
		}
		return res.IsErr, res.Text
	})

	if !isErr {
		t.Errorf("cancelled merge should be an error result, got: %s", text)
	}
	awaitSyncHookChildGone(t, childPid)
	probe.requireDiedInsideLiveCwd(t)
	if left := worktreeDirsWithPrefix(t, repoPath, ".merge-", ".land-"); len(left) != 0 {
		t.Errorf("leftover gate worktrees: %v", left)
	}
	if shaAfter := testgit.MustGit(t, repoPath, "rev-parse", defaultBranch); shaAfter != shaBefore {
		t.Errorf("default branch moved on a cancelled merge: %s -> %s", shaBefore, shaAfter)
	}
	a := readAttestation(t, repoPath, branch)
	if a == nil {
		t.Fatal("attestation consumed by a cancelled merge")
	}
	if a.Claim != nil {
		t.Errorf("claim not released after cancel: %+v", a.Claim)
	}
}
