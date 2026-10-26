package confinement

import "time"

// UnitWallClock is the wall clock clown gives an UNATTENDED agent unit
// (RuntimeMaxSec=604800, clown internal/agentscope/unit.go). Spinclass cannot see
// whether a given unit was launched unattended, so the clock assumes every
// confined unit is (the 2026-10-26 trip: they all are).
const UnitWallClock = 7 * 24 * time.Hour

// UnitClock is what spinclass can read about a confined session's unit clock.
// Started is the modification time of the unit record: clown writes the record
// once the unit is active and rewrites it only when the unit exits, so while the
// unit runs the mtime is its start (to within the seconds clown takes between
// the unit going active and the provider starting), and a restart is a new launch
// that rewrites it. Once Exited, Started is the exit time, not the start. R/I;
// not verified against a live unit.
type UnitClock struct {
	Started  time.Time
	Exited   bool
	ExitCode int
}

// UnitClockForWorktree reads the clock of the worktree's unit; ok is false when
// the session was not launched confined or its record does not load.
func UnitClockForWorktree(worktree string) (UnitClock, bool) {
	stateDir, err := DefaultStateDir()
	if err != nil {
		return UnitClock{}, false
	}
	return unitClock(Host{}, stateDir, worktree)
}

func unitClock(sys System, stateDir, worktree string) (UnitClock, bool) {
	rec, err := LoadUnitRecord(sys, stateDir, worktree)
	if err != nil {
		return UnitClock{}, false
	}
	fi, err := sys.Lstat(UnitRecordPath(stateDir, worktree))
	if err != nil {
		return UnitClock{}, false
	}
	c := UnitClock{Started: fi.ModTime().UTC()}
	if rec.ExitCode != nil {
		c.Exited, c.ExitCode = true, *rec.ExitCode
	}
	return c, true
}

// Remaining is how much of the wall clock is left at now; negative once past it.
func (c UnitClock) Remaining(now time.Time) time.Duration {
	return UnitWallClock - now.Sub(c.Started)
}
