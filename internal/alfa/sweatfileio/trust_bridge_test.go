package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [bridge] enable is read only from layers above the repo; a repo layer that
// sets it never turns the bridge on and is reported as untrusted.
func TestTrustedBridgeEnabled(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
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
	resolve := func() (bool, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedBridgeEnabled(h, home, repo)
	}

	if on, u := resolve(); on || len(u) != 0 {
		t.Errorf("unset: got (%v, %v), want (false, none)", on, u)
	}
	write(repoFile, "[bridge]\nenable = true\n")
	if on, u := resolve(); on || !reflect.DeepEqual(u, []string{repoFile}) {
		t.Errorf("repo layer: got (%v, %v), want (false, [%s])", on, u, repoFile)
	}
	write(global, "[bridge]\nenable = true\n")
	if on, _ := resolve(); !on {
		t.Errorf("global enable = true: got false")
	}
	write(filepath.Join(home, "work", "sweatfile"), "[bridge]\nenable = false\n")
	if on, _ := resolve(); on {
		t.Errorf("nearer trusted layer enable = false: got true")
	}
}
