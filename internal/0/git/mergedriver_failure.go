package git

import (
	"fmt"
	"strings"
)

// ExplainDriverNotFound puts a merge driver's shell not-found report from a
// failed rebase's output into the error (spinclass#334). The pre-flight (#324)
// catches an absent driver it can judge, but not one it cannot or one that
// vanishes before the rebase. The resulting "conflict" leaves the ours side
// with no markers, and resolving it by staging the file drops the other side.
// err is returned unchanged when out names no such failure.
func ExplainDriverNotFound(err error, out string) error {
	lines := DriverNotFoundLines(out)
	if len(lines) == 0 {
		return err
	}
	return fmt.Errorf("%w; a merge driver did not run (%s): the rebase's conflict is not a content conflict, the path holds the \"ours\" side with no conflict markers, so staging it as resolved drops the other side. Abort the rebase, put the driver on PATH, and re-run",
		err, strings.Join(lines, "; "))
}

// DriverNotFoundLines returns the lines of a rebase's combined output in
// which the shell git ran a merge driver through reported the command as not
// found (spinclass#334): "<shell>: [line 1: ]<cmd>: command not found" (bash)
// or "<shell>: 1: <cmd>: not found" (dash). The conflict such a rebase stops
// on is not a content conflict: git kept the ours side with no markers.
// Lines are split on CR as well as LF, so git's progress output does not hide
// one.
func DriverNotFoundLines(out string) []string {
	var found []string
	for _, line := range strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ": command not found") || strings.HasSuffix(line, ": not found") {
			found = append(found, line)
		}
	}
	return found
}
