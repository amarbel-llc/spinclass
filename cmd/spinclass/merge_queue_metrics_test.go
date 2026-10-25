package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/alfa/job"
)

// queueStatsdSink points STATSD_HOST/STATSD_PORT at a loopback UDP listener and
// returns a func that drains every datagram received so far.
func queueStatsdSink(t *testing.T) func() []string {
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

// metricNames strips each datagram's ":<value>|<type>" tail.
func metricNames(datagrams []string) []string {
	names := make([]string, 0, len(datagrams))
	for _, d := range datagrams {
		name, _, _ := strings.Cut(d, ":")
		names = append(names, name)
	}
	return names
}

func countName(names []string, want string) int {
	n := 0
	for _, name := range names {
		if name == want {
			n++
		}
	}
	return n
}

func testStack(enqueuedAt time.Time) stackMetrics {
	return stackMetrics{client: statsd.Resolve("", false), repo: "repo", enqueuedAt: enqueuedAt}
}

// TestProcessMergeQueueTimesStackWaitOnDequeue (#365): a dequeued entry emits
// its enqueue-to-dequeue wait as merge.<repo>.stack_wait.ok.
func TestProcessMergeQueueTimesStackWaitOnDequeue(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	drain := queueStatsdSink(t)
	wt := t.TempDir()
	mergeQueueMu.Lock()
	mergeQueue[wt] = []queuedMerge{{
		run:   func(_ context.Context, _ io.Writer) (string, bool) { return "✓ queued merge", false },
		stack: testStack(time.Now().Add(-1500 * time.Millisecond)),
	}}
	mergeQueueMu.Unlock()
	t.Cleanup(func() {
		mergeQueueMu.Lock()
		delete(mergeQueue, wt)
		mergeQueueMu.Unlock()
	})

	processMergeQueue(wt, job.KindMerge, job.StatusSucceeded, "prior-merge")
	<-job.WaitDone(wt)

	got := drain()
	want := statsd.Prefix + "merge.repo.stack_wait.ok"
	var value string
	for _, d := range got {
		if name, rest, _ := strings.Cut(d, ":"); name == want {
			value = strings.TrimSuffix(rest, "|ms")
		}
	}
	if value == "" {
		t.Fatalf("no %s timer in %q", want, got)
	}
	if ms, err := strconv.Atoi(value); err != nil || ms < 1500 {
		t.Errorf("stack_wait = %q ms, want >= 1500 (time since enqueue)", value)
	}
}

// TestProcessMergeQueueTimesStackWaitOnDrain (#365): every drained entry emits
// merge.<repo>.stack_wait.drained, distinct from a dequeued-and-run entry.
func TestProcessMergeQueueTimesStackWaitOnDrain(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	drain := queueStatsdSink(t)
	wt := t.TempDir()
	mergeQueueMu.Lock()
	mergeQueue[wt] = []queuedMerge{{stack: testStack(time.Now())}, {stack: testStack(time.Now())}}
	mergeQueueMu.Unlock()
	t.Cleanup(func() {
		mergeQueueMu.Lock()
		delete(mergeQueue, wt)
		mergeQueueMu.Unlock()
	})

	processMergeQueue(wt, job.KindMerge, job.StatusFailed, "prior-merge")

	names := metricNames(drain())
	if n := countName(names, statsd.Prefix+"merge.repo.stack_wait.drained"); n != 2 {
		t.Errorf("stack_wait.drained timers = %d, want 2: %q", n, names)
	}
	if n := countName(names, statsd.Prefix+"merge.repo.stack_wait.ok"); n != 0 {
		t.Errorf("a drained entry must not emit stack_wait.ok: %q", names)
	}
}

// TestMergeAsyncEnqueueGaugesStackDepth (#365): an enqueue emits its queue
// position as the merge.<repo>.stack_depth gauge.
func TestMergeAsyncEnqueueGaugesStackDepth(t *testing.T) {
	cwd, repoPath, _ := gatedWorktreeFixture(t, gateSweat)
	drain := queueStatsdSink(t)
	release := startBlockingJob(t, cwd)
	t.Cleanup(func() {
		mergeQueueMu.Lock()
		delete(mergeQueue, cwd)
		mergeQueueMu.Unlock()
		close(release)
		<-job.WaitDone(cwd)
	})

	res, err := handleMergeThisSessionAsync(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || res.IsErr {
		t.Fatalf("expected enqueue success, got err=%v result=%+v", err, res)
	}

	want := statsd.Prefix + "merge." + statsd.Segment(filepath.Base(repoPath)) + ".stack_depth:1|g"
	got := drain()
	for _, d := range got {
		if d == want {
			return
		}
	}
	t.Errorf("no %q datagram in %q", want, got)
}
