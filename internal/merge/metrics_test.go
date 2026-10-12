package merge

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// statsdListener opens a loopback UDP listener and returns its port plus a func
// that drains every datagram received so far, sorted. It touches no environment.
func statsdListener(t *testing.T) (port int, drain func() []string) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn.LocalAddr().(*net.UDPAddr).Port, func() []string {
		var got []string
		buf := make([]byte, 512)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			got = append(got, string(buf[:n]))
		}
		sort.Strings(got)
		return got
	}
}

// statsdSink points STATSD_* at a loopback UDP listener and returns its drain.
func statsdSink(t *testing.T) func() []string {
	t.Helper()
	port, drain := statsdListener(t)
	t.Setenv("STATSD_HOST", "127.0.0.1")
	t.Setenv("STATSD_PORT", strconv.Itoa(port))
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	return drain
}

// clearStatsdEnv removes the STATSD_* opt-in, so only a sweatfile can enable
// emission.
func clearStatsdEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	for _, k := range []string{"STATSD_HOST", "STATSD_PORT"} {
		t.Setenv(k, "x") // registers restore
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// counters keeps only the |c datagrams: the landing-ratio counters, which
// these tests assert exactly. Timers are asserted by name (timerNames).
func counters(got []string) []string {
	var out []string
	for _, d := range got {
		if strings.HasSuffix(d, "|c") {
			out = append(out, d)
		}
	}
	return out
}

// timerNames keeps only the |ms datagrams and strips ":<ms>|ms", leaving the
// metric names (sorted, as drain sorts datagrams).
func timerNames(t *testing.T, got []string) []string {
	t.Helper()
	var out []string
	for _, d := range got {
		if !strings.HasSuffix(d, "|ms") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimSuffix(d, "|ms"), ":")
		if !ok {
			t.Fatalf("malformed timer datagram %q", d)
		}
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			t.Fatalf("timer %q: value %q is not whole milliseconds", d, value)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func assertMetrics(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("metrics = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("metrics = %q, want %q", got, want)
		}
	}
}

// assertTimersInclude fails unless every name in want was emitted.
func assertTimersInclude(t *testing.T, got []string, want ...string) {
	t.Helper()
	have := map[string]bool{}
	for _, n := range got {
		have[n] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("timer %q not emitted; got %q", w, got)
		}
	}
}

// A clean remote landing counts landed + local_advance.ok — the pair whose
// equality (ok + skip == landed) FDR 0029's promotion criterion checks.
func TestMetricsCleanLanding(t *testing.T) {
	drain := statsdSink(t)
	_, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-m-ok")
	if err := os.WriteFile(filepath.Join(wtPath, "m.txt"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "m.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")

	if recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-m-ok", "main", true, false); err != nil {
		t.Fatalf("Resolved: %v\n%+v", err, recs)
	}
	assertMetrics(t, counters(drain()),
		"spinclass.merge.landed:1|c",
		"spinclass.merge.local_advance.ok:1|c",
	)
}

// Every phase of a clean remote landing emits exactly one ok timer under
// merge.<repo-segment>., and nothing fails.
func TestMetricsCleanLandingTimers(t *testing.T) {
	drain := statsdSink(t)
	_, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-m-timers")
	if err := os.WriteFile(filepath.Join(wtPath, "t.txt"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "t.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")

	if recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-m-timers", "main", true, false); err != nil {
		t.Fatalf("Resolved: %v\n%+v", err, recs)
	}
	got := timerNames(t, drain())
	prefix := "spinclass.merge." + filepath.Base(repoDir) + "."
	var want []string
	for _, phase := range []string{
		"fetch", "rebase", "prepare",
		"queue_wait", "landing_fetch", "policy", "land", "finish",
		"total",
	} {
		want = append(want, prefix+phase+".ok")
	}
	assertTimersInclude(t, got, want...)
	for _, name := range got {
		// No hook is configured here, so nothing ran and nothing is timed.
		if strings.Contains(name, ".pre_merge_hook.") {
			t.Errorf("pre_merge_hook timer %q emitted with no hook configured", name)
		}
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".ok") {
			t.Errorf("unexpected timer %q on a clean landing", name)
		}
	}
}

// A dirty overlapping root counts landed + skip + skip_reason.dirty_overlap.
func TestMetricsDirtyRootSkip(t *testing.T) {
	drain := statsdSink(t)
	_, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-m-dirty")
	if err := os.WriteFile(filepath.Join(wtPath, "file.txt"), []byte("from session"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "commit", "-am", "session edits file.txt")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("operator edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	if recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-m-dirty", "main", true, false); err != nil {
		t.Fatalf("Resolved: %v\n%+v", err, recs)
	}
	assertMetrics(t, counters(drain()),
		"spinclass.merge.landed:1|c",
		"spinclass.merge.local_advance.skip:1|c",
		"spinclass.merge.local_advance.skip_reason.dirty_overlap:1|c",
	)
}

// A refused push counts push_refused and nothing else — no landing happened,
// so no advance was attempted — and its land/finish/total timers read failed.
func TestMetricsPushRefused(t *testing.T) {
	drain := statsdSink(t)
	_, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-m-refused")
	if err := os.WriteFile(filepath.Join(wtPath, "r.txt"), []byte("r"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "r.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")
	runGit(t, repoDir, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "nonexistent.git"))

	if _, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-m-refused", "main", true, false); err == nil {
		t.Fatal("expected the dead push URL to fail the merge")
	}
	got := drain()
	assertMetrics(t, counters(got), "spinclass.merge.push_refused:1|c")

	timers := timerNames(t, got)
	prefix := "spinclass.merge." + filepath.Base(repoDir) + "."
	assertTimersInclude(t, timers,
		prefix+"prepare.ok", prefix+"queue_wait.ok",
		prefix+"land.failed", prefix+"finish.failed", prefix+"total.failed",
	)
	for _, name := range timers {
		if strings.HasSuffix(name, ".failed") && !strings.HasPrefix(name, prefix+"land.") &&
			!strings.HasPrefix(name, prefix+"finish.") && !strings.HasPrefix(name, prefix+"total.") {
			t.Errorf("unexpected failed timer %q", name)
		}
	}
}

