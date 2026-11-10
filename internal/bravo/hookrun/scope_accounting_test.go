package hookrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/gatecost"
)

const gateScopePath = "/user.slice/gate.scope"

// fakeCgroupHost lays out a fake /sys/fs/cgroup root and /proc/self/cgroup
// file. procBody "" leaves the proc file missing; files maps names under the
// scope's cgroup dir to contents. It returns the cgroup dir too.
func fakeCgroupHost(t *testing.T, procBody string, files map[string]string) (root, proc, cgDir string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "sys-fs-cgroup")
	proc = filepath.Join(base, "proc-self-cgroup")
	cgDir = filepath.Join(root, gateScopePath)
	if err := os.MkdirAll(cgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(cgDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if procBody != "" {
		if err := os.WriteFile(proc, []byte(procBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, proc, cgDir
}

// runScopeWrapper runs the start wrapper exactly as runHookInScope puts it in
// front of the hook, minus systemd-run, and returns the hook's streams, the
// exit code, the wrapper's pid and the marker's contents.
func runScopeWrapper(t *testing.T, proc, hook string) (stdout, stderr string, code, pid int, marker string) {
	t.Helper()
	mark := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(mark, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	argv := scopeStartWrap(mark, proc, []string{"sh", "-c", hook})
	c := exec.Command(argv[0], argv[1:]...)
	var out, errOut bytes.Buffer
	c.Stdout, c.Stderr = &out, &errOut
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	err := c.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatalf("running the wrapper: %v", err)
	}
	data, rerr := os.ReadFile(mark)
	if rerr != nil {
		t.Fatal(rerr)
	}
	return out.String(), errOut.String(), code, c.Process.Pid, string(data)
}

// The wrapper still execs: the hook IS the process exec.Cmd started, so a
// cancel's SIGTERM reaches it directly, as before #424.
func TestScopeWrapperStillExecsTheHook(t *testing.T) {
	_, proc, _ := fakeCgroupHost(t, "0::"+gateScopePath+"\n", nil)
	stdout, _, _, pid, _ := runScopeWrapper(t, proc, "echo $$")
	if got, err := strconv.Atoi(strings.TrimSpace(stdout)); err != nil || got != pid {
		t.Errorf("hook pid = %q, want the started process %d: the wrapper did not exec", stdout, pid)
	}
}

func TestScopeWrapperKeepsExitCodeAndStreams(t *testing.T) {
	_, proc, _ := fakeCgroupHost(t, "0::"+gateScopePath+"\n", nil)
	for _, want := range []int{0, 1, 7} {
		t.Run(fmt.Sprintf("exit %d", want), func(t *testing.T) {
			hook := fmt.Sprintf("printf 'out line\\npartial'; printf 'err line' >&2; exit %d", want)
			stdout, stderr, code, _, _ := runScopeWrapper(t, proc, hook)
			if code != want || stdout != "out line\npartial" || stderr != "err line" {
				t.Errorf("code %d stdout %q stderr %q, want %d and the hook's own streams", code, stdout, stderr, want)
			}
		})
	}
}

func TestScopeWrapperCopiesItsCgroupIntoTheMarker(t *testing.T) {
	_, proc, _ := fakeCgroupHost(t, "12:cpu:/v1\n0::"+gateScopePath+"\n", nil)
	_, _, _, _, marker := runScopeWrapper(t, proc, "exit 0")
	if !strings.HasPrefix(marker, "started\n") {
		t.Fatalf("marker %q does not open with the start mark", marker)
	}
	if got, ok := gatecost.ScopeCgroupPath([]byte(marker)); !ok || got != gateScopePath {
		t.Errorf("ScopeCgroupPath(%q) = (%q, %v), want %s", marker, got, ok, gateScopePath)
	}
}

// An unreadable /proc/self/cgroup records nothing and still execs the hook.
func TestScopeWrapperRunsTheHookWithoutProcCgroup(t *testing.T) {
	_, proc, _ := fakeCgroupHost(t, "", nil)
	stdout, stderr, code, _, marker := runScopeWrapper(t, proc, "printf ok; exit 5")
	if marker != "started\n" {
		t.Errorf("marker = %q, want only the start mark", marker)
	}
	if code != 5 || stdout != "ok" || stderr != "" {
		t.Errorf("hook changed: code %d stdout %q stderr %q", code, stdout, stderr)
	}
}

// The start-marker guarantee survives: a vanished marker means the hook never
// runs (so it can never be re-run after a fallback).
func TestScopeWrapperDoesNotRunTheHookWithoutItsMarker(t *testing.T) {
	_, proc, _ := fakeCgroupHost(t, "0::"+gateScopePath+"\n", nil)
	ran := filepath.Join(t.TempDir(), "ran")
	argv := scopeStartWrap(filepath.Join(t.TempDir(), "gone"), proc, []string{"sh", "-c", "touch " + ran})
	if err := exec.Command(argv[0], argv[1:]...).Run(); err == nil {
		t.Error("the wrapper exited 0 without its marker")
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the hook ran although its start marker was gone")
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The sampler finds the cgroup from the marker, keeps the newest readable
// values, and keeps the last good ones once the scope's dir is gone.
func TestScopeSamplerKeepsTheLastGoodSample(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root, _, cgDir := fakeCgroupHost(t, "", nil)
	marker, err := newScopeStartMarker()
	if err != nil {
		t.Fatal(err)
	}
	defer marker.discard()
	s := &scopeSampler{marker: marker, root: root}

	s.sample() // the hook has not marked yet: nothing known
	if s.last.CPUUsec != nil || s.last.MemPeakBytes != nil {
		t.Fatalf("sampled before the cgroup was known: %+v", s.last)
	}
	writeFile(t, marker.path, "started\nproc-cgroup 0::"+gateScopePath+"\n")
	writeFile(t, filepath.Join(cgDir, "cpu.stat"), "usage_usec 10\n")
	writeFile(t, filepath.Join(cgDir, "memory.peak"), "100\n")
	s.sample()
	writeFile(t, filepath.Join(cgDir, "cpu.stat"), "usage_usec 20\n")
	writeFile(t, filepath.Join(cgDir, "memory.peak"), "300\n")
	s.sample()
	if err := os.RemoveAll(cgDir); err != nil {
		t.Fatal(err)
	}
	s.sample()

	if s.last.CPUUsec == nil || *s.last.CPUUsec != 20 || s.last.MemPeakBytes == nil || *s.last.MemPeakBytes != 300 {
		t.Errorf("last = %+v, want cpu 20 and mem 300 kept after the dir vanished", s.last)
	}
}

// fakeAccounting points the wrapper and sampler at a fake cgroup host and
// samples every 20ms.
func fakeAccounting(t *testing.T, root, proc string) {
	t.Helper()
	prevRoot, prevProc, prevEvery := scopeCgroupRoot, scopeProcSelfCgroup, scopeSampleInterval
	scopeCgroupRoot, scopeProcSelfCgroup, scopeSampleInterval = root, proc, 20*time.Millisecond
	t.Cleanup(func() { scopeCgroupRoot, scopeProcSelfCgroup, scopeSampleInterval = prevRoot, prevProc, prevEvery })
}

// End to end through a (fake) scope: the hook raises the counters, then its
// cgroup disappears before it exits, as a real scope's does once emptied. The
// report holds the last values sampled while it ran.
func TestScopedPreMergeSamplesTheScopeWhileTheHookRuns(t *testing.T) {
	ctx := fakeScope(t, workingScopePrefix)
	root, proc, cgDir := fakeCgroupHost(t, "0::"+gateScopePath+"\n", map[string]string{"cpu.stat": "usage_usec 1\n"})
	fakeAccounting(t, root, proc)
	var got []gatecost.Scope
	ctx = WithScopeAccounting(ctx, func(s gatecost.Scope) { got = append(got, s) })
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")

	hook := fmt.Sprintf("echo ran >> %[1]s; sleep 0.2; echo 'usage_usec 2' > %[2]s/cpu.stat; echo 4096 > %[2]s/memory.peak; sleep 0.3; rm -rf %[2]s; echo hook-out; exit 3",
		runLog, cgDir)
	var buf bytes.Buffer
	err := PreMergeContext(ctx, preMergeSweatfile(hook, false), dir, &buf)

	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("want the hook's own exit status 3, got: %v", err)
	}
	if got := hookRuns(t, runLog); got != 1 {
		t.Errorf("hook ran %d times, want exactly 1", got)
	}
	if buf.String() != "hook-out\n" {
		t.Errorf("hook output = %q, want only the hook's own", buf.String())
	}
	if len(got) != 1 || got[0].CPUUsec == nil || *got[0].CPUUsec != 2 || got[0].MemPeakBytes == nil || *got[0].MemPeakBytes != 4096 {
		t.Errorf("accounting = %+v, want one report of cpu 2 and mem 4096", got)
	}
}

// A fallback (the scope refused) runs the bare hook: there is no scope cgroup
// to read, so nothing is reported.
func TestUnscopedFallbackReportsNoAccounting(t *testing.T) {
	ctx := fakeScope(t, refusingScopePrefix)
	root, proc, _ := fakeCgroupHost(t, "0::"+gateScopePath+"\n", map[string]string{"cpu.stat": "usage_usec 1\n"})
	fakeAccounting(t, root, proc)
	called := false
	ctx = WithScopeAccounting(ctx, func(gatecost.Scope) { called = true })
	var buf bytes.Buffer
	if err := PreMergeContext(ctx, preMergeSweatfile("true", false), t.TempDir(), &buf); err != nil {
		t.Fatalf("fallback hook failed: %v\n%s", err, buf.String())
	}
	if called {
		t.Error("accounting reported for an unscoped hook")
	}
}

// Sampling does not change cancellation: the SIGTERM still lands on the hook.
func TestCancelOfSampledScopedHookReachesTheHook(t *testing.T) {
	ctx, cancel := context.WithCancel(fakeScope(t, workingScopePrefix))
	defer cancel()
	root, proc, _ := fakeCgroupHost(t, "0::"+gateScopePath+"\n", nil)
	fakeAccounting(t, root, proc)
	ctx = WithScopeAccounting(ctx, func(gatecost.Scope) {})
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	gotTerm := filepath.Join(dir, "got-term")

	script := fmt.Sprintf("trap 'touch %s; exit 0' TERM; touch %s; while :; do sleep 0.1; done", gotTerm, started)
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- PreMergeContext(ctx, preMergeSweatfile(script, false), dir, &buf) }()

	waitForFile(t, started)
	begun := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("cancelled hook did not return within 30s")
	}
	if _, err := os.Stat(gotTerm); err != nil {
		t.Errorf("the hook never saw the SIGTERM: %v\n%s", err, buf.String())
	}
	if elapsed := time.Since(begun); elapsed >= cancelGrace {
		t.Errorf("cancel took %s, reaching the SIGKILL escalation (%s)", elapsed, cancelGrace)
	}
}
