package keyholder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listingFake is the fake runner that can also say which units exist.
type listingFake struct {
	*fakeRunner
	units []string
}

func (f listingFake) ListUnits(context.Context) ([]string, error) { return f.units, nil }

func sweepLauncher(t *testing.T, units ...string) (*Launcher, listingFake) {
	t.Helper()
	l, f, _ := launcher(t)
	lf := listingFake{fakeRunner: f, units: units}
	l.Run = lf
	return l, lf
}

// Find reports a live pair with its session, a directory that has no record, and
// the units of a pair whose directory is gone: the three shapes a sweep must
// tell apart.
func TestFindReportsALivePairADirectoryWithNoRecordAndUnitsWithNoDirectory(t *testing.T) {
	ghost := "0123456789abcdef"
	l, lf := sweepLauncher(t, "spinclass-keyholder-agent-"+ghost+".service", "spinclass-keyholder-fibby-"+ghost+".service", "unrelated.service")
	lf.active["spinclass-keyholder-agent-"+ghost+".service"] = true
	if _, err := l.Start(context.Background(), "s/live"); err != nil {
		t.Fatal(err)
	}
	liveHash := Hash("s/live")
	lf.units = append(lf.units, "spinclass-keyholder-agent-"+liveHash+".service", "spinclass-keyholder-fibby-"+liveHash+".service")
	l.Run = lf
	norec := "fedcba9876543210"
	if err := os.Mkdir(filepath.Join(l.Base, norec), 0o700); err != nil {
		t.Fatal(err)
	}
	found, err := l.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Found{}
	for _, f := range found {
		by[f.Hash] = f
	}
	if len(by) != 3 {
		t.Fatalf("found %d pairs, want 3 (live, no record, units only): %+v", len(by), found)
	}
	if got := by[liveHash]; got.Session != "s/live" || !got.Running || got.Dir == "" || len(got.Units) != 2 {
		t.Errorf("live pair = %+v", got)
	}
	if got := by[norec]; got.Session != "" || got.Dir == "" || got.Running {
		t.Errorf("directory with no record = %+v", got)
	}
	if got := by[ghost]; got.Dir != "" || len(got.Units) != 2 || !got.Running || got.Session != "" {
		t.Errorf("units with no directory = %+v", got)
	}
}

// StopFound stops the units of a pair whose directory is gone, by name, and a
// live pair's directory goes only after both units are inactive.
func TestStopFoundStopsUnitsByNameWithoutADirectoryAndRemovesALivePairLast(t *testing.T) {
	ghost := "0123456789abcdef"
	l, lf := sweepLauncher(t)
	ga, gf := "spinclass-keyholder-agent-"+ghost+".service", "spinclass-keyholder-fibby-"+ghost+".service"
	lf.active[ga], lf.active[gf] = true, true
	if err := l.StopFound(context.Background(), Found{Hash: ghost, Units: []string{ga, gf}}); err != nil {
		t.Fatal(err)
	}
	if lf.active[ga] || lf.active[gf] {
		t.Error("a unit with no directory was left running")
	}
	if lf.count("stop "+ga) != 1 || lf.count("stop "+gf) != 1 {
		t.Errorf("calls = %v, want one stop of each unit by name", lf.calls)
	}

	if _, err := l.Start(context.Background(), "s/live"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Base, Hash("s/live"))
	if err := l.StopFound(context.Background(), Found{Hash: Hash("s/live"), Session: "s/live", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the live pair's directory survived StopFound: %v", err)
	}
	if lf.active["spinclass-keyholder-agent-"+Hash("s/live")+".service"] {
		t.Error("the live pair's holder is still running")
	}
}

// A unit that will not stop leaves the directory in place and is an error.
func TestStopFoundKeepsTheDirectoryWhenAUnitWillNotStop(t *testing.T) {
	l, lf := sweepLauncher(t)
	if _, err := l.Start(context.Background(), "s/stuck"); err != nil {
		t.Fatal(err)
	}
	lf.stopStuck["spinclass-keyholder-agent-"+Hash("s/stuck")+".service"] = true
	dir := filepath.Join(l.Base, Hash("s/stuck"))
	err := l.StopFound(context.Background(), Found{Hash: Hash("s/stuck"), Session: "s/stuck", Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("StopFound = %v, want 'still running'", err)
	}
	if _, serr := os.Lstat(dir); serr != nil {
		t.Errorf("the directory was removed under a running unit: %v", serr)
	}
}

// A hash that is not a pair hash is refused before anything is touched, so a
// crafted Found cannot name a path.
func TestStopFoundRefusesAHashThatIsNotAPairHash(t *testing.T) {
	l, lf := sweepLauncher(t)
	for _, h := range []string{"", "../etc", "ABCDEF0123456789", "0123456789abcde"} {
		if err := l.StopFound(context.Background(), Found{Hash: h}); err == nil {
			t.Errorf("StopFound(%q) = nil, want a refusal", h)
		}
	}
	if len(lf.calls) != 0 {
		t.Errorf("a refused hash still ran %v", lf.calls)
	}
}
