package git

import "strings"

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
