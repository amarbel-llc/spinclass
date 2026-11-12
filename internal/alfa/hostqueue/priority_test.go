package hostqueue

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEffectiveRankAges(t *testing.T) {
	iv := 30 * time.Minute
	low := Marker{Seq: 1, Priority: PriorityLow, WaitingSince: t0}
	for _, tc := range []struct {
		at   time.Duration
		want int
	}{{0, 0}, {29 * time.Minute, 0}, {30 * time.Minute, 1}, {59 * time.Minute, 1}, {60 * time.Minute, 2}, {10 * time.Hour, 2}} {
		if got := EffectiveRank(low, t0.Add(tc.at), iv); got != tc.want {
			t.Errorf("low after %s: rank %d, want %d", tc.at, got, tc.want)
		}
	}
	// Setting the priority restarts the clock.
	reset := low
	reset.PrioritySetAt = t0.Add(50 * time.Minute)
	if got := EffectiveRank(reset, t0.Add(70*time.Minute), iv); got != 0 {
		t.Errorf("rank %d 20 min after a priority was set, want 0", got)
	}
	// No interval means no aging; "high" is the ceiling; unset is normal.
	if got := EffectiveRank(low, t0.Add(10*time.Hour), 0); got != 0 {
		t.Errorf("rank %d with aging off, want 0", got)
	}
	if got := EffectiveRank(Marker{Priority: PriorityHigh, WaitingSince: t0}, t0.Add(10*time.Hour), iv); got != 2 {
		t.Errorf("high aged to %d", got)
	}
	if got := EffectiveRank(Marker{WaitingSince: t0}, t0, iv); got != 1 {
		t.Errorf("unset priority ranks %d, want normal 1", got)
	}
	if _, err := ParsePriority("urgent"); err == nil {
		t.Error("an unknown level must be refused")
	}
}

func TestPriorityOrdersTheLineThenPlace(t *testing.T) {
	cfg := DefaultConfig()
	waiting := []Marker{
		{Session: "a/n1", Seq: 1, WaitingSince: t0},
		{Session: "b/h", Seq: 2, Priority: PriorityHigh, WaitingSince: t0},
		{Session: "c/l", Seq: 3, Priority: PriorityLow, WaitingSince: t0},
		{Session: "d/n2", Seq: 4, WaitingSince: t0},
	}
	want := map[uint64]int{2: 1, 1: 2, 4: 3, 3: 4} // high, then normals by place, then low
	for seq, pos := range want {
		got, total, _ := position(waiting, seq, t0, cfg.AgingInterval)
		if got != pos || total != 4 {
			t.Errorf("seq %d: position %d of %d, want %d of 4", seq, got, total, pos)
		}
	}
	// "behind" names the waiter directly in front.
	if _, _, ahead := position(waiting, 3, t0, cfg.AgingInterval); ahead != "d/n2" {
		t.Errorf("the low waiter is behind %q, want d/n2", ahead)
	}
	// The high waiter is admitted past a normal one that asked first.
	d := Decide(View{Waiting: waiting}, healthy(), cfg, WaiterState{Seq: 2}, t0)
	if !d.Admit {
		t.Errorf("the high waiter was not admitted: %+v", d)
	}
	d = Decide(View{Waiting: waiting}, healthy(), cfg, WaiterState{Seq: 1}, t0)
	if d.Admit || d.Reason != ReasonQueue || !strings.Contains(d.Detail, "position 2 of 4") {
		t.Errorf("the first normal waiter: %+v", d)
	}
}

// A priority never gets past any other term: a high waiter still waits for the
// gate cap, the hold and the probe.
func TestPriorityNeverBypassesOtherTerms(t *testing.T) {
	cfg := DefaultConfig()
	waiting := []Marker{{Session: "a/h", Seq: 1, Priority: PriorityHigh, WaitingSince: t0}}
	w := WaiterState{Seq: 1}
	if d := Decide(View{Waiting: waiting, Live: []Marker{{Session: "x/y", AdmittedAt: t0}}}, healthy(), cfg, w, t0.Add(time.Hour)); d.Admit || d.Reason != ReasonGates {
		t.Errorf("gate cap: %+v", d)
	}
	if d := Decide(View{Waiting: waiting, Held: true}, healthy(), cfg, w, t0); d.Admit || d.Reason != ReasonHold {
		t.Errorf("hold: %+v", d)
	}
	low := healthy()
	low.MemAvailable = 1 * gib
	if d := Decide(View{Waiting: waiting}, low, cfg, w, t0); d.Admit || d.Reason != ReasonMemory {
		t.Errorf("memory: %+v", d)
	}
}

