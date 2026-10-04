package main

import (
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

// syncCancelProbe is a pre-merge hook that records its child's pid, then waits
// on the child until SIGTERM, which it forwards.
type syncCancelProbe struct {
	pidFile, startedFile string
}

// installSyncCancelHook writes the hook script under t.TempDir() and makes it
// the pre-merge hook (alongside the attestation gate) of the fixture repo.
func installSyncCancelHook(t *testing.T, repoPath string) syncCancelProbe {
	t.Helper()
	dir := t.TempDir()
	probe := syncCancelProbe{
		pidFile:     filepath.Join(dir, "child.pid"),
		startedFile: filepath.Join(dir, "started"),
	}
	script := "sleep 600 &\n" +
		"child=$!\n" +
		"echo $child > " + probe.pidFile + "\n" +
		"trap 'kill -TERM $child 2>/dev/null; exit 143' TERM\n" +
		"touch " + probe.startedFile + "\n" +
		"wait $child\n"
	scriptPath := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
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

func awaitSyncHookChildGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
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
