package confinement

import (
	"io/fs"
	"testing"
	"time"
)

// mtimeSys gives the unit record a modification time; the shared fake reports
// the zero time for everything.
type mtimeSys struct {
	*fakeSys
	path string
	at   time.Time
}

type timedInfo struct {
	fs.FileInfo
	at time.Time
}

func (t timedInfo) ModTime() time.Time { return t.at }

func (m mtimeSys) Lstat(path string) (fs.FileInfo, error) {
	fi, err := m.fakeSys.Lstat(path)
	if err == nil && path == m.path {
		return timedInfo{fi, m.at}, nil
	}
	return fi, err
}

func TestUnitClockReadsTheRecordMtimeAsTheStart(t *testing.T) {
	sys, _ := confinedHost()
	start := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ms := mtimeSys{fakeSys: sys, path: UnitRecordPath(stateDir, "/wt/b"), at: start}

	c, ok := unitClock(ms, stateDir, "/wt/b")
	if !ok || !c.Started.Equal(start) || c.Exited {
		t.Fatalf("clock = %+v ok=%v, want a running unit started %v", c, ok, start)
	}
	if got, want := c.Remaining(start.Add(5*24*time.Hour)), 2*24*time.Hour; got != want {
		t.Errorf("Remaining after 5 days = %v, want %v", got, want)
	}
	if got := c.Remaining(start.Add(8 * 24 * time.Hour)); got >= 0 {
		t.Errorf("Remaining after 8 days = %v, want negative", got)
	}
}

func TestUnitClockReportsAnExitedUnit(t *testing.T) {
	sys, _ := confinedHost()
	path := UnitRecordPath(stateDir, "/wt/b")
	sys.files[path] = `{"version":1,"unit":"agent-b.service","cgroup":"` + otherCG + `","exit_code":1}`
	c, ok := unitClock(mtimeSys{fakeSys: sys, path: path, at: time.Unix(1000, 0)}, stateDir, "/wt/b")
	if !ok || !c.Exited || c.ExitCode != 1 {
		t.Fatalf("clock = %+v ok=%v, want an exited unit with code 1", c, ok)
	}
}

func TestUnitClockOfAnUnconfinedSessionIsNotOK(t *testing.T) {
	sys, _ := confinedHost()
	if _, ok := unitClock(sys, stateDir, "/wt/none"); ok {
		t.Error("no unit record: want not ok")
	}
}
