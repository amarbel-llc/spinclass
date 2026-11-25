package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseBridgeKnob(t *testing.T) {
	for knob, want := range map[string]string{"": "", "off": "", "offered": "offered", "required": "required"} {
		if got, err := ParseBridgeKnob(knob); err != nil || got != want {
			t.Errorf("%q: got (%q, %v), want (%q, nil)", knob, got, err, want)
		}
	}
	for _, bad := range []string{"on", "true", "Offered", "required ", "enable", "1"} {
		if _, err := ParseBridgeKnob(bad); err == nil || !strings.Contains(err.Error(), `"`+bad+`"`) {
			t.Errorf("%q: want an error naming the value, got %v", bad, err)
		}
	}
}

// [confinement] bridge is the ONE operator switch of the bridge (J-M5): the
// launch-spec field is emitted from it. It is read only from layers above the
// repo, the last trusted layer that sets it wins, and an unset knob is off. A
// repo layer that sets it never turns the bridge on and is reported as
// untrusted.
func TestTrustedConfinementBridgeKnob(t *testing.T) {
	const tbl = "[confinement]\n"
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	parent := filepath.Join(home, "work", "sweatfile")
	repoFile := filepath.Join(repo, "sweatfile")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func() (string, []string) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		p, _, untrusted := TrustedConfinement(h, home, repo)
		return p.Bridge, untrusted
	}

	if got, u := resolve(); got != "" || len(u) != 0 {
		t.Errorf("unset: got (%q, %v), want (off, none)", got, u)
	}
	write(repoFile, tbl+"bridge = \"required\"\n")
	if got, u := resolve(); got != "" || !reflect.DeepEqual(u, []string{repoFile}) {
		t.Errorf("repo layer: got (%q, %v), want (off, [%s]): a repo layer must not set the switch and must be reported", got, u, repoFile)
	}
	write(global, tbl+"bridge = \"offered\"\n")
	if got, _ := resolve(); got != "offered" {
		t.Errorf("global offered: got %q", got)
	}
	write(parent, tbl+"bridge = \"required\"\n")
	if got, _ := resolve(); got != "required" {
		t.Errorf("nearer trusted layer required: got %q", got)
	}
	write(parent, tbl+"bridge = \"off\"\n")
	if got, _ := resolve(); got != "off" {
		t.Errorf("nearer trusted layer off over a global offered: got %q, want %q", got, "off")
	}
	// A repo layer cannot turn a trusted off or offered into required.
	write(repoFile, tbl+"bridge = \"required\"\n")
	if got, _ := resolve(); got != "off" {
		t.Errorf("repo layer over a trusted off: got %q, want %q", got, "off")
	}
}
