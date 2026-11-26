package crossinglog

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeProcRoot writes <root>/<pid>/stat with starttime (field 22) = ticks.
func fakeProcRoot(t *testing.T, pid int, ticks uint64) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (spinclass) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0", pid, ticks)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func listeningState(now time.Time) State {
	return State{Session: "r/b", Listening: true, Since: now, UpdatedAt: now, PID: 4242, StartTicks: 9001, HeartbeatAt: now}
}

func TestAListeningStateWhoseWriterLivesSaysNothing(t *testing.T) {
	now := time.Now()
	root := fakeProcRoot(t, 4242, 9001)
	s := listeningState(now)
	if s.WriterGone(root, now) || s.NoteAt(root, now) != "" {
		t.Errorf("a live writer with a fresh heartbeat reads as gone: %q", s.NoteAt(root, now))
	}
}

func TestAListeningStateWhoseWriterIsGoneReadsAsDown(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		root string
		s    State
	}{
		"the pid is gone":                       {fakeProcRoot(t, 7, 9001), listeningState(now)},
		"the pid was reused by another process": {fakeProcRoot(t, 4242, 9002), listeningState(now)},
		"the heartbeat is stale": {fakeProcRoot(t, 4242, 9001), func() State {
			s := listeningState(now)
			s.HeartbeatAt = now.Add(-HeartbeatStale - time.Second)
			return s
		}()},
		"the heartbeat was never set": {fakeProcRoot(t, 4242, 9001), func() State {
			s := listeningState(now)
			s.HeartbeatAt = time.Time{}
			return s
		}()},
	}
	for name, c := range cases {
		if !c.s.WriterGone(c.root, now) {
			t.Errorf("%s: WriterGone = false", name)
		}
		if got := c.s.NoteAt(c.root, now); got != GoneNote {
			t.Errorf("%s: NoteAt = %q, want %q", name, got, GoneNote)
		}
	}
}

func TestADownStateAndAnOlderFileStandAsWritten(t *testing.T) {
	now := time.Now()
	root := fakeProcRoot(t, 7, 1) // the writer's pid is not there
	down := State{Session: "r/b", Listening: false, Reason: "no exact peer check", PID: 4242, StartTicks: 9001, HeartbeatAt: now}
	if got := down.NoteAt(root, now); got != "bridge DOWN: no exact peer check" {
		t.Errorf("a down state is overwritten by liveness: %q", got)
	}
	old := State{Session: "r/b", Listening: true} // written before the pid fields existed
	if old.WriterGone(root, now) || old.NoteAt(root, now) != "" {
		t.Errorf("a file with no pid reads as gone")
	}
}

func TestTheLivenessFieldsRoundTripThroughTheFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	if err := WriteState(dir, "p", listeningState(now)); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadState(dir, "p")
	if !ok || got.PID != 4242 || got.StartTicks != 9001 || !got.HeartbeatAt.Equal(now) {
		t.Errorf("ReadState = %+v, %v", got, ok)
	}
}
