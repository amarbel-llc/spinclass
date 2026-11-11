package hostqueue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLedger(t *testing.T) Ledger {
	t.Helper()
	dir := t.TempDir()
	return Ledger{Dir: filepath.Join(dir, "run"), HoldPath: filepath.Join(dir, "state", "hold")}
}

func TestLedgerAdmitListRelease(t *testing.T) {
	l := testLedger(t)
	unlock, err := l.Lock()
	if err != nil {
		t.Fatal(err)
	}
	a := Marker{Session: "r/a", Repo: "r", Kind: "merge", AdmittedAt: t0, PID: 1}
	b := Marker{Session: "r/b", Repo: "r", Kind: "check", AdmittedAt: t0.Add(time.Minute), PID: 2}
	releaseB, err := l.Admit(b)
	if err != nil {
		t.Fatal(err)
	}
	releaseA, err := l.Admit(a)
	if err != nil {
		t.Fatal(err)
	}
	unlock()

	live, err := l.Live(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || live[0].Session != "r/a" || live[1].Session != "r/b" || live[1].Kind != "check" {
		t.Fatalf("live = %+v, want a then b", live)
	}
	releaseA()
	releaseA() // idempotent
	live, _ = l.Live(true)
	if len(live) != 1 || live[0].Session != "r/b" {
		t.Fatalf("after releasing a: %+v", live)
	}
	releaseB()
	if live, _ := l.Live(true); len(live) != 0 {
		t.Fatalf("after releasing b: %+v", live)
	}
}

// A marker whose holder is gone (its file is not locked) is dead: a reaping
// reader removes it, a read-only reader skips it and leaves it.
func TestLedgerDeadMarkerIsReapedOnlyWhenReaping(t *testing.T) {
	l := testLedger(t)
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(l.Dir, "gate-dead.json")
	if err := os.WriteFile(dead, []byte(`{"session":"r/dead"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if live, _ := l.Live(false); len(live) != 0 {
		t.Fatalf("read-only view counted a dead marker: %+v", live)
	}
	if _, err := os.Stat(dead); err != nil {
		t.Fatalf("read-only view removed the dead marker: %v", err)
	}
	if live, _ := l.Live(true); len(live) != 0 {
		t.Fatalf("reaping view counted a dead marker: %+v", live)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("reaping view left the dead marker: %v", err)
	}
}

// A waiter is listed apart from the gates, its reason can be updated, and it
// takes no slot.
func TestLedgerWaiter(t *testing.T) {
	l := testLedger(t)
	update, leave, err := l.Wait(Marker{Session: "r/w", WaitingSince: t0, Reason: "load"})
	if err != nil {
		t.Fatal(err)
	}
	if live, _ := l.Live(true); len(live) != 0 {
		t.Fatalf("a waiter counted as a gate: %+v", live)
	}
	update("memory")
	v, err := l.View(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Waiting) != 1 || v.Waiting[0].Session != "r/w" || v.Waiting[0].Reason != "memory" {
		t.Fatalf("waiting = %+v", v.Waiting)
	}
	leave()
	leave()
	if w, _ := l.Waiting(true); len(w) != 0 {
		t.Fatalf("after leave: %+v", w)
	}
}

// A reader racing a waiter's reason updates always sees the waiter, whole: an
// update never shows a half-written marker, and a reaping reader never takes a
// replaced marker for a dead one.
func TestLedgerWaiterReadDuringUpdates(t *testing.T) {
	l := testLedger(t)
	update, leave, err := l.Wait(Marker{Session: "r/w", WaitingSince: t0, Reason: "load"})
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 2000 {
			update([]string{"load", "memory"}[i%2])
		}
	}()
	for reads := 0; ; reads++ {
		select {
		case <-done:
			if reads == 0 {
				t.Fatal("no read overlapped the updates")
			}
			return
		default:
		}
		w, err := l.Waiting(true)
		if err != nil {
			t.Fatal(err)
		}
		if len(w) != 1 || w[0].Session != "r/w" {
			t.Fatalf("read %d during updates: %+v, want the one waiter", reads, w)
		}
	}
}

func TestLedgerLiveOnAMissingDirIsEmpty(t *testing.T) {
	if live, err := testLedger(t).Live(true); err != nil || len(live) != 0 {
		t.Fatalf("= (%v, %v), want empty", live, err)
	}
}

func TestLedgerHold(t *testing.T) {
	l := testLedger(t)
	if held, _ := l.Hold(); held {
		t.Fatal("held before any hold")
	}
	if err := l.SetHold("  relock in progress \n"); err != nil {
		t.Fatal(err)
	}
	held, reason := l.Hold()
	if !held || reason != "relock in progress" {
		t.Fatalf("= (%v, %q)", held, reason)
	}
	v, err := l.View(true)
	if err != nil || !v.Held || v.HoldReason != "relock in progress" {
		t.Fatalf("view = (%+v, %v)", v, err)
	}
	if err := l.ClearHold(); err != nil {
		t.Fatal(err)
	}
	if err := l.ClearHold(); err != nil {
		t.Fatalf("clearing an absent hold: %v", err)
	}
	if held, _ := l.Hold(); held {
		t.Fatal("still held after clearing")
	}
}

func TestDefaultLedgerLocations(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/7")
	t.Setenv("XDG_STATE_HOME", "/s")
	l, err := DefaultLedger()
	if err != nil {
		t.Fatal(err)
	}
	if l.Dir != "/run/user/7/spinclass/host-queue" || l.HoldPath != "/s/spinclass/host-queue/hold" {
		t.Errorf("= %+v", l)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := DefaultLedger(); err == nil || !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") {
		t.Errorf("err = %v, want one naming XDG_RUNTIME_DIR", err)
	}
}
