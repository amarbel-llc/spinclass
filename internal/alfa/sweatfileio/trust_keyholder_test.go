package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTrustedKeyholderMode(t *testing.T) {
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
	resolve := func() (bool, []string, error) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedKeyholderMode(h, home, repo)
	}

	on, untrusted, err := resolve()
	if err != nil || on || len(untrusted) != 0 {
		t.Fatalf("defaults: got (%v, %v, %v), want off with nothing untrusted", on, untrusted, err)
	}

	// A repo-layer "on" never turns it on, and is reported.
	write(repoFile, "[keyholder]\nmode = \"on\"\n")
	on, untrusted, err = resolve()
	if err != nil || on {
		t.Errorf("a repo-layer mode = on switched the key holder on: (%v, %v)", on, err)
	}
	if !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("untrusted = %v, want [%s]", untrusted, repoFile)
	}

	// The global layer turns it on; a nearer trusted layer can turn it off again.
	write(global, "[keyholder]\nmode = \"on\"\n")
	on, _, err = resolve()
	if err != nil || !on {
		t.Errorf("global mode = on: got (%v, %v), want on", on, err)
	}
	write(parent, "[keyholder]\nmode = \"off\"\n")
	on, _, err = resolve()
	if err != nil || on {
		t.Errorf("a nearer trusted off did not win: (%v, %v)", on, err)
	}

	// Any other value is an error and leaves it off.
	write(parent, "[keyholder]\nmode = \"yes\"\n")
	on, _, err = resolve()
	if err == nil || on {
		t.Errorf("mode = yes: got (%v, %v), want an error and off", on, err)
	}
}
