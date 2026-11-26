package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// The marker is only useful if a real build carries it: this very test binary
// links the key holder code, so it must contain the marker.
func TestTheKeyHolderBuildMarkerIsEmbeddedInARealBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path:", err)
	}
	ok, err := binaryHasMarker(exe, keyholder.BuildMarker)
	if err != nil || !ok {
		t.Fatalf("the build marker is not in %s (ok=%v err=%v): an old binary could not be told from this one", exe, ok, err)
	}
}

func staleFixture(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old, cur := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	if err := os.WriteFile(old, []byte("a spinclass from before the key holder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, []byte("x "+keyholder.BuildMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := liveServeProcesses
	t.Cleanup(func() { liveServeProcesses = prev })
	liveServeProcesses = func() []serveProcess {
		return []serveProcess{
			{pid: 3, session: "a/old", exe: "/store/old", binary: old},
			{pid: 1, session: "b/new", exe: "/store/new", binary: cur},
		}
	}
}

func TestKeyholderStaleServesNamesOnlyServesWithoutTheMarkerWhileTheSwitchIsOn(t *testing.T) {
	staleFixture(t)
	prev := keyholderSwitchOnFn
	t.Cleanup(func() { keyholderSwitchOnFn = prev })
	keyholderSwitchOnFn = func() bool { return true }
	got := keyholderStaleServes()
	if len(got) != 1 || got[0].PID != 3 {
		t.Fatalf("stale serves = %+v, want only pid 3", got)
	}
	out := formatKeyholderStaleServes(got)
	if !strings.Contains(out, "KEY HOLDER STALE SERVE: 1") || !strings.Contains(out, "a/old (pid 3)") {
		t.Errorf("format = %q", out)
	}
}

func TestKeyholderStaleServesSaysNothingWhileTheSwitchIsOff(t *testing.T) {
	staleFixture(t)
	prev := keyholderSwitchOnFn
	t.Cleanup(func() { keyholderSwitchOnFn = prev })
	keyholderSwitchOnFn = func() bool { return false }
	if got := keyholderStaleServes(); len(got) != 0 {
		t.Errorf("stale serves = %+v with the switch off, want none", got)
	}
	if formatKeyholderStaleServes(nil) != "" {
		t.Error("no stale serve must render nothing")
	}
}
