package confinement

import (
	"strings"
	"testing"
)

// spinclass#483 slice 4: the host admission queue's state (hold, journal, cost
// history) must never be reachable from a unit, and neither may its runtime
// ledger. The state half is a protected state dir (F16); the runtime half is
// kept out by F14's allowlist of the operator's runtime dir.

func TestHostQueueStateIsAProtectedStateDir(t *testing.T) {
	const state = "/home/op/.local/state"
	for _, bind := range []string{
		state + "/spinclass/host-queue",
		state + "/spinclass/host-queue/journal.ndjson",
		state + "/spinclass/host-queue/hold",
		state + "/spinclass",
		state,
	} {
		if err := RefuseProtectedBinds(state, []string{bind}); err == nil {
			t.Errorf("bind %s was not refused", bind)
		}
	}
	for _, bind := range []string{
		state + "/spinclass/units",
		state + "/spinclass/host-queue-other",
	} {
		if err := RefuseProtectedBinds(state, []string{bind}); err != nil {
			t.Errorf("bind %s refused: %v", bind, err)
		}
	}
}

func TestF16FailsWhenASpecBindsTheHostQueueState(t *testing.T) {
	for name, extra := range map[string]string{
		"the queue dir": stateDir + "/spinclass/host-queue",
		"its journal":   stateDir + "/spinclass/host-queue/journal.ndjson",
	} {
		sys, in := confinedHost()
		sys.files[UnitSpecPath(stateDir, "/wt/a")] = unitSpecWith("/wt/a", extra)
		f := factByID(t, Run(sys, in), "F16")
		if f.OK || !strings.Contains(f.Detail, extra) || !strings.Contains(f.Detail, stateDir+"/spinclass/host-queue") {
			t.Errorf("%s: F16 = %+v, want a failure naming the bind and the protected dir", name, f)
		}
	}
}

func TestF16FailsWhenTheUnitCanSeeTheHostQueueState(t *testing.T) {
	sys, in := confinedHost()
	sys.dirs[root+stateDir+"/spinclass/host-queue"] = []string{"journal.ndjson", "hold"}
	if f := factByID(t, Run(sys, in), "F16"); f.OK {
		t.Errorf("F16 = %+v, want a failure: the unit lists the queue state", f)
	}
}

func TestF14FailsWhenTheUnitCanSeeTheRuntimeLedger(t *testing.T) {
	sys, in := confinedHost()
	sys.dirs[root+"/run"] = []string{"user"}
	sys.dirs[root+"/run/user"] = []string{"1000"}
	sys.dirs[root+"/run/user/1000"] = []string{"clown", "spinclass"}
	f := factByID(t, Run(sys, in), "F14")
	if f.OK || !strings.Contains(f.Detail, "spinclass") {
		t.Errorf("F14 = %+v, want a failure naming spinclass in the runtime dir", f)
	}
}
