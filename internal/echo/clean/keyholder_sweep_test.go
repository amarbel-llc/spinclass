package clean

import (
	"errors"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/bravo/keyholdersweep"
)

func khOrphan(hash string) keyholdersweep.Pair {
	return keyholdersweep.Pair{Found: keyholder.Found{Hash: hash}, Orphan: true, Why: "test"}
}

// stubKeyholderSweep installs the sweep seams for one test and returns the list
// of hashes stopped.
func stubKeyholderSweep(t *testing.T, orphans []keyholdersweep.Pair, failFor string) *[]string {
	t.Helper()
	pf, ps := findKeyholderOrphans, stopKeyholderOrphan
	t.Cleanup(func() { findKeyholderOrphans, stopKeyholderOrphan = pf, ps })
	var stopped []string
	findKeyholderOrphans = func() []keyholdersweep.Pair { return orphans }
	stopKeyholderOrphan = func(p keyholdersweep.Pair) error {
		if p.Hash == failFor {
			return errors.New("a unit will not stop")
		}
		stopped = append(stopped, p.Hash)
		return nil
	}
	return &stopped
}

// `sc clean --yes` stops the orphaned key holder pairs: pairs whose session is
// gone and that nothing else will ever close.
func TestCleanStopsOrphanedKeyHolderPairs(t *testing.T) {
	_, repoDir := setupRepo(t)
	stopped := stubKeyholderSweep(t, []keyholdersweep.Pair{khOrphan("0123456789abcdef"), khOrphan("fedcba9876543210")}, "")

	out := captureRun(t, repoDir, false, false, true, "tap")

	if len(*stopped) != 2 {
		t.Errorf("stopped %v, want both orphans", *stopped)
	}
	if !strings.Contains(out, "stopped 2 orphaned key holder pair(s)") {
		t.Errorf("the TAP output does not report the stops:\n%s", out)
	}
}

// --dry-run plans the stop and stops nothing.
func TestCleanDryRunPlansButStopsNoKeyHolderPair(t *testing.T) {
	_, repoDir := setupRepo(t)
	stopped := stubKeyholderSweep(t, []keyholdersweep.Pair{khOrphan("0123456789abcdef")}, "")

	out := captureRun(t, repoDir, false, true, false, "tap")

	if len(*stopped) != 0 {
		t.Errorf("--dry-run stopped %v", *stopped)
	}
	if !strings.Contains(out, "stop 1 orphaned key holder pair(s)") {
		t.Errorf("the dry run does not plan the stop:\n%s", out)
	}
}

// A pair that will not stop is reported and does not hold up the others.
func TestCleanReportsAnOrphanedPairThatWillNotStopAndStopsTheRest(t *testing.T) {
	_, repoDir := setupRepo(t)
	stopped := stubKeyholderSweep(t, []keyholdersweep.Pair{khOrphan("0123456789abcdef"), khOrphan("fedcba9876543210")}, "0123456789abcdef")

	out := captureRun(t, repoDir, false, false, true, "tap")

	if len(*stopped) != 1 || (*stopped)[0] != "fedcba9876543210" {
		t.Errorf("stopped %v, want only the pair that can stop", *stopped)
	}
	if !strings.Contains(out, "not ok") || !strings.Contains(out, "0123456789abcdef") || !strings.Contains(out, "a unit will not stop") {
		t.Errorf("the failure is not reported:\n%s", out)
	}
}

// With no orphans clean does not touch the key holder at all.
func TestCleanWithNoOrphanedKeyHolderPairsStopsNone(t *testing.T) {
	_, repoDir := setupRepo(t)
	stopped := stubKeyholderSweep(t, nil, "")

	out := captureRun(t, repoDir, false, false, true, "tap")

	if len(*stopped) != 0 || strings.Contains(out, "key holder") {
		t.Errorf("stopped %v, output mentions the key holder:\n%s", *stopped, out)
	}
}
