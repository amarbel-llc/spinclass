package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func khPasswdHomeIs(t *testing.T, dir string, err error) {
	t.Helper()
	prev := keyholderPasswdHomeFn
	keyholderPasswdHomeFn = func() (string, error) { return dir, err }
	t.Cleanup(func() { keyholderPasswdHomeFn = prev })
}

// khGlobalAt writes a global sweatfile under dir (a home).
func khGlobalAt(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, ".config", "spinclass", "sweatfile")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A process that starts with HOME=/somewhere/else is handed that directory as the
// home: its global sweatfile says nothing. The switch must still read the account's
// own (passwd) home, where the host wrote it.
func TestKeyholderSwitchReadsThePasswdHomeWhenTheCallerChoseAnotherHome(t *testing.T) {
	passwd := t.TempDir()
	khGlobalAt(t, passwd, khOn)
	khPasswdHomeIs(t, passwd, nil)
	callerHome, _, engRepo := khTree(t, "", "", "")
	sw, err := resolveKeyholderSwitch(callerHome, engRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !sw.On {
		t.Fatal("a caller that chose HOME read the switch as off while the passwd home's global file says on")
	}
}

// The program paths the pair runs come from the passwd home's layers when they
// set them: a global file the caller chose cannot swap them.
func TestKeyholderSwitchTakesTheProgramsFromThePasswdHomeNotTheCallersHome(t *testing.T) {
	passwd := t.TempDir()
	khGlobalAt(t, passwd, khOn)
	khPasswdHomeIs(t, passwd, nil)
	other := strings.NewReplacer("aaa-fibby", "evil-fibby", "bbb-piggy", "evil-piggy").Replace(khOn)
	callerHome, _, engRepo := khTree(t, other, "", "")
	sw, err := resolveKeyholderSwitch(callerHome, engRepo)
	if err != nil {
		t.Fatal(err)
	}
	if sw.Fibby != "/nix/store/aaa-fibby/bin/fibby" || sw.Piggy != "/nix/store/bbb-piggy/bin/piggy" {
		t.Errorf("programs = %q %q, want the passwd home's", sw.Fibby, sw.Piggy)
	}
}

// An account database that cannot answer is no launch, never the operator's agent.
func TestKeyholderSwitchFailsClosedWhenThePasswdHomeCannotBeResolved(t *testing.T) {
	khPasswdHomeIs(t, "", errors.New("no passwd entry"))
	home, workRepo, _ := khTree(t, khOn, "", "")
	if _, err := resolveKeyholderSwitch(home, workRepo); err == nil || !strings.Contains(err.Error(), "passwd home") {
		t.Fatalf("an unresolvable passwd home = %v, want a refusal naming it", err)
	}
}

// A nearer trusted layer's off does not undo a farther on, with no help from the
// driver's context: on anywhere in the trusted layers wins.
func TestKeyholderSwitchIsOnWhenAFartherTrustedLayerSaysOnAndANearerOneSaysOff(t *testing.T) {
	home, workRepo, _ := khTree(t, khOn, khOff, "")
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil || !sw.On {
		t.Fatalf("global on, nearer off = (%+v, %v), want on", sw, err)
	}
}