func TestAgedLowWaiterOvertakesAFreshNormalOne(t *testing.T) {
	cfg := DefaultConfig()
	now := t0.Add(61 * time.Minute)
	waiting := []Marker{
		{Session: "old/low", Seq: 1, Priority: PriorityLow, WaitingSince: t0},
		{Session: "new/normal", Seq: 2, WaitingSince: now},
	}
	if pos, _, _ := position(waiting, 1, now, cfg.AgingInterval); pos != 1 {
		t.Errorf("the aged low waiter is at %d, want 1", pos)
	}
	if pos, _, _ := position(waiting, 2, now, cfg.AgingInterval); pos != 2 {
		t.Errorf("the fresh normal waiter is at %d, want 2", pos)
	}
}

func TestPlacelessMarkerRanksNormalAndFirstAmongNormals(t *testing.T) {
	cfg := DefaultConfig()
	waiting := []Marker{
		{Session: "old/binary", Seq: 0, WaitingSince: t0},
		{Session: "new/normal", Seq: 5, WaitingSince: t0},
		{Session: "new/high", Seq: 6, Priority: PriorityHigh, WaitingSince: t0},
	}
	if pos, _, ahead := position(waiting, 5, t0, cfg.AgingInterval); pos != 3 || ahead != "old/binary" {
		t.Errorf("normal waiter: position %d behind %q", pos, ahead)
	}
	if pos, _, _ := position(waiting, 6, t0, cfg.AgingInterval); pos != 1 {
		t.Errorf("high waiter: position %d, want 1", pos)
	}
}

// Plan G6: a waiter that is behind carries no failing clock, so a priority raise
// that makes it head starts the clock fresh; idle-grace cannot admit it at once.
func TestRaisingPriorityDoesNotAdmitByIdleGraceAtOnce(t *testing.T) {
	cfg := DefaultConfig()
	low := healthy()
	low.MemAvailable = 1 * gib
	// Behind: even with an hour-old clock the refusal clears it.
	behind := View{Waiting: []Marker{
		{Session: "a/first", Seq: 1, WaitingSince: t0},
		{Session: "b/me", Seq: 2, WaitingSince: t0},
	}}
	d := Decide(behind, low, cfg, WaiterState{Seq: 2, ProbeFailingSince: t0.Add(-time.Hour)}, t0)
	if d.Admit || !d.Next.ProbeFailingSince.IsZero() {
		t.Fatalf("behind: %+v", d)
	}
	// Raised to high, now head: the clock starts at this look, nothing admits.
	raised := View{Waiting: []Marker{
		{Session: "a/first", Seq: 1, WaitingSince: t0},
		{Session: "b/me", Seq: 2, Priority: PriorityHigh, WaitingSince: t0},
	}}
	d = Decide(raised, low, cfg, d.Next, t0)
	if d.Admit || d.Next.ProbeFailingSince != t0 {
		t.Fatalf("raised: %+v", d)
	}
}

