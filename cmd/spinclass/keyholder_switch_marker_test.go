package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// switchMarkerFixture is switchCmdFixture plus what the `on` command reads: the
// caller's HOME holds the layers (global sweatfile body given), the caller's
// state root is its own directory, and the working directory is in no repo.
func switchMarkerFixture(t *testing.T, global string) (passwd, envRoot string) {
	t.Helper()
	passwd, _ = switchCmdFixture(t)
	home, _, _ := khTree(t, global, "", "")
	t.Setenv("HOME", home)
	envRoot = filepath.Join(t.TempDir(), "state", "spinclass", "keyholder")
	prevBase := keyholderStateBaseFn
	keyholderStateBaseFn = func() string { return envRoot }
	prevRepo := keyholderCallerRepoFn
	keyholderCallerRepoFn = func() string { return "" }
	t.Cleanup(func() { keyholderStateBaseFn, keyholderCallerRepoFn = prevBase, prevRepo })
	return passwd, envRoot
}

// L-1: `on` is not written when it would refuse every worker launch.
func TestOnIsNotWrittenWhenNoTrustedLayerSuppliesThePrograms(t *testing.T) {
	passwd, envRoot := switchMarkerFixture(t, "")
	_, errOut := stubOperatorTerminal(t, true)
	err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: "on"})
	if err == nil || !strings.Contains(err.Error(), "fibby and piggy") || !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "~/.config/spinclass/sweatfile") {
		t.Fatalf("on without programs = %v (stderr %q), want a refusal naming fibby and piggy and where the host writes them", err, errOut)
	}
	if _, serr := os.Lstat(sweatfileio.KeyholderSwitchPath(passwd)); serr == nil {
		t.Error("a refused on wrote the switch file")
	}
	for _, root := range []string{envRoot, keyholder.PasswdStateBase(passwd)} {
		if keyholder.HasSwitchMarker(root) {
			t.Errorf("a refused on left a marker under %s", root)
		}
	}
}

// off and default never need programs.
func TestOffAndDefaultAreWrittenWithoutPrograms(t *testing.T) {
	passwd, _ := switchMarkerFixture(t, "")
	stubOperatorTerminal(t, true)
	for _, w := range []string{"off", "default"} {
		if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: w}); err != nil {
			t.Fatalf("%s without programs = %v", w, err)
		}
	}
	if v, present, err := sweatfileio.ReadKeyholderSwitch(passwd); err != nil || !present || v != sweatfileio.KeyholderSwitchDefault {
		t.Errorf("after off and default the file reads (%q, %v, %v)", v, present, err)
	}
}

// L-6: on leaves the evidence marker under both roots the gate reads; off and
// default take it away.
func TestOnLeavesTheEvidenceMarkerUnderBothRootsAndOffAndDefaultRemoveIt(t *testing.T) {
	passwd, envRoot := switchMarkerFixture(t, khPathsOnly)
	stdout, _ := stubOperatorTerminal(t, true)
	pwRoot := keyholder.PasswdStateBase(passwd)
	if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: "on"}); err != nil {
		t.Fatal(err)
	}
	if v, present, err := sweatfileio.ReadKeyholderSwitch(passwd); err != nil || !present || v != sweatfileio.KeyholderSwitchOn {
		t.Fatalf("after on the file reads (%q, %v, %v)", v, present, err)
	}
	for _, root := range []string{envRoot, pwRoot} {
		if !keyholder.HasSwitchMarker(root) {
			t.Errorf("no marker under %s after on", root)
		}
		if has, _ := keyholder.HasAnyState(root); !has {
			t.Errorf("%s is not evidence after on", root)
		}
	}
	if out := stdout.String(); !strings.Contains(out, "evidence marker left") || !strings.Contains(out, keyholder.SwitchMarkerPath(envRoot)) {
		t.Errorf("the output does not name the markers:\n%s", out)
	}
	for _, w := range []string{"off", "default"} {
		if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: "on"}); err != nil {
			t.Fatal(err)
		}
		if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: w}); err != nil {
			t.Fatal(err)
		}
		for _, root := range []string{envRoot, pwRoot} {
			if keyholder.HasSwitchMarker(root) {
				t.Errorf("the marker under %s survived %s", root, w)
			}
		}
	}
}

// The gate: after on, a failing account lookup refuses a launch (the operator's
// agent is never the silent fallback); without the marker and any other evidence
// it warns and proceeds as before.
func TestAPasswdFailureAfterOnRefusesAndWithoutTheMarkerWarnsAndProceeds(t *testing.T) {
	passwd, envRoot := switchMarkerFixture(t, khPathsOnly)
	stubOperatorTerminal(t, true)
	if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: "on"}); err != nil {
		t.Fatal(err)
	}
	// The account database now fails: the switch file cannot be read, and no layer
	// says on (the paths-only global file has no mode).
	khPasswdHomeIs(t, "", errors.New("no passwd entry"))
	home, _ := os.UserHomeDir()
	_, err := resolveKeyholderSwitch(home, home)
	if err == nil || !strings.Contains(err.Error(), "left state under") || !strings.Contains(err.Error(), envRoot) {
		t.Fatalf("a passwd failure after on = %v, want a refusal that names the state root", err)
	}
	// Control: the same failure with no marker and no other evidence warns and
	// proceeds as it did before the key holder existed.
	if rerr := keyholder.RemoveSwitchMarker(envRoot); rerr != nil {
		t.Fatal(rerr)
	}
	if rerr := keyholder.RemoveSwitchMarker(keyholder.PasswdStateBase(passwd)); rerr != nil {
		t.Fatal(rerr)
	}
	if _, err := resolveKeyholderSwitch(home, home); err != nil {
		t.Errorf("a passwd failure with no marker = %v, want a warning and the launch proceeding", err)
	}
}
