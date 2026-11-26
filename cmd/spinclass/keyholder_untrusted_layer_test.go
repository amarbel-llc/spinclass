package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Only a layer that COULD say "on" (a trusted one: the global file and the
// directories above the repo) refuses when it exists and cannot be loaded. The
// driver repo's OWN main-checkout sweatfile is an untrusted layer: it could never
// have switched the key holder on, so its failure must not refuse a launch driven
// from that repo (follow-up review F1).

const khBroken = "this is = not [valid toml"

func TestAnUnparseableDriverRepoOwnSweatfileDoesNotRefuseTheLaunch(t *testing.T) {
	warn := khEvidence(t, false)
	home, workRepo, engRepo := khTree(t, "", "", "")
	if err := os.WriteFile(filepath.Join(engRepo, "sweatfile"), []byte(khBroken), 0o644); err != nil {
		t.Fatal(err)
	}
	khDriverIs(t, engRepo)
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil {
		t.Fatalf("the driver repo's own untrusted sweatfile failed the switch: %v", err)
	}
	if sw.On {
		t.Errorf("switch = %+v, want the switch-off result", sw)
	}
	if warn.Len() != 0 {
		t.Errorf("an ignored untrusted layer warned: %q", warn.String())
	}
}

// With the working directory in no repo the driver is the home directory and
// ~/sweatfile is its "repo" layer: untrusted (the global file is
// ~/.config/spinclass/sweatfile, and chainAncestors never emits the home itself),
// so it is never read for the switch.
func TestAnUnparseableHomeSweatfileDoesNotRefuseWhenTheWorkingDirectoryIsInNoRepo(t *testing.T) {
	warn := khEvidence(t, false)
	home, workRepo, _ := khTree(t, "", "", "")
	if err := os.WriteFile(filepath.Join(home, "sweatfile"), []byte(khBroken), 0o644); err != nil {
		t.Fatal(err)
	}
	// On a real host $HOME is the passwd home: the passwd-home pass is skipped, and
	// the home directory is never emitted as an ancestor layer.
	khPasswdHomeIs(t, home, nil)
	khDriverIs(t, home)
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil {
		t.Fatalf("~/sweatfile (the driver's untrusted repo layer) failed the switch: %v", err)
	}
	if sw.On {
		t.Errorf("switch = %+v, want the switch-off result", sw)
	}
	if warn.Len() != 0 {
		t.Errorf("an ignored untrusted layer warned: %q", warn.String())
	}
}

// The same broken file one directory up is a trusted layer (a strict ancestor of
// the driver repo) and still refuses: the rule narrows to what could say "on", it
// does not drop the check.
func TestAnUnparseableStrictAncestorOfTheDriverRepoStillRefuses(t *testing.T) {
	khEvidence(t, false)
	home, workRepo, engRepo := khTree(t, "", "", khBroken)
	khDriverIs(t, engRepo)
	if _, err := resolveKeyholderSwitch(home, workRepo); err == nil {
		t.Fatal("an unparseable strict ancestor of the driver repo did not refuse")
	}
}

// An unparseable GLOBAL file under the account's home refuses too, with the repo
// layer left out of that load.
func TestAnUnparseableGlobalFileUnderThePasswdHomeStillRefusesWhenTheRepoLayerIsLeftOut(t *testing.T) {
	khEvidence(t, false)
	passwd := t.TempDir()
	khGlobalAt(t, passwd, khBroken)
	khPasswdHomeIs(t, passwd, nil)
	home, workRepo, _ := khTree(t, "", "", "")
	if _, err := resolveKeyholderSwitch(home, workRepo); err == nil {
		t.Fatal("an unparseable global file under the passwd home did not refuse")
	}
}
