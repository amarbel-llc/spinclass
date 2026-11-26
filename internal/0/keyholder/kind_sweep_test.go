package keyholder

import (
	"context"
	"os"
	"testing"
)

// A `<hash>.kind` left with no directory, no unit, no lock and no key history is
// a stray record: Find reports it as KindOnly and the sweep's stop removes it.
func TestAStrayKindRecordIsFoundAndRemoved(t *testing.T) {
	l, _, _ := launcher(t)
	if _, err := RecordKind(l.Base, "s/stray", KindWorker); err != nil {
		t.Fatal(err)
	}
	found, err := l.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got *Found
	for i := range found {
		if found[i].Hash == Hash("s/stray") {
			got = &found[i]
		}
	}
	if got == nil || !got.KindOnly || got.Dir != "" || got.Unowned {
		t.Fatalf("Find = %+v, want the stray record as an owned KindOnly entry", found)
	}
	if err := l.StopFound(context.Background(), *got); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(KindPath(l.Base, "s/stray")); !os.IsNotExist(err) {
		t.Errorf("the stray kind record survived StopFound: %v", err)
	}
	if _, err := os.Lstat(LockPath(l.Base, "s/stray")); !os.IsNotExist(err) {
		t.Errorf("the sweep left its lock file behind: %v", err)
	}
}

// A kind record that has a key history beside it is a stopped session's, kept on
// purpose: it is not stray.
func TestAKindRecordWithAKeyHistoryIsNotStray(t *testing.T) {
	l, _, _ := launcher(t)
	if _, err := RecordKind(l.Base, "s/kept", KindWorker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Base+"/"+Hash("s/kept")+".last.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := l.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if f.Hash == Hash("s/kept") && f.KindOnly {
			t.Fatalf("a kind record with a key history was reported stray: %+v", f)
		}
	}
}
