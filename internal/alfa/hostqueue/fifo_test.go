package hostqueue

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// spinclass#482: admission goes by place in line, not by who polls first.

func waiter(session string, seq uint64) Marker {
	return Marker{Session: session, Kind: "merge", Seq: seq, WaitingSince: t0.Add(-time.Duration(100-seq) * time.Second)}
}

// With a free slot and a healthy host, only the waiter at the head of the line
// is admitted; every other sees its place and who is ahead.
func TestDecideAdmitsOnlyTheHeadOfTheQueue(t *testing.T) {
	cfg := DefaultConfig()
	waiting := []Marker{waiter("r/a", 4), waiter("r/b", 5), waiter("r/c", 7)}
	cases := []struct {
		seq        uint64
		admit      bool
		wantDetail string
	}{
		{4, true, ""},
		{5, false, "position 2 of 3: behind r/a"},
		{7, false, "position 3 of 3: behind r/b"},
	}
	for _, c := range cases {
		d := Decide(View{Waiting: waiting}, healthy(), cfg, WaiterState{Seq: c.seq}, t0)
		if d.Admit != c.admit {
			t.Fatalf("seq %d: admit = %v (%+v), want %v", c.seq, d.Admit, d, c.admit)
		}
		if c.admit {
			continue
		}
		if d.Reason != ReasonQueue || d.Detail != c.wantDetail {
			t.Errorf("seq %d: = %q %q, want %q %q", c.seq, d.Reason, d.Detail, ReasonQueue, c.wantDetail)
		}
	}
}

// The head of the queue is judged by the gate count and the probe as before,
// and its refusal begins with its place.
func TestDecideHeadStillFacesTheGatesAndTheProbe(t *testing.T) {
	cfg := DefaultConfig()
	waiting := []Marker{waiter("r/a", 1), waiter("r/b", 2)}
	live := []Marker{{Session: "r/old", AdmittedAt: t0.Add(-10 * time.Minute)}}

	d := Decide(View{Live: live, Waiting: waiting}, healthy(), cfg, WaiterState{Seq: 1}, t0)
	if d.Admit || d.Reason != ReasonGates || !strings.HasPrefix(d.Detail, "position 1 of 2: 1 of 1 gate slots taken") {
		t.Errorf("gate cap: %+v", d)
	}
	snap := healthy()
	snap.MemAvailable = 1 * gib
	d = Decide(View{Waiting: waiting}, snap, cfg, WaiterState{Seq: 1}, t0)
	if d.Admit || d.Reason != ReasonMemory || !strings.HasPrefix(d.Detail, "position 1 of 2: waiting for memory") {
		t.Errorf("memory: %+v", d)
	}
	d = Decide(View{Held: true, HoldReason: "deploy", Waiting: waiting}, healthy(), cfg, WaiterState{Seq: 2}, t0)
	if d.Reason != ReasonHold || d.Detail != "position 2 of 2: held by the operator: deploy" {
		t.Errorf("hold: %+v", d)
	}
}

// A waiter behind someone else never passes a failing probe or a free slot:
// the queue is strict, so a waiter that could fit is not admitted past an
// earlier one (spinclass#482).
func TestDecideIsStrictFIFO(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxGates = 2
	waiting := []Marker{waiter("r/a", 1), waiter("r/b", 2)}
	live := []Marker{{Session: "r/old", AdmittedAt: t0.Add(-10 * time.Minute)}}
	d := Decide(View{Live: live, Waiting: waiting}, healthy(), cfg, WaiterState{Seq: 2}, t0)
	if d.Admit {
		t.Fatalf("a free slot and a healthy host admit only the head, got %+v", d)
	}
}

