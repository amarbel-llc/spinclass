package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// A host that never switched the key holder on launches exactly as before: a
// failure of a source the key holder ADDED refuses only with evidence the switch
// is or was on (a loaded layer says on, or the key holder left state under the
// state root); otherwise it is warned once and ignored.

// khEvidence stands a state root in and says whether the key holder has left
// state there. It returns the warning buffer too.
func khEvidence(t *testing.T, haveState bool) *bytes.Buffer {
	t.Helper()
	base := filepath.Join(t.TempDir(), "kh")
	if haveState {
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "0123456789abcdef.lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := keyholderStateBaseFn
	keyholderStateBaseFn = func() string { return base }
	var warn bytes.Buffer
	prevW := keyholderWarnWriter
	keyholderWarnWriter = &warn
	keyholderWarned = sync.Map{}
	t.Cleanup(func() { keyholderStateBaseFn = prev; keyholderWarnWriter = prevW; keyholderWarned = sync.Map{} })
	return &warn
}

func TestAPasswdLookupFailureWithNoEvidenceWarnsOnceAndProceedsAsBefore(t *testing.T) {
	warn := khEvidence(t, false)
	khPasswdHomeIs(t, "", errors.New("no passwd entry"))
	home, workRepo, _ := khTree(t, "", "", "")
	for i := 0; i < 3; i++ {
		sw, err := resolveKeyholderSwitch(home, workRepo)
		if err != nil {
			t.Fatalf("launch %d: a host with no key holder refused on a passwd lookup failure: %v", i, err)
		}
		if sw.On || sw.Fibby != "" || sw.Piggy != "" {
			t.Fatalf("launch %d: switch = %+v, want exactly the switch-off result", i, sw)
		}
	}
	if n := strings.Count(warn.String(), "\n"); n != 1 || !strings.Contains(warn.String(), "account's home") {
		t.Errorf("warnings = %q, want exactly one line naming the account's home", warn.String())
	}
}

func TestAPasswdLookupFailureWithALoadedLayerSayingOnRefuses(t *testing.T) {
	warn := khEvidence(t, false)
	khPasswdHomeIs(t, "", errors.New("no passwd entry"))
	home, workRepo, _ := khTree(t, khOn, "", "")
	if _, err := resolveKeyholderSwitch(home, workRepo); err == nil || !strings.Contains(err.Error(), "account's home") {
		t.Fatalf("passwd failure + a layer on = %v, want a refusal naming the account's home", err)
	}
	if warn.Len() != 0 {
		t.Errorf("a refusal also warned: %q", warn.String())
	}
}

func TestAPasswdLookupFailureWithKeyholderStateUnderTheStateRootRefuses(t *testing.T) {
	warn := khEvidence(t, true)
	khPasswdHomeIs(t, "", errors.New("no passwd entry"))
	home, workRepo, _ := khTree(t, "", "", "")
	if _, err := resolveKeyholderSwitch(home, workRepo); err == nil || !strings.Contains(err.Error(), "account's home") {
		t.Fatalf("passwd failure + key holder state = %v, want a refusal naming the account's home", err)
	}
	if warn.Len() != 0 {
		t.Errorf("a refusal also warned: %q", warn.String())
	}
}

// The passwd home answers and a sweatfile under it EXISTS but does not load: that
// file may be the one that switches the key holder on, so it refuses with no
// state at all. Only an ABSENT layer is "never on".
func TestAnExistingButUnloadablePasswdHomeLayerRefusesWithNoOtherEvidence(t *testing.T) {
	warn := khEvidence(t, false)
	passwd := t.TempDir()
	khGlobalAt(t, passwd, "this is = not [valid toml")
	khPasswdHomeIs(t, passwd, nil)
	home, workRepo, _ := khTree(t, "", "", "")
	if sw, err := resolveKeyholderSwitch(home, workRepo); err == nil || !strings.Contains(err.Error(), "sweatfiles under the account's home") {
		t.Fatalf("an unloadable passwd-home layer = (%+v, %v), want a refusal naming the account's home sweatfiles", sw, err)
	}
	if warn.Len() != 0 {
		t.Errorf("a refusal also warned: %q", warn.String())
	}
}

// An ABSENT layer under the passwd home is "never on": nothing to load, the launch
// is as before.
func TestAnAbsentPasswdHomeLayerIsNeverOn(t *testing.T) {
	khEvidence(t, false)
	khPasswdHomeIs(t, t.TempDir(), nil)
	home, workRepo, _ := khTree(t, "", "", "")
	if sw, err := resolveKeyholderSwitch(home, workRepo); err != nil || sw.On {
		t.Fatalf("an absent passwd-home layer = (%+v, %v), want the switch-off result", sw, err)
	}
}

// The driver's own context has a layer that EXISTS but does not load: refuse (the
// target repo's own hierarchy was already read by every launch, and still refuses).
func TestAnExistingButUnloadableDriverLayerRefusesWithNoOtherEvidence(t *testing.T) {
	warn := khEvidence(t, false)
	home, workRepo, engRepo := khTree(t, "", "", "this is = not [valid toml")
	khDriverIs(t, engRepo)
	if sw, err := resolveKeyholderSwitch(home, workRepo); err == nil || !strings.Contains(err.Error(), "driver's sweatfiles") {
		t.Fatalf("an unloadable driver layer = (%+v, %v), want a refusal naming the driver's sweatfiles", sw, err)
	}
	if warn.Len() != 0 {
		t.Errorf("a refusal also warned: %q", warn.String())
	}
}

// The evidence is read from the account database's home as well as from the
// environment's state root: a process started with another XDG_STATE_HOME cannot
// hide what the host holds.
func TestEvidenceIsReadFromThePasswdHomeToo(t *testing.T) {
	warn := khEvidence(t, false) // the environment's root: empty
	passwd := t.TempDir()
	root := filepath.Join(passwd, ".local", "state", "spinclass", "keyholder")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "0123456789abcdef.last.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	khPasswdHomeIs(t, passwd, nil)
	err := keyholderNewSourceFailure(errors.New("some new source failed"), false)
	if err == nil || !strings.Contains(err.Error(), root) {
		t.Fatalf("state under the account's home = %v, want a refusal naming %s", err, root)
	}
	if warn.Len() != 0 {
		t.Errorf("a refusal also warned: %q", warn.String())
	}
}

// A state root that is not absolute (an empty HOME makes the environment's root
// relative to the working directory) says nothing: it must not count as evidence
// because the working directory happens to hold such a path.
func TestARelativeStateRootIsNotEvidence(t *testing.T) {
	khEvidence(t, false)
	khPasswdHomeIs(t, t.TempDir(), nil)
	cwd := t.TempDir()
	rel := filepath.Join(".local", "state", "spinclass", "keyholder")
	if err := os.MkdirAll(filepath.Join(cwd, rel), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, rel, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })
	keyholderStateBaseFn = func() string { return rel }
	if nerr := keyholderNewSourceFailure(errors.New("some new source failed"), false); nerr != nil {
		t.Fatalf("a relative state root counted as evidence: %v", nerr)
	}
}

