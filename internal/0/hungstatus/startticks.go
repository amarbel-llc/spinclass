package hungstatus

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// StartTicks is field 22 (starttime) of /proc/<pid>/stat under procRoot: the
// number a process writes about itself so a reader can tell it from a later
// process that reused its pid (FrontendState compares against it). ok is false
// when the pid is not positive or its stat cannot be read or parsed.
func StartTicks(procRoot string, pid int) (ticks uint64, ok bool) {
	if pid <= 0 {
		return 0, false
	}
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(s[i+1:]) // fields[0] is field 3 (state)
	if len(fields) < 20 {
		return 0, false
	}
	got, err := strconv.ParseUint(fields[19], 10, 64) // field 22
	if err != nil {
		return 0, false
	}
	return got, true
}
