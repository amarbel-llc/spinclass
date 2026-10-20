package confinement

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	stopUnit = "spinclass-agent-a.service"
	stopCG   = slice + "/" + stopUnit
	stopProc = "/sys/fs/cgroup" + stopCG + "/cgroup.procs"
)

// stopFixture is a host with one agent unit record for /wt/a and stubbed
// systemctl and file removal.
func stopFixture(t *testing.T, unit, cg string) (sys *fakeSys, stopped *[]string, removed *[]string) {
	t.Helper()
	prevStop, prevRm, prevWait, prevPoll := systemctlStop, removeFile, UnitStopWait, UnitStopPoll
	t.Cleanup(func() { systemctlStop, removeFile, UnitStopWait, UnitStopPoll = prevStop, prevRm, prevWait, prevPoll })
	UnitStopWait, UnitStopPoll = 50*time.Millisecond, time.Millisecond

	sys = &fakeSys{files: map[string]string{
		UnitRecordPath(stateDir, "/wt/a"):       record(unit, cg, 0, "", 0),
		"/sys/fs/cgroup" + cg + "/cgroup.procs": "100\n101\n",
	}}
	stopped, removed = new([]string), new([]string)
	systemctlStop = func(_ context.Context, u string) error {
		*stopped = append(*stopped, u)
		sys.files["/sys/fs/cgroup"+cg+"/cgroup.procs"] = "" // the unit's processes are gone
		return nil
	}
	removeFile = func(p string) error { *removed = append(*removed, p); return nil }
	return sys, stopped, removed
}

func TestStopUnitStopsWaitsAndRemovesTheRecords(t *testing.T) {
	sys, stopped, removed := stopFixture(t, stopUnit, stopCG)
	ok, err := StopUnit(context.Background(), sys, stateDir, "/wt/a")
	if err != nil || !ok {
		t.Fatalf("got (%v, %v), want stopped", ok, err)
	}
	if len(*stopped) != 1 || (*stopped)[0] != stopUnit {
		t.Errorf("systemctl stop called with %v, want [%s]", *stopped, stopUnit)
	}
	want := []string{UnitRecordPath(stateDir, "/wt/a"), UnitSpecPath(stateDir, "/wt/a")}
	if strings.Join(*removed, ",") != strings.Join(want, ",") {
		t.Errorf("removed %v, want %v", *removed, want)
	}
}

func TestStopUnitWithoutARecordIsANoOp(t *testing.T) {
	sys, stopped, removed := stopFixture(t, stopUnit, stopCG)
	ok, err := StopUnit(context.Background(), sys, stateDir, "/wt/never-confined")
	if err != nil || ok || len(*stopped) != 0 || len(*removed) != 0 {
		t.Errorf("got (%v, %v) stopped=%v removed=%v, want a quiet no-op", ok, err, *stopped, *removed)
	}
}

// The record is read from a file; a unit that is not ours must never reach
// systemctl, whatever the file says.
func TestStopUnitRefusesAUnitThatIsNotOurs(t *testing.T) {
	for name, tc := range map[string]struct{ unit, cg string }{
		"foreign unit":    {"dbus.service", slice + "/dbus.service"},
		"cgroup mismatch": {stopUnit, slice + "/spinclass-agent-other.service"},
		"slash in name":   {"spinclass-agent-../x.service", slice + "/spinclass-agent-../x.service"},
	} {
		t.Run(name, func(t *testing.T) {
			sys, stopped, _ := stopFixture(t, tc.unit, tc.cg)
			ok, err := StopUnit(context.Background(), sys, stateDir, "/wt/a")
			if err == nil || ok || len(*stopped) != 0 {
				t.Errorf("got (%v, %v) stopped=%v, want a refusal that stops nothing", ok, err, *stopped)
			}
		})
	}
}

func TestStopUnitFailsWhenTheCgroupStaysPopulated(t *testing.T) {
	sys, stopped, removed := stopFixture(t, stopUnit, stopCG)
	systemctlStop = func(_ context.Context, u string) error { *stopped = append(*stopped, u); return nil } // does not empty it
	ok, err := StopUnit(context.Background(), sys, stateDir, "/wt/a")
	if err == nil || ok || !strings.Contains(err.Error(), "still holds processes") {
		t.Errorf("got (%v, %v), want a cgroup-still-populated failure", ok, err)
	}
	if len(*removed) != 0 {
		t.Errorf("records removed although the unit is still running: %v", *removed)
	}
}

func TestStopUnitSucceedsWhenStopFailsButTheUnitIsAlreadyGone(t *testing.T) {
	sys, _, removed := stopFixture(t, stopUnit, stopCG)
	sys.files[stopProc] = ""
	systemctlStop = func(context.Context, string) error { return errors.New("Unit not loaded") }
	ok, err := StopUnit(context.Background(), sys, stateDir, "/wt/a")
	if err != nil || !ok || len(*removed) != 2 {
		t.Errorf("got (%v, %v) removed=%v, want success for an already-gone unit", ok, err, *removed)
	}
}

func TestStopUnitReportsAStopFailureWhileProcessesRemain(t *testing.T) {
	sys, _, _ := stopFixture(t, stopUnit, stopCG)
	systemctlStop = func(context.Context, string) error { return errors.New("Access denied") }
	if ok, err := StopUnit(context.Background(), sys, stateDir, "/wt/a"); err == nil || ok {
		t.Errorf("got (%v, %v), want the systemctl failure", ok, err)
	}
}
