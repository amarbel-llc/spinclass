package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// Whether a REAL build carries the marker is not decided here: this test binary
// holds the marker text for any reference a test file makes, so a scan of it
// proves nothing about the shipped binary. The bats lane greps the built
// spinclass for the marker (zz-tests_bats/serve.bats,
// keyholder_build_marker_is_in_the_shipped_binary).

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

// The list is a fact about binaries: with no sweatfile anywhere and no
// [keyholder] table (the switch off), a stale serve is still named, so the
// operator can check it BEFORE switching on. It must not depend on the working
// directory or on HOME either.
func TestKeyholderStaleServesAreListedWithTheSwitchOffAndFromAnyDirectory(t *testing.T) {
	staleFixture(t)
	t.Setenv("HOME", t.TempDir())
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })
	got := keyholderStaleServes()
	if len(got) != 1 || got[0].PID != 3 {
		t.Fatalf("stale serves = %+v, want only pid 3", got)
	}
	out := formatKeyholderStaleServes(got)
	if !strings.Contains(out, "KEY HOLDER STALE SERVE: 1") || !strings.Contains(out, "a/old (pid 3)") || !strings.Contains(out, "before switching it on") {
		t.Errorf("format = %q", out)
	}
}

func TestKeyholderStaleServesNamesNothingWhenEveryServeCarriesTheMarker(t *testing.T) {
	staleFixture(t)
	prev := liveServeProcesses
	t.Cleanup(func() { liveServeProcesses = prev })
	all := prev()
	liveServeProcesses = func() []serveProcess {
		var cur []serveProcess
		for _, p := range all {
			if p.session == "b/new" {
				cur = append(cur, p)
			}
		}
		return cur
	}
	if got := keyholderStaleServes(); len(got) != 0 {
		t.Errorf("stale serves = %+v when every serve carries the marker, want none", got)
	}
	if formatKeyholderStaleServes(nil) != "" {
		t.Error("no stale serve must render nothing")
	}
}

// The scan looks for the marker the key holder package defines.
func TestTheStaleScanLooksForTheKeyHolderBuildMarker(t *testing.T) {
	if keyholderStaleMarker != keyholder.BuildMarker {
		t.Errorf("the stale scan looks for %q, not the build marker %q", keyholderStaleMarker, keyholder.BuildMarker)
	}
}
