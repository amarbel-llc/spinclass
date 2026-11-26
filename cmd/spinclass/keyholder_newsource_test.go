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

// The passwd home answers but a sweatfile under it does not load: a new source.
func TestAnUnloadablePasswdHomeHierarchyWithNoEvidenceProceedsAndWithStateRefuses(t *testing.T) {
	for _, haveState := range []bool{false, true} {
		warn := khEvidence(t, haveState)
		passwd := t.TempDir()
		khGlobalAt(t, passwd, "this is = not [valid toml")
		khPasswdHomeIs(t, passwd, nil)
		home, workRepo, _ := khTree(t, "", "", "")
		sw, err := resolveKeyholderSwitch(home, workRepo)
		if haveState {
			if err == nil {
				t.Errorf("state present: want a refusal, got %+v", sw)
			}
			continue
		}
		if err != nil || sw.On {
			t.Errorf("no evidence: (%+v, %v), want the switch-off result", sw, err)
		}
		if !strings.Contains(warn.String(), "sweatfiles under the account's home") {
			t.Errorf("no evidence: warning = %q", warn.String())
		}
	}
}

// The driver's own context does not load: a new source (the target repo's own
// hierarchy was already read by every launch).
func TestAnUnloadableDriverContextWithNoEvidenceProceedsAndWithStateRefuses(t *testing.T) {
	for _, haveState := range []bool{false, true} {
		warn := khEvidence(t, haveState)
		home, workRepo, engRepo := khTree(t, "", "", "this is = not [valid toml")
		khDriverIs(t, engRepo)
		sw, err := resolveKeyholderSwitch(home, workRepo)
		if haveState {
			if err == nil || !strings.Contains(err.Error(), "driver's sweatfiles") {
				t.Errorf("state present: err = %v, want a refusal naming the driver's sweatfiles", err)
			}
			continue
		}
		if err != nil || sw.On {
			t.Errorf("no evidence: (%+v, %v), want the switch-off result", sw, err)
		}
		if !strings.Contains(warn.String(), "driver's sweatfiles") {
			t.Errorf("no evidence: warning = %q", warn.String())
		}
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
