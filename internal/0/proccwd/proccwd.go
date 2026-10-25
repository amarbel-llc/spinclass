// Package proccwd finds live processes whose working directory lies inside a
// directory, so a sweep can leave a directory some process is still in rather
// than delete it from under that process (#350).
package proccwd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Inside returns the pids of processes whose current working directory is dir
// or below it, ascending. Processes whose cwd cannot be read (another user's,
// or gone mid-scan) are skipped, so the answer covers what this user can see.
// It is empty for a missing dir and on hosts without /proc.
func Inside(dir string) []int {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil {
			continue
		}
		if cwd == real || strings.HasPrefix(cwd, real+string(filepath.Separator)) {
			pids = append(pids, pid)
		}
	}
	slices.Sort(pids)
	return pids
}

// InUseError reports a directory left in place because live processes are in it.
type InUseError struct {
	Dir  string
	Pids []int
}

func (e *InUseError) Error() string {
	ids := make([]string, len(e.Pids))
	for i, p := range e.Pids {
		ids[i] = strconv.Itoa(p)
	}
	return fmt.Sprintf("%s is the working directory of live process(es) %s; left in place", e.Dir, strings.Join(ids, ", "))
}

// RefuseIfInUse returns an *InUseError when a live process is inside dir.
func RefuseIfInUse(dir string) error {
	if pids := Inside(dir); len(pids) > 0 {
		return &InUseError{Dir: dir, Pids: pids}
	}
	return nil
}
