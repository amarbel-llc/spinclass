package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every test in this package reads the key holder switch for the target repo
// alone unless it says otherwise: a developer's own driver context (the repo the
// tests run in, and the sweatfiles above it on the host) must not decide a
// result. Tests of the driver's context install their own repo.
func init() {
	keyholderDriverRepoFn = func(string) string { return "" }
}

// khTree builds a home with two sibling trees, work/ and eng/, each with its own
// parent-directory sweatfile body, and a repo under each. It returns the home
// and the two repos.
func khTree(t *testing.T, global, workBody, engBody string) (home, workRepo, engRepo string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	workRepo = filepath.Join(home, "work", "repo")
	engRepo = filepath.Join(home, "eng", "repo")
	for _, d := range []string{workRepo, engRepo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if body == "" {
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".config", "spinclass", "sweatfile"), global)
	write(filepath.Join(home, "work", "sweatfile"), workBody)
	write(filepath.Join(home, "eng", "sweatfile"), engBody)
	return home, workRepo, engRepo
}

func khDriverIs(t *testing.T, repo string) {
	t.Helper()
	prev := keyholderDriverRepoFn
	keyholderDriverRepoFn = func(string) string { return repo }
	t.Cleanup(func() { keyholderDriverRepoFn = prev })
}

const (
	khOn  = "[keyholder]\nmode = \"on\"\nfibby = \"/nix/store/aaa-fibby/bin/fibby\"\npiggy = \"/nix/store/bbb-piggy/bin/piggy\"\n"
	khOff = "[keyholder]\nmode = \"off\"\n"
)

// The target repo sits under a directory whose sweatfile says off, and the
// caller chose it: if the driver's own context says on, the key holder is on. A
// caller cannot turn the operator's switch off by pointing a spawn at a repo
// under a directory it controls.
func TestKeyholderSwitchIsOnWhenTheDriversContextSaysOnAndTheTargetSaysOff(t *testing.T) {
	home, workRepo, engRepo := khTree(t, "", khOff, khOn)
	khDriverIs(t, engRepo)
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !sw.On {
		t.Error("the target said off and the driver's context said on, but the key holder is off")
	}
	if sw.Fibby != "/nix/store/aaa-fibby/bin/fibby" || sw.Piggy != "/nix/store/bbb-piggy/bin/piggy" {
		t.Errorf("the programs did not come from the driver's context when the target sets none: %q %q", sw.Fibby, sw.Piggy)
	}
}

// The other direction: the target says on, the driver's context says off: on.
// The target can still turn it on.
func TestKeyholderSwitchIsOnWhenTheTargetSaysOnAndTheDriversContextSaysOff(t *testing.T) {
	home, workRepo, engRepo := khTree(t, "", khOn, khOff)
	khDriverIs(t, engRepo)
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil || !sw.On {
		t.Fatalf("target on, driver off = (%+v, %v), want on", sw, err)
	}
}

func TestKeyholderSwitchIsOffWhenBothSayOff(t *testing.T) {
	home, workRepo, engRepo := khTree(t, "", khOff, khOff)
	khDriverIs(t, engRepo)
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil || sw.On {
		t.Fatalf("both off = (%+v, %v), want off", sw, err)
	}
}

// The host writes [keyholder] into the global file: both contexts see it.
func TestKeyholderSwitchFromTheGlobalFileIsOnInBothContexts(t *testing.T) {
	home, workRepo, engRepo := khTree(t, khOn, khOff, "")
	khDriverIs(t, engRepo)
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil {
		t.Fatal(err)
	}
	// The work/ layer sets mode off after the global on; on anywhere in the
	// trusted layers wins, so the target alone says on, and so does the driver's
	// context, which sees only the global file.
	if !sw.On {
		t.Error("a lower layer's off undid the global on")
	}
}

// A repo-level [keyholder] in the driver's own repo is never honoured.
func TestKeyholderSwitchIgnoresARepoLevelTableInTheDriversRepo(t *testing.T) {
	home, workRepo, engRepo := khTree(t, "", khOff, "")
	if err := os.WriteFile(filepath.Join(engRepo, "sweatfile"), []byte(khOn), 0o644); err != nil {
		t.Fatal(err)
	}
	khDriverIs(t, engRepo)
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil || sw.On {
		t.Fatalf("a repo-level table in the driver's repo = (%+v, %v), want off", sw, err)
	}
}

// Two different programs for one pair: refused when on, because the directory the
// caller chose must not pick the program the pair runs.
func TestKeyholderSwitchRefusesWhenTheContextsNameDifferentPrograms(t *testing.T) {
	other := strings.ReplaceAll(khOn, "aaa-fibby", "zzz-fibby")
	home, workRepo, engRepo := khTree(t, "", other, khOn)
	khDriverIs(t, engRepo)
	_, err := resolveKeyholderSwitch(home, workRepo)
	if err == nil || !strings.Contains(err.Error(), "[keyholder] fibby") || !strings.Contains(err.Error(), "driver's context") {
		t.Fatalf("different programs = %v, want a refusal naming fibby and the driver's context", err)
	}
}

// An invalid mode in the driver's context fails closed.
func TestKeyholderSwitchFailsClosedOnAnInvalidModeInTheDriversContext(t *testing.T) {
	home, workRepo, engRepo := khTree(t, "", khOff, "[keyholder]\nmode = \"yes\"\n")
	khDriverIs(t, engRepo)
	if _, err := resolveKeyholderSwitch(home, workRepo); err == nil || !strings.Contains(err.Error(), "driver's context") {
		t.Fatalf("an invalid driver mode = %v, want a refusal naming the driver's context", err)
	}
}

// The boundary check reads the same switch, so a relaunch is refused or allowed
// on the same answer a launch would give.
func TestKeyholderWantsPairReadsTheSameSwitchAsALaunch(t *testing.T) {
	home, workRepo, engRepo := khTree(t, "", khOff, khOn)
	khDriverIs(t, engRepo)
	khLaunchSeams(t, false, "") // not a driver launch: a worker
	on, err := keyholderWantsPair(home, workRepo, true)
	if err != nil || !on {
		t.Fatalf("keyholderWantsPair = (%v, %v), want on through the driver's context", on, err)
	}
}