// A local-only (self) merge is outside the promotion ratio: it emits no counters.
func TestMetricsLocalOnlyEmitsNoCounters(t *testing.T) {
	drain := statsdSink(t)
	_, repoDir := setupSyncRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-m-self")
	if err := os.WriteFile(filepath.Join(wtPath, "s.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "s.txt")
	runGit(t, wtPath, "commit", "-m", "session commit")

	if recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-m-self", "main", false, false); err != nil {
		t.Fatalf("Resolved: %v\n%+v", err, recs)
	}
	got := drain()
	assertMetrics(t, counters(got))

	// Nothing is fetched for a self target, so those two timers are skipped.
	timers := timerNames(t, got)
	for _, name := range timers {
		if strings.Contains(name, ".fetch.") || strings.Contains(name, ".landing_fetch.") {
			t.Errorf("self target emitted a fetch timer: %q", name)
		}
	}
	assertTimersInclude(t, timers,
		"spinclass.merge."+filepath.Base(repoDir)+".land.ok",
		"spinclass.merge."+filepath.Base(repoDir)+".total.ok",
	)
}

// A named post-merge target splits into command and verify timers; a failing
// verify reads command.ok + verify.failed while the merge (and total) stay ok.
func TestMetricsNamedPostMergeTargetVerifyFailed(t *testing.T) {
	drain := statsdSink(t)
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "Host.A"
command = "echo triggered"
verify = "exit 1"
`)
	if _, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil); err != nil {
		t.Fatalf("a verify failure must not fail the merge: %v", err)
	}
	timers := timerNames(t, drain())
	prefix := "spinclass.merge." + filepath.Base(repoDir) + "."
	assertTimersInclude(t, timers,
		prefix+"post_merge.host_a.command.ok",
		prefix+"post_merge.host_a.verify.failed",
		prefix+"total.ok",
		prefix+"finish.ok",
	)
}

// The legacy [hooks].post-merge string is timed as post_merge_hook.
func TestMetricsLegacyPostMergeHook(t *testing.T) {
	drain := statsdSink(t)
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, "[hooks]\npost-merge = \"exit 1\"\n")
	if _, err := runFinish(t, repoDir, wtPath, "feature", false); err != nil {
		t.Fatalf("a failing post-merge hook must not fail the merge: %v", err)
	}
	prefix := "spinclass.merge." + filepath.Base(repoDir) + "."
	assertTimersInclude(t, timerNames(t, drain()), prefix+"post_merge_hook.failed", prefix+"total.ok")
}

// A configured pre-merge hook is timed; a failing one reads failed and takes
// finish and total with it.
func TestMetricsPreMergeHookTimed(t *testing.T) {
	drain := statsdSink(t)
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, "[hooks]\npre-merge = \"exit 1\"\n")
	if _, err := runFinish(t, repoDir, wtPath, "feature", false); err == nil {
		t.Fatal("expected the failing pre-merge hook to fail the merge")
	}
	prefix := "spinclass.merge." + filepath.Base(repoDir) + "."
	assertTimersInclude(t, timerNames(t, drain()),
		prefix+"pre_merge_hook.failed", prefix+"finish.failed", prefix+"total.failed")
}

// [metrics] disable-statsd = true silences everything, even with the env
// opt-in pointing at a live listener.
func TestMetricsSweatfileDisableEmitsNothing(t *testing.T) {
	drain := statsdSink(t)
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, "[metrics]\ndisable-statsd = true\n")
	if _, err := runFinish(t, repoDir, wtPath, "feature", false); err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	assertMetrics(t, drain())
}

// [metrics] statsd-address enables emission with STATSD_* unset.
func TestMetricsSweatfileAddressWithoutEnv(t *testing.T) {
	port, drain := statsdListener(t)
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	clearStatsdEnv(t) // after setupRepo, which sets HOME but not STATSD_*
	writeRepoSweatfile(t, repoDir, "[metrics]\nstatsd-address = \"127.0.0.1:"+strconv.Itoa(port)+"\"\n")
	if _, err := runFinish(t, repoDir, wtPath, "feature", false); err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	prefix := "spinclass.merge." + filepath.Base(repoDir) + "."
	assertTimersInclude(t, timerNames(t, drain()), prefix+"total.ok", prefix+"land.ok")
}
