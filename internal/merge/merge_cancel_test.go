package merge

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
	"code.linenisgreat.com/spinclass/internal/check"
)

// cancelHookProbe is a pre-merge hook that records its child's pid, then waits
// on the child until SIGTERM, which it forwards.
type cancelHookProbe struct {
	pidFile, startedFile string
}

// installCancelHook writes the hook script under t.TempDir() and a repo-root
// sweatfile that runs it as the pre-merge hook.
func installCancelHook(t *testing.T, repoDir string) cancelHookProbe {
	t.Helper()
	dir := t.TempDir()
	probe := cancelHookProbe{
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
	writeRepoSweatfile(t, repoDir, "[hooks]\npre-merge = \"sh "+scriptPath+"\"\n")
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	return probe
}

// awaitHookStarted blocks until the hook is running, registers a SIGKILL
// cleanup for its child, and returns the child's pid.
func (p cancelHookProbe) awaitHookStarted(t *testing.T) int {
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
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parsing child pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
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

// A cancel that arrives while the pre-merge hook runs must reap the hook, drop
// the transient worktrees, and land nothing on the default branch (#188).
func TestResolvedContext_CancelDuringHookLandsNothing(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-cancel")
	if err := os.WriteFile(filepath.Join(wtPath, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "c.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")
	probe := installCancelHook(t, repoDir)
	mainBefore := runGit(t, repoDir, "rev-parse", "main")

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		_, runErr = ResolvedContext(ctx, &mockExecutor{}, rep, ts, repoDir, wtPath, "feature-cancel", "main", false, true, nil, PostMergeOptions{})
	}()

	childPid := probe.awaitHookStarted(t)
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("ResolvedContext did not return within 30s of cancel")
	}
	ts.Finish()

	if runErr == nil {
		t.Error("ResolvedContext returned nil after the hook was cancelled")
	}
	awaitProcessGone(t, childPid)
	entries, err := os.ReadDir(filepath.Join(repoDir, ".worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), check.BuildWorktreePrefix) || strings.HasPrefix(e.Name(), LandWorktreePrefix) {
			t.Errorf("transient worktree %s left behind after cancel", e.Name())
		}
	}
	if got := runGit(t, repoDir, "rev-parse", "main"); got != mainBefore {
		t.Errorf("main moved from %s to %s on a cancelled merge", mainBefore, got)
	}
}
