package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeToolsOnPath puts executable files named fibby and piggy on PATH, the
// thing the launcher must never resolve a program from.
func fakeToolsOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"fibby", "piggy"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if p, err := exec.LookPath("fibby"); err != nil || filepath.Dir(p) != dir {
		t.Skipf("the temp dir cannot hold runnable files here (LookPath fibby = %q, %v): this test would prove nothing", p, err)
	}
	return dir
}

func setPins(t *testing.T, fibby, piggy string) {
	t.Helper()
	pf, pp := fibbyBin, piggyBin
	t.Cleanup(func() { fibbyBin, piggyBin = pf, pp })
	fibbyBin, piggyBin = fibby, piggy
}

// An unpinned build refuses to start a pair even when both programs are on
// PATH: the pair holds a session's key and runs outside the unit, so a name
// resolved from a PATH a session can influence must never be exec'd.
func TestUnpinnedBuildRefusesToStartAPairEvenWithBothProgramsOnPATH(t *testing.T) {
	fakeToolsOnPath(t)
	setPins(t, "", "")
	l, err := realKeyholderLauncher()
	if err == nil || l != nil {
		t.Fatalf("an unpinned build produced a launcher (%v, %v), want a refusal", l, err)
	}
	if !strings.Contains(err.Error(), "fibby is not pinned") || !strings.Contains(err.Error(), "PATH") {
		t.Errorf("the refusal does not name the reason: %v", err)
	}
}

func TestHalfPinnedBuildNamesTheMissingProgram(t *testing.T) {
	fakeToolsOnPath(t)
	setPins(t, "/nix/store/x-fibby/bin/fibby", "")
	if _, err := realKeyholderLauncher(); err == nil || !strings.Contains(err.Error(), "piggy is not pinned") {
		t.Fatalf("got %v, want the refusal to name piggy", err)
	}
}

func TestARelativePinIsRefused(t *testing.T) {
	setPins(t, "bin/fibby", "/nix/store/x-piggy/bin/piggy")
	if _, err := realKeyholderLauncher(); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("got %v, want a refusal of the relative pin", err)
	}
}

func TestAPinnedBuildExecsExactlyThePinnedStorePaths(t *testing.T) {
	fakeToolsOnPath(t)
	setPins(t, "/nix/store/x-fibby/bin/fibby", "/nix/store/x-piggy/bin/piggy")
	l, err := realKeyholderLauncher()
	if err != nil {
		t.Fatal(err)
	}
	if l.Tools.Fibby != "/nix/store/x-fibby/bin/fibby" || l.Tools.Piggy != "/nix/store/x-piggy/bin/piggy" {
		t.Errorf("tools = %+v, want the pinned store paths and nothing from PATH", l.Tools)
	}
}

// The hidden debugging command may name its own absolute paths: a person at a
// terminal chose them. An unpinned build still refuses without them.
func TestKeyholderPairCommandNamesItsOwnProgramsOnlyWhenGivenBoth(t *testing.T) {
	setPins(t, "", "")
	if _, err := launcherForTools("/opt/fibby", "/opt/piggy"); err != nil {
		t.Fatalf("explicit absolute paths were refused: %v", err)
	}
	if _, err := launcherForTools("/opt/fibby", ""); err == nil {
		t.Error("a launcher was built with only one program named")
	}
}
