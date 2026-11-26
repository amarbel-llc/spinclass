package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
)

// livenessClock makes bridgeNow a clock the test moves and restores it.
type livenessClock struct {
	mu  sync.Mutex
	now time.Time
}

func fakeBridgeClock(t *testing.T, start time.Time) *livenessClock {
	t.Helper()
	c := &livenessClock{now: start}
	old := bridgeNow
	bridgeNow = func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.now
	}
	t.Cleanup(func() { bridgeNow = old })
	return c
}

func (c *livenessClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeServeProc makes bridgeProcRoot a root holding one process (pid, ticks).
func fakeServeProc(t *testing.T, pid int, ticks uint64) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, strconv.Itoa(pid)), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (spinclass) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0", pid, ticks)
	if err := os.WriteFile(filepath.Join(root, strconv.Itoa(pid), "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	old := bridgeProcRoot
	bridgeProcRoot = root
	t.Cleanup(func() { bridgeProcRoot = old })
}

func TestTheStateFileNamesTheServeAndCarriesAHeartbeat(t *testing.T) {
	clock := fakeBridgeClock(t, time.Date(2026, 11, 26, 5, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	w := &bridgeStateWriter{dir: dir, principal: "p", key: "r/b", pid: 4242, ticks: 9001}
	w.mark(true, "")
	got, ok := crossinglog.ReadState(dir, "p")
	if !ok || got.PID != 4242 || got.StartTicks != 9001 || !got.HeartbeatAt.Equal(clock.now) {
		t.Fatalf("state = %+v, %v; want pid 4242, ticks 9001, heartbeat now", got, ok)
	}
}

func TestBeatRewritesOnlyWhenAHeartbeatIntervalHasPassed(t *testing.T) {
	clock := fakeBridgeClock(t, time.Date(2026, 11, 26, 5, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	w := &bridgeStateWriter{dir: dir, principal: "p", key: "r/b", pid: 4242, ticks: 9001}
	first := clock.now
	w.mark(true, "")

	clock.advance(10 * time.Second)
	w.beat()
	if got, _ := crossinglog.ReadState(dir, "p"); !got.HeartbeatAt.Equal(first) {
		t.Errorf("a beat 10 s after the write rewrote the file (heartbeat %v)", got.HeartbeatAt)
	}

	clock.advance(crossinglog.HeartbeatEvery)
	w.beat()
	got, _ := crossinglog.ReadState(dir, "p")
	if !got.HeartbeatAt.Equal(clock.now) || !got.Listening || !got.Since.Equal(first) {
		t.Errorf("a beat after the interval: heartbeat %v since %v listening %v; want heartbeat now, since unchanged", got.HeartbeatAt, got.Since, got.Listening)
	}
}

func TestBeatNeverRefreshesADownStateNorRecreatesARemovedFile(t *testing.T) {
	clock := fakeBridgeClock(t, time.Date(2026, 11, 26, 5, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	w := &bridgeStateWriter{dir: dir, principal: "p", key: "r/b", pid: 4242, ticks: 9001}
	down := clock.now
	w.mark(false, "no exact peer check")
	clock.advance(10 * crossinglog.HeartbeatEvery)
	w.beat()
	if got, _ := crossinglog.ReadState(dir, "p"); !got.HeartbeatAt.Equal(down) {
		t.Errorf("a down state was refreshed: %v", got.HeartbeatAt)
	}
	w.remove()
	w.beat()
	if _, err := os.Stat(crossinglog.StatePath(dir, "p")); err == nil {
		t.Error("a beat after remove brought the file back")
	}
}

func TestAKilledServeReadsAsDownAndALiveOneAsListening(t *testing.T) {
	clock := fakeBridgeClock(t, time.Date(2026, 11, 26, 5, 0, 0, 0, time.UTC))
	stateHome := t.TempDir()
	dir := crossinglog.StateDir(stateHome)
	if err := crossinglog.WriteState(dir, "p", crossinglog.State{
		Session: "r/b", Listening: true, Since: clock.now, PID: 4242, StartTicks: 9001, HeartbeatAt: clock.now,
	}); err != nil {
		t.Fatal(err)
	}

	fakeServeProc(t, 4242, 9001)
	if got := bridgeStateNote(stateHome, "p", true); got != "bridge listening" {
		t.Errorf("a live serve: note = %q", got)
	}
	if got := bridgeStateNote(stateHome, "p", false); got != "" {
		t.Errorf("sc list speaks of a live bridge: %q", got)
	}

	fakeServeProc(t, 77, 1) // the serve's pid is not there: it was killed
	for _, withListening := range []bool{true, false} {
		if got := bridgeStateNote(stateHome, "p", withListening); got != crossinglog.GoneNote {
			t.Errorf("a killed serve (withListening=%v): note = %q, want %q", withListening, got, crossinglog.GoneNote)
		}
	}

	fakeServeProc(t, 4242, 9002) // the pid was reused
	if got := bridgeStateNote(stateHome, "p", false); got != crossinglog.GoneNote {
		t.Errorf("a reused pid: note = %q", got)
	}

	fakeServeProc(t, 4242, 9001)
	clock.advance(crossinglog.HeartbeatStale + time.Minute) // alive but silent: hung
	if got := bridgeStateNote(stateHome, "p", false); got != crossinglog.GoneNote {
		t.Errorf("a stale heartbeat: note = %q", got)
	}
}

func TestTheHeartbeatGoroutineBeatsOnItsTickerAndStopsBeforeTheFileGoes(t *testing.T) {
	clock := fakeBridgeClock(t, time.Date(2026, 11, 26, 5, 0, 0, 0, time.UTC))
	tick := make(chan time.Time)
	oldTicker := bridgeHeartbeatTicker
	bridgeHeartbeatTicker = func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} }
	t.Cleanup(func() { bridgeHeartbeatTicker = oldTicker })

	dir := t.TempDir()
	w := &bridgeStateWriter{dir: dir, principal: "p", key: "r/b", pid: 4242, ticks: 9001}
	w.mark(true, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startBridgeHeartbeat(ctx, w)

	clock.advance(2 * crossinglog.HeartbeatEvery)
	tick <- clock.now // unbuffered: returns once the goroutine took it
	stop()            // waits for the goroutine, so the beat is done
	if got, _ := crossinglog.ReadState(dir, "p"); !got.HeartbeatAt.Equal(clock.now) {
		t.Errorf("a tick did not refresh the heartbeat: %v, want %v", got.HeartbeatAt, clock.now)
	}
	stop() // twice is fine
	w.remove()
	clock.advance(2 * crossinglog.HeartbeatEvery)
	select {
	case tick <- clock.now:
		t.Error("the goroutine still takes ticks after stop")
	default:
	}
	if _, err := os.Stat(crossinglog.StatePath(dir, "p")); err == nil {
		t.Error("the file is back after stop and remove")
	}
}

func TestANewServeStateWriterNamesItsOwnProcess(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	w := newBridgeStateWriter("r/b")
	if w == nil {
		t.Fatal("no writer")
	}
	if w.pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", w.pid, os.Getpid())
	}
	if want, ok := hungstatus.StartTicks("/proc", os.Getpid()); ok && w.ticks != want {
		t.Errorf("ticks = %d, want %d", w.ticks, want)
	}
}