func newWaiter(t *testing.T, l Ledger, session string, seq uint64) *Waiter {
	t.Helper()
	w, err := l.OpenWait(Marker{Session: session, Seq: seq, PID: os.Getpid(), WaitingSince: time.Now().Add(-time.Minute).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Leave)
	return w
}

func cmdFor(w *Waiter, level string) Command {
	m := w.Marker()
	return Command{Verb: cmdVerbPri, Session: m.Session, Seq: m.Seq, PID: m.PID, WaitingSince: m.WaitingSince, Priority: level, At: time.Now().UTC(), Actor: ActorTTY}
}

func TestWaiterAppliesOnlyCommandsAddressedToIt(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	other := newWaiter(t, l, "r/other", 2)

	mine, err := l.SendCommand(cmdFor(w, PriorityHigh))
	if err != nil {
		t.Fatal(err)
	}
	wrongPID := cmdFor(w, PriorityLow)
	wrongPID.PID++
	forged, err := l.SendCommand(wrongPID)
	if err != nil {
		t.Fatal(err)
	}
	if applied := other.ApplyCommands(); len(applied) != 0 {
		t.Fatalf("a waiter applied a command for another: %+v", applied)
	}
	applied := w.ApplyCommands()
	if len(applied) != 1 || applied[0].Priority != PriorityHigh {
		t.Fatalf("applied = %+v", applied)
	}
	if got := w.Marker(); got.Priority != PriorityHigh || got.PrioritySetAt.IsZero() {
		t.Errorf("marker = %+v", got)
	}
	if CommandPending(mine) {
		t.Error("an applied command file was left")
	}
	if !CommandPending(forged) {
		t.Error("a command for no live waiter was applied or removed by the wrong party")
	}
	// The change is on disk, whole.
	waiting, _ := l.Waiting(false)
	for _, m := range waiting {
		if m.Seq == 1 && m.Priority != PriorityHigh {
			t.Errorf("on-disk marker = %+v", m)
		}
	}
	// Locked readers collect the command with no live target.
	l.CollectCommands(waiting, time.Now())
	if CommandPending(forged) {
		t.Error("a command with no live target survived collection")
	}
}

func TestCollectCommandsRemovesUnparseableAndOldFiles(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	bad := filepath.Join(l.Dir, "cmd-priority-junk.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := cmdFor(w, PriorityHigh)
	old.At = time.Now().Add(-2 * time.Hour).UTC()
	oldPath, err := l.SendCommand(old)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := l.SendCommand(cmdFor(w, PriorityLow))
	if err != nil {
		t.Fatal(err)
	}
	waiting, _ := l.Waiting(false)
	l.CollectCommands(waiting, time.Now())
	if CommandPending(bad) || CommandPending(oldPath) {
		t.Error("an unparseable or hour-old command was kept")
	}
	if !CommandPending(fresh) {
		t.Error("a fresh command for a live waiter was collected")
	}
}

// runWaiterLoop plays the waiter's side of the lifecycle: apply commands at each
// look until stopped.
func runWaiterLoop(l Ledger, w *Waiter) (stop func()) {
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
				w.ApplyCommands()
				unlock()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-finished }) }
}

func TestRequestPriorityEndToEnd(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	stop := runWaiterLoop(l, w)
	defer stop()

	res, err := l.RequestPriority(operator, "r/me", PriorityHigh, time.Now(), 5*time.Second)
	if err != nil || !res.Applied || res.Old != PriorityNormal || res.New != PriorityHigh {
		t.Fatalf("result = %+v, %v", res, err)
	}
	recs := readJournal(t, l)
	if len(recs) != 1 || recs[0].Act != "priority" || recs[0].Target != "r/me" || recs[0].Seq != 1 ||
		recs[0].Old != "normal" || recs[0].New != "high" || recs[0].Actor != "tty" {
		t.Errorf("journal = %+v", recs)
	}
}

func TestRequestPriorityRefusals(t *testing.T) {
	l := testLedger(t)
	newWaiter(t, l, "r/me", 1)
	if _, err := l.RequestPriority(Actor{}, "r/me", PriorityHigh, time.Now(), 0); err == nil {
		t.Error("a non-operator actor changed a priority")
	}
	if _, err := l.RequestPriority(operator, "r/me", "urgent", time.Now(), 0); err == nil {
		t.Error("an unknown level was accepted")
	}
	if _, err := l.RequestPriority(operator, "r/nobody", PriorityHigh, time.Now(), 0); !errors.Is(err, ErrNoWaiter) {
		t.Errorf("err = %v, want ErrNoWaiter", err)
	}
	if recs := readJournal(t, l); len(recs) != 0 {
		t.Errorf("a refused request was journalled: %+v", recs)
	}
}

// With no waiter looking, the request is reported as not applied and journalled
// as such; it changes nothing.
func TestRequestPriorityNotAppliedWhenTheWaiterNeverLooks(t *testing.T) {
	l := testLedger(t)
	w := newWaiter(t, l, "r/me", 1)
	res, err := l.RequestPriority(operator, "r/me", PriorityHigh, time.Now(), 200*time.Millisecond)
	if err != nil || res.Applied {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := w.Marker(); got.Priority != "" {
		t.Errorf("marker changed: %+v", got)
	}
	recs := readJournal(t, l)
	if len(recs) != 2 || recs[1].Act != "priority-unapplied" {
		t.Errorf("journal = %+v", recs)
	}
}

// The priority verb fails CLOSED: no journal line, no command.
func TestRequestPriorityFailsClosedWithoutAJournal(t *testing.T) {
	l := testLedger(t)
	newWaiter(t, l, "r/me", 1)
	if err := os.MkdirAll(filepath.Dir(l.HoldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(l.JournalPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RequestPriority(operator, "r/me", PriorityHigh, time.Now(), 50*time.Millisecond); err == nil {
		t.Fatal("want an error when the journal cannot be written")
	}
	entries, _ := os.ReadDir(l.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), cmdPrefix) {
			t.Errorf("a command was sent without a journal line: %s", e.Name())
		}
	}
}
