package spawn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubServeBinaries points the running binary and the PATH lookup at the given
// paths (an empty one fails its lookup).
func stubServeBinaries(t *testing.T, running, onPath string) {
	t.Helper()
	oldRun, oldLook := runningExecutable, lookPathSpinclass
	oldBuild, oldRead := runningBuild, readBuildIdentity
	t.Cleanup(func() {
		runningExecutable, lookPathSpinclass = oldRun, oldLook
		runningBuild, readBuildIdentity = oldBuild, oldRead
	})
	// By default the two builds differ, so a differing path is stale.
	stubBuilds(Build{"1.0", "aaaaaaa"}, Build{"1.0", "bbbbbbb"}, nil)
	runningExecutable = func() (string, error) {
		if running == "" {
			return "", errors.New("no /proc/self/exe")
		}
		return running, nil
	}
	lookPathSpinclass = func() (string, error) {
		if onPath == "" {
			return "", errors.New("not on PATH")
		}
		return onPath, nil
	}
}

// stubBuilds sets the running build and what reading the on-PATH binary yields.
func stubBuilds(running, onPath Build, readErr error) {
	runningBuild = func() Build { return running }
	readBuildIdentity = func(string) (Build, error) { return onPath, readErr }
}

// storeBinaries lays out two "store" binaries and a profile symlink to the
// first, as a nix profile does.
func storeBinaries(t *testing.T) (oldBin, newBin, profileLink string) {
	t.Helper()
	dir := t.TempDir()
	oldBin, newBin = filepath.Join(dir, "old-spinclass"), filepath.Join(dir, "new-spinclass")
	for _, p := range []string{oldBin, newBin} {
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	profileLink = filepath.Join(dir, "profile-spinclass")
	if err := os.Symlink(oldBin, profileLink); err != nil {
		t.Fatal(err)
	}
	return oldBin, newBin, profileLink
}

func TestServeFreshWhenThePathResolvesToTheRunningBinary(t *testing.T) {
	oldBin, _, profileLink := storeBinaries(t)
	stubServeBinaries(t, oldBin, profileLink) // a symlinked profile path is not a false positive
	if err := refuseStaleServe(); err != nil {
		t.Fatalf("got %v, want fresh", err)
	}
}

func TestServeSamePathNeverReadsTheBuild(t *testing.T) {
	oldBin, _, profileLink := storeBinaries(t)
	stubServeBinaries(t, oldBin, profileLink)
	readBuildIdentity = func(string) (Build, error) { t.Fatal("identity read for a same path"); return Build{}, nil }
	b, err := CurrentServeBinary()
	if err != nil || b.Stale() || b.SameBuildDifferentPath() {
		t.Fatalf("got %+v, %v, want fresh", b, err)
	}
}

func TestServeSameBuildDifferentPathIsNotStale(t *testing.T) {
	oldBin, newBin, _ := storeBinaries(t)
	stubServeBinaries(t, oldBin, newBin)
	stubBuilds(Build{"1.0", "abc1234"}, Build{"1.0", "abc1234"}, nil)
	b, err := CurrentServeBinary()
	if err != nil || b.Stale() || !b.SameBuildDifferentPath() {
		t.Fatalf("got %+v, %v, want same build, different path", b, err)
	}
	if err := refuseStaleServe(); err != nil {
		t.Fatalf("got %v, want the spawn allowed", err)
	}
}

func TestServeStaleWhenTheCommitsDiffer(t *testing.T) {
	oldBin, newBin, _ := storeBinaries(t)
	stubServeBinaries(t, oldBin, newBin)
	stubBuilds(Build{"1.0", "abc1234"}, Build{"1.0", "def5678"}, nil)
	err := refuseStaleServe()
	if err == nil {
		t.Fatal("want a stale refusal")
	}
	for _, want := range []string{"stale", oldBin, newBin, "abc1234", "def5678"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
}

func TestServeStaleWhenACommitIsUnknown(t *testing.T) {
	oldBin, newBin, _ := storeBinaries(t)
	for _, c := range []string{"unknown", "dev", ""} {
		for name, builds := range map[string][2]Build{
			"on PATH": {{"1.0", "abc1234"}, {"1.0", c}},
			"running": {{"1.0", c}, {"1.0", "abc1234"}},
			"both":    {{"1.0", c}, {"1.0", c}},
		} {
			t.Run(name+"/"+c, func(t *testing.T) {
				stubServeBinaries(t, oldBin, newBin)
				stubBuilds(builds[0], builds[1], nil)
				if err := refuseStaleServe(); err == nil || !strings.Contains(err.Error(), "stale") {
					t.Fatalf("got %v, want stale", err)
				}
			})
		}
	}
}

func TestServeStaleWhenTheOnPathIdentityCannotBeRead(t *testing.T) {
	oldBin, newBin, _ := storeBinaries(t)
	stubServeBinaries(t, oldBin, newBin)
	stubBuilds(Build{"1.0", "abc1234"}, Build{}, errors.New("timed out"))
	b, err := CurrentServeBinary()
	if err != nil || !b.Stale() || b.IdentityErr == nil {
		t.Fatalf("got %+v, %v, want stale with the read error", b, err)
	}
	err = refuseStaleServe()
	want := "cannot read the build identity of " + newBin + ": timed out"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseVersionTable(t *testing.T) {
	table := "COMPONENT                  VERSION        REV\n" +
		"spinclass-0.1.5/spinclass  0.1.5+ff06182  ff06182\n" +
		"madder                     -              dormant\n"
	got, err := parseVersionTable(table)
	if err != nil || got != (Build{"0.1.5", "ff06182"}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	for name, bad := range map[string]string{
		"empty":      "",
		"no row":     "COMPONENT VERSION REV\nmadder - dormant\n",
		"no plus":    "spinclass-1/spinclass 1 abc\n",
		"empty part": "spinclass-1/spinclass +abc abc\n",
	} {
		if _, err := parseVersionTable(bad); err == nil {
			t.Errorf("%s: want a parse error", name)
		}
	}
}

func TestServeStaleWhenThePathResolvesElsewhere(t *testing.T) {
	oldBin, newBin, _ := storeBinaries(t)
	stubServeBinaries(t, oldBin, newBin)
	err := refuseStaleServe()
	if err == nil || !strings.Contains(err.Error(), "stale") ||
		!strings.Contains(err.Error(), oldBin) || !strings.Contains(err.Error(), newBin) {
		t.Fatalf("got %v, want a stale refusal naming both binaries", err)
	}
}

// A collected running binary keeps its " (deleted)" marker, so it is stale.
func TestServeStaleWhenTheRunningBinaryWasDeleted(t *testing.T) {
	oldBin, _, _ := storeBinaries(t)
	stubServeBinaries(t, oldBin+" (deleted)", oldBin)
	if err := refuseStaleServe(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("got %v, want stale", err)
	}
}

func TestServeFreshnessUnresolvableRefuses(t *testing.T) {
	oldBin, _, _ := storeBinaries(t)
	for name, paths := range map[string][2]string{
		"no running binary":         {"", oldBin},
		"spinclass not on PATH":     {oldBin, ""},
		"PATH entry does not exist": {oldBin, filepath.Join(t.TempDir(), "gone")},
	} {
		t.Run(name, func(t *testing.T) {
			stubServeBinaries(t, paths[0], paths[1])
			if err := refuseStaleServe(); err == nil || !strings.Contains(err.Error(), "cannot tell") {
				t.Fatalf("got %v, want the unresolvable refusal", err)
			}
		})
	}
}
