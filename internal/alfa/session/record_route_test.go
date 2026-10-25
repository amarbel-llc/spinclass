package session

import (
	"io"
	"os"
	"strings"
	"testing"
)

// #405: while a renderer owns the output, RouteReports takes the default sink's
// printed line instead of stderr; restore puts stderr back.
func TestRouteReportsTakesTheLineInsteadOfStderr(t *testing.T) {
	w1, _ := twoSessions(t)
	forged := w1
	forged.Principal = "route-forged-principal"
	forgeDisplayCopy(t, w1.WorktreePath, forged)

	var routed []string
	restore := RouteReports(func(line string) { routed = append(routed, line) })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	_, readErr := Read(w1.RepoPath, w1.Branch)
	os.Stderr = stderr
	restore()
	_ = w.Close()
	printed, _ := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}

	if len(printed) != 0 {
		t.Errorf("a routed report still reached stderr: %q", printed)
	}
	if len(routed) != 1 || !strings.HasPrefix(routed[0], "spinclass: ") || !strings.Contains(routed[0], "principal") {
		t.Errorf("want the one disagreement line routed, got %q", routed)
	}
}