// A refusal by evidence says so, names the state root it looked at, and the way
// out; a refusal because a layer says on says that.
func TestARefusalByEvidenceNamesTheStateRootAndTheWayOut(t *testing.T) {
	khEvidence(t, true)
	root := keyholderStateBaseFn()
	err := keyholderNewSourceFailure(errors.New("the failure"), false)
	if err == nil {
		t.Fatal("no refusal")
	}
	for _, want := range []string{"the failure", root, "sc keyholder-stop --all", "sc clean", "remove"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	err = keyholderNewSourceFailure(errors.New("the failure"), true)
	if err == nil || !strings.Contains(err.Error(), "layer that did load says the key holder is on") {
		t.Errorf("refusal for a layer saying on = %v", err)
	}
}

// HOME and XDG_STATE_HOME empty: the relaunch site cannot resolve the state
// directory, but the account's own layers say on, so the relaunch is refused
// (the evidence did not follow the empty environment).
func TestTheRelaunchBoundaryRefusesWhenThePasswdHomeSaysOnAndTheEnvironmentIsEmpty(t *testing.T) {
	khEvidence(t, false)
	passwd := t.TempDir()
	khGlobalAt(t, passwd, khOn)
	khPasswdHomeIs(t, passwd, nil)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	st := &session.State{SessionKey: "repo/b", RepoPath: t.TempDir(), WorktreePath: t.TempDir(), SessionState: session.StateInactive}
	if err := relaunchBoundaryNow(st); err == nil || !strings.Contains(err.Error(), "cannot tell whether") {
		t.Fatalf("relaunchBoundaryNow with the passwd home saying on = %v, want a refusal", err)
	}
}

func TestTheStartHandoffRefusesWhenThePasswdHomeSaysOnAndTheHomeIsEmpty(t *testing.T) {
	khEvidence(t, false)
	passwd := t.TempDir()
	khGlobalAt(t, passwd, khOn)
	khPasswdHomeIs(t, passwd, nil)
	t.Setenv("HOME", "")
	child := &session.State{SessionKey: "repo/c", RepoPath: t.TempDir(), WorktreePath: t.TempDir(), SessionState: session.StateInactive}
	if _, _, err := startHandoffEnvFor(child, os.Environ(), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("startHandoffEnvFor with the passwd home saying on = %v, want a refusal naming the home directory", err)
	}
}

// The TARGET repo's own hierarchy not loading refused every launch before the key
// holder existed (attachSession and the spawn path read it first): it is not a new
// source and stays a refusal with no evidence at all.
func TestAnUnloadableTargetHierarchyStillRefusesWithNoEvidence(t *testing.T) {
	khEvidence(t, false)
	home, workRepo, _ := khTree(t, "", "this is = not [valid toml", "")
	if _, err := resolveKeyholderSwitch(home, workRepo); err == nil {
		t.Fatal("an unparseable target hierarchy no longer refuses")
	}
}

// A [keyholder] table that does not validate names the key holder: that is
// evidence in itself, and it refuses with no state at all.
func TestAnInvalidKeyholderTableRefusesWithNoOtherEvidence(t *testing.T) {
	khEvidence(t, false)
	home, workRepo, _ := khTree(t, "[keyholder]\nmode = \"maybe\"\n", "", "")
	if _, err := resolveKeyholderSwitch(home, workRepo); err == nil {
		t.Fatal("an invalid [keyholder] mode no longer refuses")
	}
}

// The relaunch paths resolve the home and the state directory themselves: a
// failure there is a new source too.
func TestTheRelaunchBoundaryProceedsOnAnUnresolvableStateDirWithNoEvidenceAndRefusesWithState(t *testing.T) {
	for _, haveState := range []bool{false, true} {
		warn := khEvidence(t, haveState)
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		st := &session.State{SessionKey: "repo/b", RepoPath: t.TempDir(), WorktreePath: t.TempDir(), SessionState: session.StateInactive}
		err := relaunchBoundaryNow(st)
		if haveState {
			if err == nil || !strings.Contains(err.Error(), "cannot tell whether") {
				t.Errorf("state present: err = %v, want a refusal", err)
			}
			continue
		}
		if err != nil {
			t.Errorf("no evidence: err = %v, want the relaunch to proceed as before", err)
		}
		if !strings.Contains(warn.String(), "cannot tell whether") {
			t.Errorf("no evidence: warning = %q", warn.String())
		}
	}
}

func TestTheStartHandoffProceedsAsBeforeOnAnUnresolvableHomeWithNoEvidenceAndRefusesWithState(t *testing.T) {
	for _, haveState := range []bool{false, true} {
		warn := khEvidence(t, haveState)
		t.Setenv("HOME", "")
		child := &session.State{SessionKey: "repo/c", RepoPath: t.TempDir(), WorktreePath: t.TempDir(), SessionState: session.StateInactive}
		env, undo, err := startHandoffEnvFor(child, os.Environ(), &bytes.Buffer{})
		if haveState {
			if err == nil || !strings.Contains(err.Error(), "home directory") {
				t.Errorf("state present: err = %v, want a refusal naming the home directory", err)
			}
			continue
		}
		if err != nil || env != nil || undo != nil {
			t.Errorf("no evidence: (%v, %v, %v), want nil, nil, nil: the hand-off runs as it always did", env, undo != nil, err)
		}
		if !strings.Contains(warn.String(), "home directory") {
			t.Errorf("no evidence: warning = %q", warn.String())
		}
	}
}
