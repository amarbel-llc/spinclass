package confinement

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Seams for tests; production values are the real host.
var (
	// systemctlStop stops a user-manager unit by name.
	systemctlStop = func(ctx context.Context, unit string) error {
		out, err := exec.CommandContext(ctx, "systemctl", "--user", "stop", "--", unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl --user stop %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	removeFile = os.Remove

	// UnitStopWait bounds how long StopUnit waits for the unit's cgroup to
	// empty after the stop; UnitStopPoll is the interval.
	UnitStopWait = 15 * time.Second
	UnitStopPoll = 100 * time.Millisecond
)

// unitNamePrefix is the prefix spinclass gives every agent unit it asks clown to
// start (spawn.buildUnitSpec's unit_name_hint); clown may append a suffix.
const unitNamePrefix = "spinclass-agent-"

// StopUnit ends the agent unit of the session whose worktree is worktree, so a
// closed confined worker does not keep running with its worktree gone from
// under it. It reads the unit name from the record clown wrote, stops it
// (clown's frontend exits and removes the agent dir on its own once the unit
// is gone, confirmed by the operator), waits for the cgroup to empty, then
// removes the record and the launch spec. A worktree with no record was never
// launched confined: stopped is false and err nil. A record naming a unit that
// is not one of ours is refused rather than stopped.
func StopUnit(ctx context.Context, sys System, stateDir, worktree string) (stopped bool, err error) {
	rec, lerr := LoadUnitRecord(sys, stateDir, worktree)
	if lerr != nil {
		if isMissingRecord(lerr) {
			return false, nil
		}
		return false, fmt.Errorf("reading the agent unit record: %w", lerr)
	}
	if !strings.HasPrefix(rec.Unit, unitNamePrefix) || !strings.HasSuffix(rec.Unit, ".service") || strings.ContainsAny(rec.Unit, "/ \n") {
		return false, fmt.Errorf("the record names unit %q, which is not a spinclass agent unit; not stopping it", rec.Unit)
	}
	if !strings.HasSuffix(rec.Cgroup, "/"+rec.Unit) {
		return false, fmt.Errorf("the record's cgroup %s does not belong to unit %s; not stopping it", rec.Cgroup, rec.Unit)
	}
	if err := systemctlStop(ctx, rec.Unit); err != nil {
		// Already gone (the worker exited on its own) is the success we wanted.
		if !cgroupHasProcesses(sys, rec.Cgroup) {
			removeUnitFiles(stateDir, worktree)
			return true, nil
		}
		return false, err
	}
	deadline := time.Now().Add(UnitStopWait)
	for cgroupHasProcesses(sys, rec.Cgroup) {
		if !time.Now().Before(deadline) {
			return false, fmt.Errorf("unit %s was told to stop but its cgroup still holds processes after %s", rec.Unit, UnitStopWait)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(UnitStopPoll):
		}
	}
	removeUnitFiles(stateDir, worktree)
	return true, nil
}

// cgroupHasProcesses reports whether the unit's cgroup still lists a process.
// A cgroup directory that is gone is empty; an unreadable one is treated as
// still populated, so a stop is not declared done on a guess.
func cgroupHasProcesses(sys System, cgroup string) bool {
	b, err := sys.ReadFile("/sys/fs/cgroup" + cgroup + "/cgroup.procs")
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	return strings.TrimSpace(string(b)) != ""
}

func removeUnitFiles(stateDir, worktree string) {
	_ = removeFile(UnitRecordPath(stateDir, worktree))
	_ = removeFile(UnitSpecPath(stateDir, worktree))
}

// isMissingRecord reports whether LoadUnitRecord failed only because no record
// exists (a session never launched confined), as opposed to a malformed one.
func isMissingRecord(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
