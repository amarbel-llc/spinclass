package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/charlie/keyholdersweep"
)

// stopFixture stands a fake runner and a short keyholder root in for the real
// system, an empty session store, and a journal in a temporary directory, and
// starts one pair per session key.
type stopFixture struct {
	l       *keyholder.Launcher
	f       *khFakeRunner
	journal string
}

func newStopFixture(t *testing.T, sessions ...string) stopFixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // no session records: every pair is without a session
	base, err := os.MkdirTemp("", "khs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	f := newKhFake(t)
	l := &keyholder.Launcher{Base: filepath.Join(base, "kh"), Tools: keyholder.Tools{Fibby: "/pin/fibby", Piggy: "/pin/piggy"}, Run: f, Wait: time.Second}
	prev := keyholdersweep.NewLauncher
	keyholdersweep.NewLauncher = func() *keyholder.Launcher { return l }
	t.Cleanup(func() { keyholdersweep.NewLauncher = prev })
	journal := filepath.Join(base, "journal.ndjson")
	prevJ := keyholderStopJournal
	keyholderStopJournal = func() string { return journal }
	t.Cleanup(func() { keyholderStopJournal = prevJ })
	for _, s := range sessions {
		if _, err := l.Start(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	return stopFixture{l: l, f: f, journal: journal}
}

// agePair rewrites a pair's record so it was minted long ago: a pair with no
// session is an orphan only once it is old enough for a launch not to be in
// flight.
func agePair(t *testing.T, l *keyholder.Launcher, session string) {
	t.Helper()
	path := filepath.Join(l.Base, keyholder.Hash(session), "record.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["minted_at"] = "2020-01-01T00:00:00Z"
	b, _ = json.Marshal(m)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestKeyholderStopNeedsAll(t *testing.T) {
	newStopFixture(t)
	stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{}); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("without --all = %v, want an error naming --all", err)
	}
}

// A real stop is the operator's act: with no terminal nothing is stopped.
func TestKeyholderStopRefusesWithoutATerminalAndStopsNothing(t *testing.T) {
	fx := newStopFixture(t, "s/a")
	stubOperatorTerminal(t, false)
	err := runKeyholderStop(context.Background(), keyholderStopParams{All: true})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("no terminal = %v, want a refusal naming the terminals", err)
	}
	if fx.f.active["spinclass-keyholder-agent-"+keyholder.Hash("s/a")+".service"] != true {
		t.Error("a pair was stopped without a terminal")
	}
	if _, serr := os.Stat(fx.journal); serr == nil {
		t.Error("a refused stop wrote a journal entry")
	}
}

// --dry-run lists and marks the orphans, changes nothing, and needs no terminal.
func TestKeyholderStopDryRunListsAndMarksOrphansAndStopsNothing(t *testing.T) {
	fx := newStopFixture(t, "s/old", "s/young")
	agePair(t, fx.l, "s/old")
	stdout, _ := stubOperatorTerminal(t, false)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "ORPHAN "+keyholder.Hash("s/old")) {
		t.Errorf("the old pair with no session is not marked ORPHAN:\n%s", out)
	}
	if strings.Contains(out, "ORPHAN "+keyholder.Hash("s/young")) || !strings.Contains(out, "pair "+keyholder.Hash("s/young")) {
		t.Errorf("a launch-in-progress pair is marked orphan or missing:\n%s", out)
	}
	if !strings.Contains(out, "nothing was stopped") {
		t.Errorf("no dry-run summary:\n%s", out)
	}
	for _, s := range []string{"s/old", "s/young"} {
		if !fx.f.active["spinclass-keyholder-agent-"+keyholder.Hash(s)+".service"] {
			t.Errorf("--dry-run stopped %s", s)
		}
	}
}

// All pairs are stopped, units first and directories last, each stop is
// journalled with actor tty, and nothing is left.
func TestKeyholderStopAllStopsEveryPairAndJournalsEach(t *testing.T) {
	fx := newStopFixture(t, "s/a", "s/b")
	stdout, _ := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"s/a", "s/b"} {
		h := keyholder.Hash(s)
		if fx.f.active["spinclass-keyholder-agent-"+h+".service"] || fx.f.active["spinclass-keyholder-fibby-"+h+".service"] {
			t.Errorf("a unit of %s is still active", s)
		}
		if _, err := os.Lstat(filepath.Join(fx.l.Base, h)); !os.IsNotExist(err) {
			t.Errorf("the directory of %s survived: %v", s, err)
		}
	}
	if strings.Count(stdout.String(), "stopped ") != 2 {
		t.Errorf("output:\n%s", stdout.String())
	}
	b, err := os.ReadFile(fx.journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("journal has %d lines, want 2:\n%s", len(lines), b)
	}
	for _, line := range lines {
		var rec keyholderJournalRecord
		if err := json.Unmarshal(line, &rec); err != nil || rec.Actor != "tty" || rec.Action != "stop" || rec.Result != "stopped" || rec.Session == "" {
			t.Errorf("journal line %s = %+v (%v)", line, rec, err)
		}
	}
}

// A unit that will not stop is reported, journalled as failed, and leaves its
// directory; the other pairs are still stopped.
func TestKeyholderStopAllReportsAUnitThatWillNotStopAndStopsTheRest(t *testing.T) {
	fx := newStopFixture(t, "s/stuck", "s/fine")
	fx.f.mu.Lock()
	fx.f.stuck = map[string]bool{"spinclass-keyholder-agent-" + keyholder.Hash("s/stuck") + ".service": true}
	fx.f.mu.Unlock()
	_, stderr := stubOperatorTerminal(t, true)
	err := runKeyholderStop(context.Background(), keyholderStopParams{All: true})
	if err == nil || !strings.Contains(err.Error(), keyholder.Hash("s/stuck")) {
		t.Fatalf("a stuck unit = %v, want an error naming it", err)
	}
	if _, serr := os.Lstat(filepath.Join(fx.l.Base, keyholder.Hash("s/stuck"))); serr != nil {
		t.Errorf("the stuck pair's directory was removed: %v", serr)
	}
	if _, serr := os.Lstat(filepath.Join(fx.l.Base, keyholder.Hash("s/fine"))); !os.IsNotExist(serr) {
		t.Error("the other pair was not stopped")
	}
	if !strings.Contains(stderr.String(), keyholder.Hash("s/stuck")) {
		t.Errorf("stderr does not name the stuck pair:\n%s", stderr.String())
	}
	b, _ := os.ReadFile(fx.journal)
	if !bytes.Contains(b, []byte(`"result":"failed"`)) {
		t.Errorf("the failure was not journalled:\n%s", b)
	}
}

// The remedy for a too-costly key holder must not wait on its own audit trail.
func TestKeyholderStopStopsEvenWhenTheJournalCannotBeWritten(t *testing.T) {
	fx := newStopFixture(t, "s/a")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyholderStopJournal = func() string { return filepath.Join(blocker, "journal.ndjson") } // a file where a directory is needed
	_, stderr := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true}); err != nil {
		t.Fatalf("a journal failure failed the stop: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fx.l.Base, keyholder.Hash("s/a"))); !os.IsNotExist(err) {
		t.Error("the pair was not stopped")
	}
	if !strings.Contains(stderr.String(), "journalled") {
		t.Errorf("no warning about the journal:\n%s", stderr.String())
	}
}

func TestKeyholderStopWithNoPairsSaysSo(t *testing.T) {
	newStopFixture(t)
	stdout, _ := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "no key holder pairs") {
		t.Errorf("output = %q", stdout.String())
	}
}
