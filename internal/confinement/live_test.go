//go:build linux

package confinement

import (
	"os"
	"testing"
)

// TestLiveHost runs the real probes against this host for the session named
// by SPINCLASS_SESSION_ID and logs the table. It asserts only that every fact
// was probed: on every host today most facts fail (no agent unit yet), and
// that is the expected state, not a test failure. Opt-in, since a build
// sandbox has no sessions, cgroups or agent to look at:
//
//	SPINCLASS_CONFINEMENT_LIVE=1 go test -run TestLiveHost -v ./internal/confinement
//
// `just debug-confinement-check` prints the same table from the built binary.
func TestLiveHost(t *testing.T) {
	if os.Getenv("SPINCLASS_CONFINEMENT_LIVE") != "1" {
		t.Skip("set SPINCLASS_CONFINEMENT_LIVE=1 to probe this host")
	}
	in, err := InputsForDriver(os.Getenv("SPINCLASS_SESSION_ID"), os.Getenv("CLOWN_SESSION_ID"))
	if err != nil {
		t.Fatalf("assembling inputs: %v", err)
	}
	r := Run(Host{}, in)
	if len(r.Facts) != 16 {
		t.Fatalf("probed %d facts, want 16", len(r.Facts))
	}
	for _, f := range r.Facts {
		status := "pass"
		if !f.OK {
			status = "FAIL"
		}
		t.Logf("%-4s %-3s %s: %s", status, f.ID, f.Name, f.Detail)
	}
	t.Logf("overall: holds=%v", r.OK())
}
