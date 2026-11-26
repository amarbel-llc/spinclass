package validate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// switchFileIs aims the deploy-free switch file reader at a canned answer.
func switchFileIs(t *testing.T, v sweatfileio.KeyholderSwitchValue, present bool, err error) {
	t.Helper()
	prev := KeyholderSwitchFile
	KeyholderSwitchFile = func() (sweatfileio.KeyholderSwitchValue, string, bool, error) {
		return v, "/home/u/.config/spinclass/keyholder-switch", present, err
	}
	t.Cleanup(func() { KeyholderSwitchFile = prev })
}

// khStore points the store root at a directory holding two executables, so a
// [keyholder] layer can name valid programs; it returns the layer body that turns
// the key holder on with them.
func khStoreOn(t *testing.T, mode string) string {
	t.Helper()
	store := t.TempDir()
	prev := sweatfileio.KeyholderStoreRoot
	sweatfileio.KeyholderStoreRoot = store
	t.Cleanup(func() { sweatfileio.KeyholderStoreRoot = prev })
	fibby, piggy := filepath.Join(store, "fibby"), filepath.Join(store, "piggy")
	for _, p := range []string{fibby, piggy} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return "[keyholder]\n" + mode + "fibby = \"" + fibby + "\"\npiggy = \"" + piggy + "\"\n"
}

// With no switch file the lines say where the mode came from: a layer, or none.
func TestRunNamesTheSourceOfTheEffectiveKeyHolderMode(t *testing.T) {
	switchFileIs(t, sweatfileio.KeyholderSwitchDefault, false, nil)
	out, _ := keyholderRun(t, "")
	if !strings.Contains(out, "key holder: off (source: none") {
		t.Errorf("no layer, no file: want 'key holder: off (source: none':\n%s", out)
	}
	out, _ = keyholderRun(t, khStoreOn(t, "mode = \"on\"\n"))
	if !strings.Contains(out, "key holder: on (source: layer") {
		t.Errorf("a layer on: want 'key holder: on (source: layer':\n%s", out)
	}
}

// off is the kill switch: it beats a layer that says on, and says so.
func TestRunShowsTheSwitchFileOffBeatingALayersOn(t *testing.T) {
	switchFileIs(t, sweatfileio.KeyholderSwitchOff, true, nil)
	out, code := keyholderRun(t, khStoreOn(t, "mode = \"on\"\n"))
	if code != 0 || !strings.Contains(out, "key holder: off (source: switch file") {
		t.Errorf("file off, layer on: exit %d, want 'key holder: off (source: switch file':\n%s", code, out)
	}
}

// on in the file turns the key holder on, with the programs from the layers; with
// none supplying them validate fails, as it does for a layer's on.
func TestRunShowsTheSwitchFileOnAndTheProgramsItStillNeeds(t *testing.T) {
	switchFileIs(t, sweatfileio.KeyholderSwitchOn, true, nil)
	out, code := keyholderRun(t, khStoreOn(t, ""))
	if code != 0 || !strings.Contains(out, "key holder: on (source: switch file") {
		t.Errorf("file on with programs: exit %d, want 'key holder: on (source: switch file':\n%s", code, out)
	}
	out, code = keyholderRun(t, "")
	if code == 0 || !strings.Contains(out, "keyholder.fibby") {
		t.Errorf("file on without programs: exit %d, want a failure naming keyholder.fibby:\n%s", code, out)
	}
}

// A switch file that exists and cannot be used refuses launches: validate fails
// and names it.
func TestRunFailsOnAnUnusableSwitchFile(t *testing.T) {
	switchFileIs(t, "", true, errors.New("the key holder switch file /x: \"maybe\" is not one of on, off or default"))
	out, code := keyholderRun(t, "")
	if code == 0 || !strings.Contains(out, "keyholder.switch-file") || !strings.Contains(out, "not one of on, off or default") {
		t.Errorf("a garbage switch file: exit %d, want a failure naming keyholder.switch-file:\n%s", code, out)
	}
}

func TestKeyholderEffectivelyOnLaysTheFileOverTheLayers(t *testing.T) {
	for _, tc := range []struct {
		name          string
		v             sweatfileio.KeyholderSwitchValue
		present       bool
		layerOn, want bool
	}{
		{"no file, layer on", "", false, true, true},
		{"no file, layer off", "", false, false, false},
		{"default, layer on", sweatfileio.KeyholderSwitchDefault, true, true, true},
		{"off beats a layer's on", sweatfileio.KeyholderSwitchOff, true, true, false},
		{"on beats a layer's off", sweatfileio.KeyholderSwitchOn, true, false, true},
	} {
		switchFileIs(t, tc.v, tc.present, nil)
		if got := keyholderEffectivelyOn(tc.layerOn); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