// idle-grace is the head's: a waiter behind another does not run its clock.
func TestDecideIdleGraceBelongsToTheHead(t *testing.T) {
	cfg := DefaultConfig()
	snap := healthy()
	snap.MemAvailable = 1 * gib
	waiting := []Marker{waiter("r/a", 1), waiter("r/b", 2)}

	behind := WaiterState{Seq: 2, ProbeFailingSince: t0.Add(-time.Hour)}
	d := Decide(View{Waiting: waiting}, snap, cfg, behind, t0)
	if d.Admit || !d.Next.ProbeFailingSince.IsZero() {
		t.Errorf("behind the head: %+v, want held with the grace clock reset", d)
	}
	head := WaiterState{Seq: 1, ProbeFailingSince: t0.Add(-time.Hour)}
	d = Decide(View{Waiting: waiting}, snap, cfg, head, t0)
	if !d.Admit || !d.Grace {
		t.Errorf("head after the grace: %+v, want admitted by idle-grace", d)
	}
}

// A marker from a binary that predates numbering (Seq 0) is ahead of every
// numbered waiter; a caller with no place (Seq 0) is first in line.
func TestDecideLegacyMarkersAndPlacelessCallers(t *testing.T) {
	cfg := DefaultConfig()
	legacy := Marker{Session: "r/old-binary", Kind: "merge", WaitingSince: t0.Add(-time.Hour)}
	d := Decide(View{Waiting: []Marker{legacy, waiter("r/a", 3)}}, healthy(), cfg, WaiterState{Seq: 3}, t0)
	if d.Admit || d.Detail != "position 2 of 2: behind r/old-binary" {
		t.Errorf("behind a legacy marker: %+v", d)
	}
	d = Decide(View{Waiting: []Marker{waiter("r/a", 3)}}, healthy(), cfg, WaiterState{}, t0)
	if !d.Admit {
		t.Errorf("a caller with no place is judged on the host alone: %+v", d)
	}
}

// Waiting lists in queue order, by place, whatever order the files were
// written or their waiting-since stamps.
func TestLedgerWaitingIsInQueueOrder(t *testing.T) {
	l := testLedger(t)
	var leaves []func()
	for _, m := range []Marker{
		{Session: "r/three", Seq: 3, WaitingSince: t0},
		{Session: "r/one", Seq: 1, WaitingSince: t0.Add(time.Hour)},
		{Session: "r/two", Seq: 2, WaitingSince: t0.Add(-time.Hour)},
	} {
		_, leave, err := l.Wait(m)
		if err != nil {
			t.Fatal(err)
		}
		leaves = append(leaves, leave)
	}
	defer func() {
		for _, f := range leaves {
			f()
		}
	}()
	w, err := l.Waiting(false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range w {
		got = append(got, m.Session)
	}
	if strings.Join(got, ",") != "r/one,r/two,r/three" {
		t.Errorf("order = %v, want by place", got)
	}
}

// Places rise with every request, also when many ask at once, and never repeat.
func TestLedgerNextSeqIsMonotonicAndUniqueUnderConcurrency(t *testing.T) {
	l := testLedger(t)
	const n = 40
	var (
		mu   sync.Mutex
		seen = map[uint64]bool{}
		wg   sync.WaitGroup
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := l.Lock()
			if err != nil {
				t.Error(err)
				return
			}
			s, err := l.NextSeq()
			unlock()
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[s] {
				t.Errorf("place %d handed out twice", s)
			}
			seen[s] = true
		}()
	}
	wg.Wait()
	for s := uint64(1); s <= n; s++ {
		if !seen[s] {
			t.Errorf("place %d was never handed out (a gap or a repeat)", s)
		}
	}
}

// A lost counter cannot hand out a place a live marker already holds.
func TestLedgerNextSeqNeverReusesALivePlace(t *testing.T) {
	l := testLedger(t)
	unlock, err := l.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, leave, err := l.Wait(Marker{Session: "r/high", Seq: 41, WaitingSince: t0})
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	s, err := l.NextSeq()
	if err != nil || s != 42 {
		t.Errorf("NextSeq = %d, %v; want 42, past the live marker's place", s, err)
	}
	s, err = l.NextSeq()
	if err != nil || s != 43 {
		t.Errorf("NextSeq = %d, %v; want 43", s, err)
	}
}
