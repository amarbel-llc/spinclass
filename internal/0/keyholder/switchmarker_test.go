package keyholder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTheSwitchMarkerIsWrittenOnceOwnerOnlyAndCountsAsState(t *testing.T) {
	base := filepath.Join(t.TempDir(), "spinclass", "keyholder")
	if has, _ := HasAnyState(base); has {
		t.Fatal("a root that does not exist is state")
	}
	if err := WriteSwitchMarker(base); err != nil {
		t.Fatal(err)
	}
	if err := WriteSwitchMarker(base); err != nil {
		t.Fatalf("a second write of our own marker = %v, want it left as it is", err)
	}
	fi, err := os.Lstat(SwitchMarkerPath(base))
	if err != nil || fi.Mode().Perm() != 0o600 || !HasSwitchMarker(base) {
		t.Fatalf("marker: %v %v", fi, err)
	}
	if has, _ := HasAnyState(base); !has {
		t.Error("the marker is not evidence: HasAnyState says no")
	}
	if err := RemoveSwitchMarker(base); err != nil {
		t.Fatal(err)
	}
	if HasSwitchMarker(base) {
		t.Error("the marker survived its removal")
	}
	if err := RemoveSwitchMarker(base); err != nil {
		t.Errorf("removing an absent marker = %v, want nil", err)
	}
}

func TestALinkAtTheMarkerNameIsNeverFollowedOrAcceptedAsTheMarker(t *testing.T) {
	base := filepath.Join(t.TempDir(), "spinclass", "keyholder")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, SwitchMarkerPath(base)); err != nil {
		t.Fatal(err)
	}
	if HasSwitchMarker(base) {
		t.Error("a link counts as the marker")
	}
	if err := WriteSwitchMarker(base); err == nil {
		t.Error("writing the marker through a link succeeded")
	}
	if b, _ := os.ReadFile(target); string(b) != "x" {
		t.Errorf("the link target was written: %q", b)
	}
}

// The marker belongs to no session: Find, the stop of a pair, the forgetting of a
// session and a stop of everything found never touch it.
func TestTheSweepsAndStopsAndCloseLeaveTheSwitchMarkerAlone(t *testing.T) {
	l, _, _ := launcher(t)
	ctx := context.Background()
	if err := WriteSwitchMarker(l.Base); err != nil {
		t.Fatal(err)
	}
	found, err := l.Find(ctx)
	if err != nil || len(found) != 0 {
		t.Fatalf("Find with only the marker = (%v, %v), want nothing found", found, err)
	}
	if _, err := l.Start(ctx, "spinclass/alpha"); err != nil {
		t.Fatal(err)
	}
	if err := l.Stop(ctx, "spinclass/alpha"); err != nil {
		t.Fatal(err)
	}
	if !HasSwitchMarker(l.Base) {
		t.Error("a Stop removed the switch marker")
	}
	if err := l.ForgetSession(ctx, "spinclass/alpha"); err != nil {
		t.Fatal(err)
	}
	if !HasSwitchMarker(l.Base) {
		t.Error("ForgetSession (sc close) removed the switch marker")
	}
	if _, err := l.Start(ctx, "spinclass/beta"); err != nil {
		t.Fatal(err)
	}
	found, err = l.Find(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if err := l.StopFoundWith(ctx, f, StopOptions{KeepHistory: false}); err != nil {
			t.Fatal(err)
		}
	}
	if !HasSwitchMarker(l.Base) {
		t.Error("stopping everything Find lists removed the switch marker")
	}
}
