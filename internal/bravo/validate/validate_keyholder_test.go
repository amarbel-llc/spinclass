package validate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// keyholderRun validates a hierarchy whose only layer is the global sweatfile
// with the given body and returns the TAP output and the exit code.
func keyholderRun(t *testing.T, body string) (string, int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweatfile(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), body)
	var buf bytes.Buffer
	code := Run(&buf, home, repoDir)
	return buf.String(), code
}

// A host with the mode on and no fibby or piggy refuses every start, resume and
// spawn, and the per-layer check cannot see that: validate must fail on it,
// naming the key and where it comes from.
func TestRunFailsWhenTheKeyHolderIsOnWithoutItsPrograms(t *testing.T) {
	out, code := keyholderRun(t, "[keyholder]\nmode = \"on\"\n")
	if code == 0 {
		t.Errorf("exit 0 for mode on with fibby and piggy unset:\n%s", out)
	}
	for _, want := range []string{"keyholder.fibby", "[keyholder] fibby is not set", "keyholder.piggy", "the host's configuration writes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The effective mode is always printed, off included, so a host whose trusted
// file is missing does not look like a healthy one.
func TestRunPrintsTheEffectiveKeyHolderMode(t *testing.T) {
	out, code := keyholderRun(t, "")
	if code != 0 || !strings.Contains(out, "key holder: off") {
		t.Errorf("no sweatfile: exit %d, want 0 and the line 'key holder: off':\n%s", code, out)
	}
	out, _ = keyholderRun(t, "[keyholder]\nmode = \"on\"\n")
	if !strings.Contains(out, "key holder: on") {
		t.Errorf("mode on: want the line 'key holder: on':\n%s", out)
	}
}

// preflightIs aims the shared start preconditions, which a test build (no tool
// pins) cannot meet, at a canned answer.
func preflightIs(t *testing.T, problems ...keyholder.Problem) {
	t.Helper()
	prev := KeyholderPreflight
	KeyholderPreflight = func() []keyholder.Problem { return problems }
	t.Cleanup(func() { KeyholderPreflight = prev })
}

// What a start would refuse, validate fails on, one point each, with the next
// command; with the mode off it asks nothing.
func TestRunFailsOnEveryPreconditionAPairWouldRefuse(t *testing.T) {
	body := khStoreOn(t, "mode = \"on\"\n")
	preflightIs(t, keyholder.Problem{Name: "user-manager", What: "your systemd user manager does not answer", Next: "run `loginctl enable-linger \"$USER\"`"})
	out, code := keyholderRun(t, body)
	for _, want := range []string{"keyholder.user-manager", "does not answer", "next: run `loginctl enable-linger"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if code == 0 {
		t.Error("exit 0 with a precondition that does not hold")
	}
	out, code = keyholderRun(t, "")
	if code != 0 || strings.Contains(out, "keyholder.user-manager") || !strings.Contains(out, "to turn it on") {
		t.Errorf("off: exit %d, want 0, no precondition and how to turn it on:\n%s", code, out)
	}
}

// Both paths valid: mode on is clean, and says how to prove it.
func TestRunAcceptsTheKeyHolderOnWithBothPrograms(t *testing.T) {
	preflightIs(t)
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
	out, code := keyholderRun(t, "[keyholder]\nmode = \"on\"\nfibby = \""+fibby+"\"\npiggy = \""+piggy+"\"\n")
	if code != 0 || strings.Contains(out, "not ok") || !strings.Contains(out, "sc keyholder-selftest") {
		t.Errorf("mode on with both programs: exit %d:\n%s", code, out)
	}
}

// [keyholder] is the operator's switch: a repo-level table warns once and a
// trusted one does not; an invalid mode is an error wherever it is.
func TestCheckKeyholder(t *testing.T) {
	on, bad := "on", "yes"
	sf := sweatfile.Sweatfile{Keyholder: &sweatfile.Keyholder{Mode: &on}}
	if got := CheckKeyholder(sf, false); len(got) != 1 || got[0].Field != "keyholder" || got[0].Severity != SeverityWarning {
		t.Errorf("repo-layer [keyholder] should warn once, got %+v", got)
	}
	if got := CheckKeyholder(sf, true); len(got) != 0 {
		t.Errorf("above-repo valid [keyholder] should be silent, got %+v", got)
	}
	if got := CheckKeyholder(sweatfile.Sweatfile{}, false); len(got) != 0 {
		t.Errorf("no [keyholder] table should be silent, got %+v", got)
	}
	got := CheckKeyholder(sweatfile.Sweatfile{Keyholder: &sweatfile.Keyholder{Mode: &bad}}, true)
	if len(got) != 1 || got[0].Severity != SeverityError || !strings.Contains(got[0].Message, `"yes"`) {
		t.Errorf("an invalid mode should be one error naming the value, got %+v", got)
	}
}

// A [keyholder] fibby or piggy no store program could satisfy is a warning while
// the layer's mode is not "on" and an error when it is: before the switch is
// turned on nothing is launched, after it every launch would be refused.
func TestCheckKeyholderToolPaths(t *testing.T) {
	on, off := "on", "off"
	rel, outside := "bin/fibby", "/usr/bin/piggy"
	cases := []struct {
		name     string
		kh       sweatfile.Keyholder
		wantSev  string
		wantMsg  string
		wantNone bool
	}{
		{"relative fibby, mode on", sweatfile.Keyholder{Mode: &on, Fibby: &rel}, SeverityError, "[keyholder] fibby", false},
		{"outside the store piggy, mode on", sweatfile.Keyholder{Mode: &on, Piggy: &outside}, SeverityError, "[keyholder] piggy", false},
		{"relative fibby, mode off", sweatfile.Keyholder{Mode: &off, Fibby: &rel}, SeverityWarning, "[keyholder] fibby", false},
		{"relative fibby, mode unset", sweatfile.Keyholder{Fibby: &rel}, SeverityWarning, "[keyholder] fibby", false},
		{"nothing set", sweatfile.Keyholder{Mode: &on}, "", "", true},
	}
	for _, c := range cases {
		kh := c.kh
		got := CheckKeyholder(sweatfile.Sweatfile{Keyholder: &kh}, true)
		if c.wantNone {
			if len(got) != 0 {
				t.Errorf("%s: got %+v, want nothing", c.name, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Severity != c.wantSev || !strings.Contains(got[0].Message, c.wantMsg) {
			t.Errorf("%s: got %+v, want one %s naming %q", c.name, got, c.wantSev, c.wantMsg)
		}
	}
}
