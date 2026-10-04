package check

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
)

// cancelHookProbe is a pre-merge hook that records its child's pid and its own
// cwd, then waits on the child until SIGTERM, which it forwards.
type cancelHookProbe struct {
	pidFile, cwdFile, startedFile string
}

// installCancelHook writes the hook script under t.TempDir() and a sweatfile in
// wtPath that runs it as the pre-merge hook.
func installCancelHook(t *testing.T, wtPath string) cancelHookProbe {
	t.Helper()
	dir := t.TempDir()
	probe := cancelHookProbe{
		pidFile:     filepath.Join(dir, "child.pid"),
		cwdFile:     filepath.Join(dir, "hook.cwd"),
		startedFile: filepath.Join(dir, "started"),
	}
	script := "sleep 600 &\n" +
		"child=$!\n" +
		"echo $child > " + probe.pidFile + "\n" +
		"trap 'kill -TERM $child 2>/dev/null; exit 143' TERM\n" +
		"pwd > " + probe.cwdFile + "\n" +
		"touch " + probe.startedFile + "\n" +
		"wait $child\n"
	scriptPath := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"sh "+scriptPath+"\"\n")
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	return probe
}

// awaitHookStarted blocks until the hook has recorded its child pid and cwd,
// registers a SIGKILL cleanup for the child, and returns both.
func (p cancelHookProbe) awaitHookStarted(t *testing.T) (childPid int, hookCwd string) {
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
	cwd, err := os.ReadFile(p.cwdFile)
	if err != nil {
		t.Fatal(err)
	}
	return childPid, strings.TrimSpace(string(cwd))
}

func awaitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("hook child %d still alive after cancel", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func buildWorktreesUnder(t *testing.T, repoDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoDir, ".worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), BuildWorktreePrefix) {
			found = append(found, e.Name())
		}
	}
	return found
}

// Cancelling RunContext must reap the hook's process tree before the deferred
// cleanup removes the build worktree, so no process outlives its cwd (#188).
func TestRunContext_CancelReapsHookBeforeRemovingBuildWorktree(t *testing.T) {
	_, repoDir, wtPath := setupRepoWithWorktree(t, "feature-cancel")
	probe := installCancelHook(t, wtPath)

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		_, runErr = RunContext(ctx, rep, wtPath, nil)
	}()

	childPid, hookCwd := probe.awaitHookStarted(t)
	if !strings.HasPrefix(filepath.Base(hookCwd), BuildWorktreePrefix) {
		t.Fatalf("hook cwd %q is not a build worktree", hookCwd)
	}
	if _, err := os.Stat(hookCwd); err != nil {
		t.Fatalf("build worktree missing while the hook runs: %v", err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunContext did not return within 30s of cancel")
	}
	if runErr == nil {
		t.Error("RunContext returned nil after the hook was cancelled")
	}
	awaitProcessGone(t, childPid)
	if _, err := os.Stat(hookCwd); !os.IsNotExist(err) {
		t.Errorf("build worktree %s still exists after cancel (stat err: %v)", hookCwd, err)
	}
	if left := buildWorktreesUnder(t, repoDir); len(left) != 0 {
		t.Errorf("leftover build worktrees: %v", left)
	}
}
