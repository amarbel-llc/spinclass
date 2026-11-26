package keyholder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordKindIsWriteOnceAndTheFirstWriterWins(t *testing.T) {
	l, _, _ := launcher(t)
	got, err := RecordKind(l.Base, "spinclass/k1", KindWorker)
	if err != nil || got != KindWorker {
		t.Fatalf("first record = %q, %v", got, err)
	}
	got, err = RecordKind(l.Base, "spinclass/k1", KindDriver)
	if err != nil || got != KindWorker {
		t.Fatalf("second record = %q, %v; want the first writer's %q", got, err, KindWorker)
	}
	if k, ok := ReadKind(l.Base, "spinclass/k1"); !ok || k != KindWorker {
		t.Errorf("ReadKind = %q, %v", k, ok)
	}
	fi, err := os.Stat(KindPath(l.Base, "spinclass/k1"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("kind file mode: %v %v", fi, err)
	}
}

func TestRecordKindRefusesAWordThatIsNotAKind(t *testing.T) {
	l, _, _ := launcher(t)
	if _, err := RecordKind(l.Base, "spinclass/k2", "admin"); err == nil {
		t.Error("recorded a kind that is neither driver nor worker")
	}
}

func TestReadKindRefusesALinkAndJunk(t *testing.T) {
	l, _, _ := launcher(t)
	if err := os.MkdirAll(l.Base, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(l.Base), "elsewhere")
	if err := os.WriteFile(target, []byte("driver\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, KindPath(l.Base, "spinclass/k3")); err != nil {
		t.Fatal(err)
	}
	if k, ok := ReadKind(l.Base, "spinclass/k3"); ok {
		t.Errorf("a symlinked kind file was read: %q", k)
	}
	if err := os.WriteFile(KindPath(l.Base, "spinclass/k4"), []byte("root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if k, ok := ReadKind(l.Base, "spinclass/k4"); ok {
		t.Errorf("junk was read as a kind: %q", k)
	}
}

// Stop is the session's own end: it ends the kind record with the key history.
func TestStopEndsTheKindRecord(t *testing.T) {
	l, _, _ := launcher(t)
	if _, err := l.Start(context.Background(), "spinclass/k5"); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordKind(l.Base, "spinclass/k5", KindWorker); err != nil {
		t.Fatal(err)
	}
	if err := l.Stop(context.Background(), "spinclass/k5"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(KindPath(l.Base, "spinclass/k5")); !os.IsNotExist(err) {
		t.Errorf("the kind record survived the session's stop: %v", err)
	}
}

// The operator's stop-all keeps the session's history, and so its kind: the
// session lives on.
func TestAStopThatKeepsTheHistoryKeepsTheKindRecord(t *testing.T) {
	l, _, _ := launcher(t)
	if _, err := l.Start(context.Background(), "spinclass/k6"); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordKind(l.Base, "spinclass/k6", KindWorker); err != nil {
		t.Fatal(err)
	}
	found, err := l.Find(context.Background())
	if err != nil || len(found) != 1 {
		t.Fatalf("Find = %v, %v", found, err)
	}
	if err := l.StopFoundWith(context.Background(), found[0], StopOptions{KeepHistory: true}); err != nil {
		t.Fatal(err)
	}
	if k, ok := ReadKind(l.Base, "spinclass/k6"); !ok || k != KindWorker {
		t.Errorf("the kind record after a history-keeping stop = %q, %v; want it kept", k, ok)
	}
}
