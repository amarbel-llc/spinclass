package validate

import (
	"strings"
	"testing"
)

// A-M2: turning the switch on starts nothing for a session that already runs, and
// a restart brings it back on the operator's agent. `sc validate` says so, one
// line per such session, only while the switch is on.
func TestRunListsTheWorkersThatRunWithoutAHolderOnlyWhileTheSwitchIsOn(t *testing.T) {
	prev := KeyholderSessionNotes
	t.Cleanup(func() { KeyholderSessionNotes = prev })
	KeyholderSessionNotes = func() []string {
		return []string{"repo/old: holder: none, operator agent (launched before the switch)"}
	}
	out, _ := keyholderRun(t, "[keyholder]\nmode = \"on\"\n")
	if !strings.Contains(out, "key holder: repo/old: holder: none, operator agent") {
		t.Errorf("mode on: the worker with no holder is not listed:\n%s", out)
	}
	out, _ = keyholderRun(t, "")
	if strings.Contains(out, "repo/old") {
		t.Errorf("mode off: a session is listed as lacking a holder:\n%s", out)
	}
}
