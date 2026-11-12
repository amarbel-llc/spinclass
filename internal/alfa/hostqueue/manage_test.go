package hostqueue

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var operator = Actor{Kind: ActorTTY}

func readJournal(t *testing.T, l Ledger) []JournalRecord {
	t.Helper()
	f, err := os.Open(l.JournalPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []JournalRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r JournalRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("journal line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

func TestHoldAndReleaseAreJournalled(t *testing.T) {
	l := testLedger(t)
	out, err := l.PlaceHold(operator, "deploy", t0)
	if err != nil || !out.Changed || out.Warning != "" {
		t.Fatalf("hold = %+v, %v", out, err)
	}
	if held, why := l.Hold(); !held || why != "deploy" {
		t.Fatalf("hold = %v %q", held, why)
	}
	if out, err = l.PlaceHold(operator, "deploy 2", t0); err != nil || !out.Changed {
		t.Fatalf("re-hold = %+v, %v", out, err)
	}
	if out, err = l.LiftHold(operator, t0); err != nil || !out.Changed {
		t.Fatalf("release = %+v, %v", out, err)
	}
	if held, _ := l.Hold(); held {
		t.Fatal("still held after release")
	}
	// Releasing an absent hold changes and writes nothing.
	if out, err = l.LiftHold(operator, t0); err != nil || out.Changed {
		t.Fatalf("second release = %+v, %v", out, err)
	}
	recs := readJournal(t, l)
	if len(recs) != 3 {
		t.Fatalf("journal = %+v, want hold, hold, release", recs)
	}
	if recs[0].Act != "hold" || recs[0].New != "deploy" || recs[0].Old != "" || recs[0].Actor != "tty" || recs[0].Assurance != "tty" {
		t.Errorf("first = %+v", recs[0])
	}
	if recs[1].Old != "deploy" || recs[1].New != "deploy 2" {
		t.Errorf("second = %+v", recs[1])
	}
	if recs[2].Act != "release" || recs[2].Old != "deploy 2" {
		t.Errorf("third = %+v", recs[2])
	}
	if info, err := os.Stat(l.JournalPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("journal mode = %v (%v)", info.Mode().Perm(), err)
	}
}

func TestOnlyTheOperatorAtATerminalMayManage(t *testing.T) {
	l := testLedger(t)
	for _, a := range []Actor{{}, {Kind: "principal", Principal: "p"}} {
		if _, err := l.PlaceHold(a, "x", t0); err == nil {
			t.Errorf("actor %+v placed a hold", a)
		}
		if _, err := l.LiftHold(a, t0); err == nil {
			t.Errorf("actor %+v lifted a hold", a)
		}
	}
	if held, _ := l.Hold(); held {
		t.Error("a refused verb changed the hold")
	}
	if recs := readJournal(t, l); len(recs) != 0 {
		t.Errorf("a refused verb was journalled: %+v", recs)
	}
}

// The whole-queue hold is the brake: it fails open when the journal cannot be
// written, with a loud warning (plan G8).
func TestHoldFailsOpenWhenTheJournalCannotBeWritten(t *testing.T) {
	l := testLedger(t)
	if err := os.MkdirAll(filepath.Dir(l.HoldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// A directory where the journal file belongs makes every append fail.
	if err := os.Mkdir(l.JournalPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := l.PlaceHold(operator, "stop", t0)
	if err != nil || !out.Changed || !strings.Contains(out.Warning, "WARNING") {
		t.Fatalf("hold = %+v, %v; want it placed with a warning", out, err)
	}
	if held, _ := l.Hold(); !held {
		t.Fatal("the hold was not placed")
	}
	out, err = l.LiftHold(operator, t0)
	if err != nil || !out.Changed || !strings.Contains(out.Warning, "WARNING") {
		t.Fatalf("release = %+v, %v", out, err)
	}
}

func TestEveryVerbIsDeclared(t *testing.T) {
	seen := map[Verb]bool{}
	for _, v := range Verbs {
		if v.Target == "" {
			t.Errorf("verb %s has no target", v.Verb)
		}
		seen[v.Verb] = true
	}
	for _, v := range []Verb{VerbHold, VerbRelease} {
		if !seen[v] {
			t.Errorf("verb %s is not declared", v)
		}
	}
}
