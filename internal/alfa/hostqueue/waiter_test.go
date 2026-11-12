package hostqueue

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHoldAndReleaseACommandedWaiter(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	base := w.Marker().WaitingSince

	hold := cmdFor(w, "")
	hold.Verb, hold.At = cmdVerbHold, base.Add(2*time.Minute)
	if _, err := l.SendCommand(hold); err != nil {
		t.Fatal(err)
	}
	if applied := w.ApplyCommands(); len(applied) != 1 {
		t.Fatalf("applied = %+v", applied)
	}
	if m := w.Marker(); !m.Held || !m.HeldAt.Equal(hold.At) {
		t.Fatalf("after hold: %+v", m)
	}
	// The change is on disk, whole.
	if waiting, _ := l.Waiting(false); len(waiting) != 1 || !waiting[0].Held {
		t.Fatalf("on disk: %+v", waiting)
	}

	release := cmdFor(w, "")
	release.Verb, release.At = cmdVerbRelease, base.Add(12*time.Minute)
	if _, err := l.SendCommand(release); err != nil {
		t.Fatal(err)
	}
	w.ApplyCommands()
	m := w.Marker()
	if m.Held || !m.HeldAt.IsZero() || m.HeldFor != 10*time.Minute {
		t.Fatalf("after release: %+v", m)
	}
	// Aging moved on by the held time.
	if want := base.Add(10 * time.Minute); !m.AgeStart.Equal(want) {
		t.Errorf("AgeStart = %s, want %s", m.AgeStart, want)
	}
	// Releasing a waiter that is not held changes nothing.
	again := cmdFor(w, "")
	again.Verb, again.At = cmdVerbRelease, base.Add(20*time.Minute)
	if _, err := l.SendCommand(again); err != nil {
		t.Fatal(err)
	}
	w.ApplyCommands()
	if got := w.Marker(); got.HeldFor != 10*time.Minute || !got.AgeStart.Equal(m.AgeStart) {
		t.Errorf("a release of a free waiter changed it: %+v", got)
	}
}

func TestAPriorityOrderedDuringAHoldRestartsAgingAtTheRelease(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	base := w.Marker().WaitingSince
	for _, step := range []struct {
		verb string
		at   time.Duration
		prio string
	}{{cmdVerbHold, time.Minute, ""}, {cmdVerbPri, 2 * time.Minute, PriorityLow}, {cmdVerbRelease, 9 * time.Minute, ""}} {
		c := cmdFor(w, step.prio)
		c.Verb, c.At = step.verb, base.Add(step.at)
		if _, err := l.SendCommand(c); err != nil {
			t.Fatal(err)
		}
		w.ApplyCommands()
	}
	if m := w.Marker(); !m.AgeStart.Equal(base.Add(9 * time.Minute)) {
		t.Errorf("AgeStart = %s, want the release time %s", m.AgeStart, base.Add(9*time.Minute))
	}
}

func TestAHeldWaiterDoesNotAgeOrCountItsHeldTime(t *testing.T) {
	iv := 30 * time.Minute
	held := Marker{Seq: 1, Priority: PriorityLow, WaitingSince: t0, Held: true, HeldAt: t0.Add(10 * time.Minute)}
	if got := EffectiveRank(held, t0.Add(5*time.Hour), iv); got != 0 {
		t.Errorf("a waiter held for hours aged to %d", got)
	}
	if d := WaitedFor(held, t0.Add(20*time.Minute)); d != 10*time.Minute {
		t.Errorf("WaitedFor while held = %s, want 10m", d)
	}
	freed := Marker{WaitingSince: t0, HeldFor: 5 * time.Minute}
	if d := WaitedFor(freed, t0.Add(20*time.Minute)); d != 15*time.Minute {
		t.Errorf("WaitedFor after a hold = %s, want 15m", d)
	}
}

func TestAHeldWaiterKeepsItsPlaceAndDoesNotBlockThoseBehind(t *testing.T) {
	cfg := DefaultConfig()
	waiting := []Marker{
		{Session: "a/held", Seq: 1, Held: true, HeldAt: t0, WaitingSince: t0},
		{Session: "b/next", Seq: 2, WaitingSince: t0},
	}
	d := Decide(View{Waiting: waiting}, healthy(), cfg, WaiterState{Seq: 1}, t0)
	if d.Admit || d.Reason != ReasonWaiterHold || !strings.Contains(d.Detail, "held by the operator") {
		t.Errorf("the held waiter: %+v", d)
	}
	if d := Decide(View{Waiting: waiting}, healthy(), cfg, WaiterState{Seq: 2}, t0); !d.Admit {
		t.Errorf("the waiter behind a held one was not admitted: %+v", d)
	}
	// Released, it is first again.
	waiting[0].Held = false
	if d := Decide(View{Waiting: waiting}, healthy(), cfg, WaiterState{Seq: 1}, t0); !d.Admit {
		t.Errorf("the released waiter: %+v", d)
	}
}

