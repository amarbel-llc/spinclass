package keyholder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pairUnits(hash string) (agent, fibby string) {
	return "spinclass-keyholder-agent-" + hash + ".service", "spinclass-keyholder-fibby-" + hash + ".service"
}

// Units are per user but the state root follows the caller's XDG_STATE_HOME: a
// Launcher whose Base holds nothing for a hash still sees that hash's units. Find
// marks them Unowned; a hash with a directory, a key history or a lock file under
// THIS base is the base's own.
func TestFindMarksUnitsThisStateRootRecordedNothingForAsUnowned(t *testing.T) {
	foreign, withHistory, withLock := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"
	var units []string
	for _, h := range []string{foreign, withHistory, withLock} {
		a, f := pairUnits(h)
		units = append(units, a, f)
	}
	l, _ := sweepLauncher(t, units...)
	if err := os.MkdirAll(l.Base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Base, withHistory+".last.json"), []byte(`{"fingerprint":"SHA256:x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Base, withLock+".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(context.Background(), "s/own"); err != nil {
		t.Fatal(err)
	}
	own := Hash("s/own")
	oa, of := pairUnits(own)
	lf := sweepLauncher2(l, append(units, oa, of)...)
	found, err := lf.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Found{}
	for _, f := range found {
		by[f.Hash] = f
	}
	if !by[foreign].Unowned {
		t.Errorf("units under another state root = %+v, want Unowned", by[foreign])
	}
	for name, h := range map[string]string{"key history": withHistory, "lock file": withLock, "directory": own} {
		if by[h].Unowned {
			t.Errorf("a hash with a %s under this base is Unowned: %+v", name, by[h])
		}
	}
}

// sweepLauncher2 gives l a lister over the given units, keeping its fake.
func sweepLauncher2(l *Launcher, units ...string) *Launcher {
	if lf, ok := l.Run.(listingFake); ok {
		lf.units = units
		l.Run = lf
		return l
	}
	f := l.Run.(*fakeRunner)
	l.Run = listingFake{fakeRunner: f, units: units}
	return l
}

// A stop of a pair this state root did not record touches nothing: the units stay
// running and the error says "not mine".
func TestStopFoundRefusesAnUnownedPairAndLeavesItsUnitsRunning(t *testing.T) {
	foreign := "aaaaaaaaaaaaaaaa"
	l, lf := sweepLauncher(t)
	a, f := pairUnits(foreign)
	lf.active[a], lf.active[f] = true, true
	err := l.StopFound(context.Background(), Found{Hash: foreign, Units: []string{a, f}, Unowned: true})
	if !errors.Is(err, ErrNotMine) {
		t.Fatalf("StopFound of an unowned pair = %v, want ErrNotMine", err)
	}
	if !lf.active[a] || !lf.active[f] || len(lf.calls) != 0 {
		t.Errorf("an unowned pair was touched: active %v %v, calls %v", lf.active[a], lf.active[f], lf.calls)
	}
}

// The operator's explicit flag lets the stop act on them.
func TestStopFoundWithIncludeUnownedStopsTheUnits(t *testing.T) {
	foreign := "aaaaaaaaaaaaaaaa"
	l, lf := sweepLauncher(t)
	a, f := pairUnits(foreign)
	lf.active[a], lf.active[f] = true, true
	if err := l.StopFoundWith(context.Background(), Found{Hash: foreign, Units: []string{a, f}, Unowned: true}, StopOptions{IncludeUnowned: true}); err != nil {
		t.Fatal(err)
	}
	if lf.active[a] || lf.active[f] {
		t.Error("the unowned units were left running with IncludeUnowned")
	}
}

// A stop of a session that lives on keeps the key history, marked stopped, and
// the next start is announced as a new key against the stopped one.
func TestAStopWithKeepHistoryMarksTheHistoryStoppedAndTheNextStartIsAnnounced(t *testing.T) {
	l, _ := sweepLauncher(t)
	ctx := context.Background()
	p1, err := l.Start(ctx, "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.StopFoundWith(ctx, Found{Hash: Hash("s/a"), Session: "s/a", Dir: filepath.Join(l.Base, Hash("s/a"))}, StopOptions{KeepHistory: true}); err != nil {
		t.Fatal(err)
	}
	hist, ok := readLast(LastPath(l.Base, "s/a"))
	if !ok || !hist.Stopped || hist.StoppedAt.IsZero() || hist.Fingerprint != p1.Fingerprint {
		t.Fatalf("history after a kept stop = %+v (%v), want stopped with the last fingerprint %q", hist, ok, p1.Fingerprint)
	}
	if !HasState(l.Base, "s/a") {
		t.Error("a stopped session has no state on record: the restart preflight would not check it")
	}
	h, err := l.HealthOf(ctx, "s/a")
	if err != nil || !h.Stopped || !strings.Contains(h.Note(), "STOPPED") || !strings.Contains(h.Note(), p1.Fingerprint) {
		t.Errorf("health after a kept stop = %+v (%v), note %q", h, err, h.Note())
	}
	p2, err := l.Start(ctx, "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Reminted == nil || p2.Reminted.OldFingerprint != p1.Fingerprint || p2.Reminted.NewFingerprint != p2.Fingerprint {
		t.Fatalf("the start after a kept stop = %+v, want a re-mint announced old %q new %q", p2.Reminted, p1.Fingerprint, p2.Fingerprint)
	}
	if hist, _ := readLast(LastPath(l.Base, "s/a")); hist.Stopped {
		t.Errorf("the history is still marked stopped after a new mint: %+v", hist)
	}
}

// Without KeepHistory (a pair whose session is gone) the history goes, so the
// first start of a never-seen session announces nothing.
func TestAStopWithoutKeepHistoryRemovesTheHistoryAndTheNextStartIsNotAChange(t *testing.T) {
	l, _ := sweepLauncher(t)
	ctx := context.Background()
	if _, err := l.Start(ctx, "s/a"); err != nil {
		t.Fatal(err)
	}
	if err := l.StopFound(ctx, Found{Hash: Hash("s/a"), Session: "s/a", Dir: filepath.Join(l.Base, Hash("s/a"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LastPath(l.Base, "s/a")); !os.IsNotExist(err) {
		t.Errorf("the history of an orphan survived StopFound: %v", err)
	}
	p2, err := l.Start(ctx, "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Reminted != nil {
		t.Errorf("a never-seen session's first start announces a change: %+v", p2.Reminted)
	}
}
