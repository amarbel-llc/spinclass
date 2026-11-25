package main

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// A person at a terminal may name systemd-run and systemctl for a build that has
// no pins (a dev build); they must be absolute paths and reach the runner as
// given. Nothing here ever becomes a PATH lookup.
func TestKeyholderPairLauncherTakesAbsoluteSystemdPathsAndRefusesRelativeOnes(t *testing.T) {
	l, err := launcherForToolsAndSystemd("/opt/fibby", "/opt/piggy", "/opt/systemd/bin/systemd-run", "/opt/systemd/bin/systemctl")
	if err != nil {
		t.Fatalf("absolute paths refused: %v", err)
	}
	r, ok := l.Run.(keyholder.SystemdRunner)
	if !ok {
		t.Fatalf("the launcher's runner is %T, want the real SystemdRunner", l.Run)
	}
	if r.RunBin != "/opt/systemd/bin/systemd-run" || r.CtlBin != "/opt/systemd/bin/systemctl" {
		t.Errorf("runner = %+v, want the paths the person named", r)
	}
	for _, c := range []struct{ run, ctl string }{{"systemd-run", ""}, {"", "systemctl"}, {"./systemctl", ""}} {
		_, err := launcherForToolsAndSystemd("/opt/fibby", "/opt/piggy", c.run, c.ctl)
		if err == nil || !strings.Contains(err.Error(), "not an absolute path") {
			t.Errorf("systemd-run %q systemctl %q: err = %v, want a refusal naming an absolute path", c.run, c.ctl, err)
		}
	}
}

// The default launcher (a session launch, `sc clean`, `sc close`) uses the build's
// pins through an empty SystemdRunner, never a name on PATH.
func TestKeyholderSessionLauncherUsesThePinnedRunnerNotAPATHName(t *testing.T) {
	l := newKeyholderLauncher(keyholder.Tools{Fibby: "/opt/fibby", Piggy: "/opt/piggy"})
	r, ok := l.Run.(keyholder.SystemdRunner)
	if !ok || r.RunBin != "" || r.CtlBin != "" {
		t.Fatalf("session launcher runner = %#v, want an empty SystemdRunner that resolves through the pins", l.Run)
	}
	defer keyholder.OverrideSystemdPins("", "")()
	if err := r.Stop(t.Context(), "spinclass-keyholder-x"); err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Errorf("with no pins the runner ran or failed otherwise: %v, want the not-pinned refusal", err)
	}
}
