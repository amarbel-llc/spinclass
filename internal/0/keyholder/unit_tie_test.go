package keyholder

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// B-M2: the holder's unit is bound to fibby's and ordered after it, so fibby going
// away stops the agent too and a half-dead pair (an agent that lists the key but
// can no longer sign) cannot exist. Fibby itself is tied to nothing.
func TestTheAgentUnitIsBoundToFibbyAndStartsAfterIt(t *testing.T) {
	l, f, _ := launcher(t)
	if _, err := l.Start(context.Background(), "s/tied"); err != nil {
		t.Fatal(err)
	}
	fibby := "spinclass-keyholder-fibby-" + Hash("s/tied") + ".service"
	agent := "spinclass-keyholder-agent-" + Hash("s/tied") + ".service"
	f.mu.Lock()
	defer f.mu.Unlock()
	got := strings.Join(f.props[agent], " ")
	for _, want := range []string{"BindsTo=" + fibby, "After=" + fibby} {
		if !strings.Contains(got, want) {
			t.Errorf("the agent unit's properties = %q, want %q", got, want)
		}
	}
	if len(f.props[fibby]) != 0 {
		t.Errorf("fibby's unit was started with properties %v, want none", f.props[fibby])
	}
}

// The properties reach systemd-run as --property arguments, after the fixed ones.
func TestSystemdRunnerPassesUnitPropertiesToSystemdRun(t *testing.T) {
	pinned := t.TempDir()
	log := filepath.Join(t.TempDir(), "pinned.log")
	run := fakeTool(t, pinned, "systemd-run", log)
	ctl := fakeTool(t, pinned, "systemctl", log)
	defer OverrideSystemdPins(run, ctl)()
	err := SystemdRunner{}.Start(context.Background(), "spinclass-keyholder-agent-x", []string{"/nix/store/x/bin/piggy", "agent"}, nil, "BindsTo=spinclass-keyholder-fibby-x.service", "After=spinclass-keyholder-fibby-x.service")
	if err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{"--property=BindsTo=spinclass-keyholder-fibby-x.service", "--property=After=spinclass-keyholder-fibby-x.service"} {
		if !strings.Contains(got, want) {
			t.Errorf("systemd-run never saw %q; log:\n%s", want, got)
		}
	}
}
