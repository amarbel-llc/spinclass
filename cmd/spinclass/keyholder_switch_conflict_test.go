package main

import (
	"strings"
	"testing"
)

// M-1 of the switch review: with the mode unset in every layer and the switch
// file saying on, a caller-chosen target repo under a directory whose sweatfile
// names ANOTHER fibby than the driver's context must be refused, exactly as it is
// when a layer says on. The disagreement is kept while the switch is off and
// returned wherever it turns on.
func TestSwitchFileOnRefusesATargetAndDriverThatDisagreeOnAProgram(t *testing.T) {
	const (
		targetLayer = "[keyholder]\nfibby = \"/nix/store/ttt-fibby/bin/fibby\"\npiggy = \"/nix/store/bbb-piggy/bin/piggy\"\n"
		driverLayer = "[keyholder]\nfibby = \"/nix/store/ddd-fibby/bin/fibby\"\npiggy = \"/nix/store/bbb-piggy/bin/piggy\"\n"
	)
	home, workRepo, engRepo := khTree(t, "", targetLayer, driverLayer)
	khDriverIs(t, engRepo)
	passwd := t.TempDir()
	khPasswdHomeIs(t, passwd, nil)

	// Control: no switch file and no mode anywhere: off, and no refusal (nothing
	// is on, so nothing to guard).
	if sw, err := resolveKeyholderSwitch(home, workRepo); err != nil || sw.On {
		t.Fatalf("control (no file, no mode) = (%+v, %v), want off with no refusal", sw, err)
	}

	khSwitchWord(t, passwd, "on\n")
	_, err := resolveKeyholderSwitch(home, workRepo)
	if err == nil || !strings.Contains(err.Error(), "fibby") || !strings.Contains(err.Error(), "may not depend on which directory the caller chose") {
		t.Fatalf("file on with a target/driver program disagreement = %v, want the program-mismatch refusal", err)
	}

	// off keeps the kill switch working: nothing is on, so the disagreement is not
	// a refusal (the operator's escape hatch must work).
	khSwitchWord(t, passwd, "off\n")
	if sw, err := resolveKeyholderSwitch(home, workRepo); err != nil || sw.On {
		t.Errorf("file off with the same disagreement = (%+v, %v), want off with no refusal", sw, err)
	}
}
