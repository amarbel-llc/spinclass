package keyholder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeHistory(t *testing.T, base, session string) string {
	t.Helper()
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(base, Hash(session)+".last.json")
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func findHash(found []Found, h string) *Found {
	for i := range found {
		if found[i].Hash == h {
			return &found[i]
		}
	}
	return nil
}

// A key history with no directory, no unit and no lock (and a kind record beside
// it) is what `sc clean` leaves when it reaps a session's worktree after an
// operator stop kept the history: Find reports it, and the sweep's stop removes
// the history and the kind record, and the lock it took.
func TestAHistoryOnlyLeftoverIsFoundAndRemovedWithItsKindRecord(t *testing.T) {
	l, _, _ := launcher(t)
	hist := writeHistory(t, l.Base, "s/gone")
	if _, err := RecordKind(l.Base, "s/gone", KindWorker); err != nil {
		t.Fatal(err)
	}
	found, err := l.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := findHash(found, Hash("s/gone"))
	if got == nil || !got.HistoryOnly || got.KindOnly || got.LockOnly || got.Dir != "" || got.Unowned {
		t.Fatalf("Find = %+v, want the history as an owned HistoryOnly entry", found)
	}
	if err := l.StopFound(context.Background(), *got); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{hist, KindPath(l.Base, "s/gone"), LockPath(l.Base, "s/gone")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep's stop: %v", p, err)
		}
	}
}

// Found said there was no directory; a launch may have made one since. A stop of
// a HistoryOnly entry looks again under the lock and leaves what a start made.
func TestStopFoundOfAHistoryOnlyEntryLeavesAPairALaunchMadeSince(t *testing.T) {
	l, _, _ := launcher(t)
	hist := writeHistory(t, l.Base, "s/racing")
	found, err := l.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := findHash(found, Hash("s/racing"))
	if got == nil || !got.HistoryOnly {
		t.Fatalf("Find = %+v, want a HistoryOnly entry", found)
	}
	if err := os.MkdirAll(filepath.Join(l.Base, Hash("s/racing")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := l.StopFound(context.Background(), *got); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(hist); err != nil {
		t.Errorf("a history of a pair a launch made since was removed: %v", err)
	}
}

// A history beside a pair directory is that pair's, not a leftover.
func TestAKeyHistoryBesideAPairDirectoryIsNotHistoryOnly(t *testing.T) {
	l, _, _ := launcher(t)
	writeHistory(t, l.Base, "s/live")
	if err := os.MkdirAll(filepath.Join(l.Base, Hash("s/live")), 0o700); err != nil {
		t.Fatal(err)
	}
	found, err := l.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := findHash(found, Hash("s/live")); got == nil || got.HistoryOnly {
		t.Fatalf("Find = %+v, a history beside a directory must not be HistoryOnly", found)
	}
}
