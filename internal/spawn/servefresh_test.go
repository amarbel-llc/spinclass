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
	t.Cleanup(func() { runningExecutable, lookPathSpinclass = oldRun, oldLook })
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