func TestHoldExpiresAtMaxWaitNeverAtZero(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	base := w.Marker().WaitingSince
	hold := cmdFor(w, "")
	hold.Verb, hold.At = cmdVerbHold, base
	if _, err := l.SendCommand(hold); err != nil {
		t.Fatal(err)
	}
	w.ApplyCommands()
	if w.ExpireHold(base.Add(24*time.Hour), 0) {
		t.Error("a hold expired with max-wait 0")
	}
	if w.ExpireHold(base.Add(4*time.Minute), 5*time.Minute) {
		t.Error("a hold expired early")
	}
	if !w.ExpireHold(base.Add(5*time.Minute), 5*time.Minute) {
		t.Fatal("a hold did not expire at max-wait")
	}
	if m := w.Marker(); m.Held || m.HeldFor != 5*time.Minute {
		t.Errorf("after expiry: %+v", m)
	}
}

// loopApplying plays a waiter's side: apply commands under the lock until
// stopped, leaving the line on a cancel as the gate does.
func loopApplying(l Ledger, w *Waiter) (stop func()) {
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			default:
			}
			if unlock, err := l.Lock(); err == nil {
				for _, c := range w.ApplyCommands() {
					if c.IsCancel() {
						w.Leave()
					}
				}
				unlock()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-finished }) }
}

func TestRequestHoldAndReleaseWaiterEndToEnd(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	stop := loopApplying(l, w)
	defer stop()

	res, err := l.RequestHoldWaiter(operator, "r/me", true, time.Now(), 5*time.Second)
	if err != nil || !res.Applied || res.Old != "not held" || res.New != "held" {
		t.Fatalf("hold = %+v, %v", res, err)
	}
	res, err = l.RequestHoldWaiter(operator, "r/me", false, time.Now(), 5*time.Second)
	if err != nil || !res.Applied || res.Old != "held" || res.New != "not held" {
		t.Fatalf("release = %+v, %v", res, err)
	}
	recs := readJournal(t, l)
	if len(recs) != 2 || recs[0].Act != "hold-waiter" || recs[1].Act != "release-waiter" || recs[0].Target != "r/me" {
		t.Errorf("journal = %+v", recs)
	}
}

func TestRequestCancelEndsTheWait(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	stop := loopApplying(l, w)
	defer stop()
	res, err := l.RequestCancel(operator, "r/me", time.Now(), 5*time.Second)
	if err != nil || !res.Applied || res.TooLate {
		t.Fatalf("cancel = %+v, %v", res, err)
	}
	if waiting, _ := l.Waiting(false); len(waiting) != 0 {
		t.Errorf("the cancelled waiter is still in line: %+v", waiting)
	}
	if recs := readJournal(t, l); len(recs) != 1 || recs[0].Act != "cancel" {
		t.Errorf("journal = %+v", recs)
	}
}

// Plan G2: a cancel that loses the race is ignored, journalled "too late" and
// reported; the running gate is untouched.
func TestRequestCancelTooLateWhenTheGateWasAdmittedFirst(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	var release func()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(100 * time.Millisecond)
		unlock, err := l.Lock()
		if err != nil {
			return
		}
		defer unlock()
		// The slot is written before the place is given up, under the lock, as in
		// the gate: a reader never sees neither.
		release, _ = l.Admit(Marker{Session: "r/me", Seq: 1, Kind: "merge", AdmittedAt: time.Now().UTC(), PID: 1})
		w.Leave()
	}()
	res, err := l.RequestCancel(operator, "r/me", time.Now(), 5*time.Second)
	wg.Wait()
	if release != nil {
		defer release()
	}
	if err != nil || res.Applied || !res.TooLate {
		t.Fatalf("cancel = %+v, %v; want too late", res, err)
	}
	recs := readJournal(t, l)
	if len(recs) != 2 || recs[0].Act != "cancel" || recs[1].Act != "cancel-too-late" {
		t.Errorf("journal = %+v", recs)
	}
}

func TestWaiterVerbsRefuseNonOperatorsAndUnknownWaiters(t *testing.T) {
	l := testLedger(t)
	newWaiter(t, l, "r/me", 1)
	if _, err := l.RequestCancel(Actor{}, "r/me", time.Now(), 0); err == nil {
		t.Error("a non-operator cancelled a wait")
	}
	if _, err := l.RequestHoldWaiter(Actor{Kind: "principal"}, "r/me", true, time.Now(), 0); err == nil {
		t.Error("a non-operator held a waiter")
	}
	if _, err := l.RequestCancel(operator, "r/nobody", time.Now(), 0); !errors.Is(err, ErrNoWaiter) {
		t.Errorf("err = %v, want ErrNoWaiter", err)
	}
	if recs := readJournal(t, l); len(recs) != 0 {
		t.Errorf("a refused verb was journalled: %+v", recs)
	}
}
