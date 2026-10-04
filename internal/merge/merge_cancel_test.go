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

// cancelHookProbe is a pre-merge hook whose long-lived child runs from the hook's
// cwd (the build worktree). On SIGTERM the child records, in a file outside the
// worktree, whether that cwd still exists, then exits; the hook forwards SIGTERM
// and waits for it. That makes the probe prove the ORDER: the child must die
// while the build worktree is still there (#188).
type cancelHookProbe struct {
	pidFile, startedFile, diedFile string
}

// installCancelHook writes the hook scripts under t.TempDir() and a repo-root
// sweatfile that runs the hook as the pre-merge hook.
func installCancelHook(t *testing.T, repoDir string) cancelHookProbe {
	t.Helper()
	dir := t.TempDir()
	probe := cancelHookProbe{
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

// requireDiedInsideLiveCwd fails unless the child's TERM trap ran and saw its
// cwd still present. A missing file means the child was killed without running
// its trap (or never ran), which must not pass vacuously.
func (p cancelHookProbe) requireDiedInsideLiveCwd(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(p.diedFile)
	if err != nil {
		t.Fatalf("hook child never recorded its death (killed without running its TERM trap?): %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "cwd-present" {
		t.Errorf("hook child was told to die with its cwd %q; want cwd-present (build worktree removed before the hook tree died)", got)
	}
}

// processGone reports whether pid is dead. kill(pid, 0) succeeds for a zombie,
// so on Linux /proc is consulted: state Z, or no entry, counts as gone.
func processGone(pid int) bool {
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

func awaitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("hook child %d still alive after cancel", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The CLI entry point (sc merge) must honour its ctx the same way (#188).
func TestRunContextCancelDuringHook(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-cli-cancel")
	if err := os.WriteFile(filepath.Join(wtPath, "d.txt"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "d.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")
	probe := installCancelHook(t, repoDir)
	t.Chdir(wtPath)

	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	realStdout := os.Stdout
	os.Stdout = stdout
	t.Cleanup(func() { os.Stdout = realStdout })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = RunContext(ctx, &mockExecutor{}, "ndjson", "", false, PostMergeOptions{})
	}()

	childPid := probe.awaitHookStarted(t)
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunContext did not return within 30s of cancel")
	}
	os.Stdout = realStdout

	if runErr == nil {
		t.Error("RunContext returned nil after the hook was cancelled")
	}
	awaitProcessGone(t, childPid)
	probe.requireDiedInsideLiveCwd(t)
	entries, err := os.ReadDir(filepath.Join(repoDir, ".worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), check.BuildWorktreePrefix) {
			t.Errorf("build worktree %s left behind after cancel", e.Name())
		}
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
	probe.requireDiedInsideLiveCwd(t)
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
