package merge

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// statsdSink points STATSD_* at a loopback UDP listener and returns a func that
// drains every datagram received so far, sorted.
func statsdSink(t *testing.T) func() []string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("STATSD_HOST", "127.0.0.1")
	t.Setenv("STATSD_PORT", strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port))
	t.Setenv("SPINCLASS_DISABLE_STATSD", "")
	return func() []string {
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
	assertMetrics(t, drain(),
		"spinclass.merge.landed:1|c",
		"spinclass.merge.local_advance.ok:1|c",
	)
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
	assertMetrics(t, drain(),
		"spinclass.merge.landed:1|c",
		"spinclass.merge.local_advance.skip:1|c",
		"spinclass.merge.local_advance.skip_reason.dirty_overlap:1|c",
	)
}

// A refused push counts push_refused and nothing else — no landing happened,
// so no advance was attempted.
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
	assertMetrics(t, drain(), "spinclass.merge.push_refused:1|c")
}

// A local-only (self) merge is outside the promotion ratio: it emits nothing.
func TestMetricsLocalOnlyEmitsNothing(t *testing.T) {
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
	assertMetrics(t, drain())
}
