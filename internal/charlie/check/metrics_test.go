package check

import (
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/statsd"
)

// statsdSink points STATSD_HOST/STATSD_PORT at a loopback UDP listener and
// returns a func that drains every datagram received so far. It overrides the
// SPINCLASS_DISABLE_STATSD=1 the hermetic TestMain env sets.
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
				return got
			}
			got = append(got, string(buf[:n]))
		}
	}
}

// checkTimerName is the full metric name `sc check` emits for repoDir's hook.
func checkTimerName(repoDir, outcome string) string {
	return statsd.Prefix + "check." + statsd.Segment(filepath.Base(repoDir)) + ".pre_merge_hook." + outcome
}

// TestCheckTimesPreMergeHookByOutcome pins the standalone `sc check` timer
// (spinclass#363): one |ms datagram named by the hook's outcome.
func TestCheckTimesPreMergeHookByOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, hook, outcome string
		wantErr             bool
	}{
		{"passing hook", "true", statsd.OutcomeOK, false},
		{"failing hook", "false", statsd.OutcomeFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repoDir, wtPath := setupRepoWithWorktree(t, "feature-timer")
			writeSweatfile(t, wtPath, "[hooks]\npre-merge = \""+tc.hook+"\"\n")
			drain := statsdSink(t)

			if _, _, err := runCheck(t, wtPath); (err != nil) != tc.wantErr {
				t.Fatalf("Run() error = %v, wantErr %v", err, tc.wantErr)
			}

			got := drain()
			if len(got) != 1 {
				t.Fatalf("datagrams = %q, want exactly one timer", got)
			}
			name, ms, ok := strings.Cut(strings.TrimSuffix(got[0], "|ms"), ":")
			if !strings.HasSuffix(got[0], "|ms") || !ok {
				t.Fatalf("datagram %q is not a timer", got[0])
			}
			if want := checkTimerName(repoDir, tc.outcome); name != want {
				t.Errorf("timer name = %q, want %q", name, want)
			}
			if _, err := strconv.ParseInt(ms, 10, 64); err != nil {
				t.Errorf("timer value %q is not whole milliseconds", ms)
			}
		})
	}
}

// TestCheckSendsNoTimerWithoutHook: nothing ran, so nothing is timed.
func TestCheckSendsNoTimerWithoutHook(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-no-timer")
	drain := statsdSink(t)

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if got := drain(); len(got) != 0 {
		t.Errorf("datagrams = %q, want none when no hook is configured", got)
	}
}
