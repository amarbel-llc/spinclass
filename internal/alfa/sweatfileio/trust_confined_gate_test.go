package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// [hooks] confined-pre-merge (spinclass#449) is the gate a guarded merge runs
// instead of the repo's pre-merge: read only from layers above the repo, last
// trusted layer wins, DefaultConfinedPreMerge when none sets it. A repo layer
// cannot choose it, because a confined worker's own commit can change the repo
// sweatfile and an operator-accepted gate-definition digest would then carry a
// gate the worker wrote.
func TestTrustedConfinedPreMerge(t *testing.T) {
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
	resolve := func() (ConfinedPreMergeGate, []string) {
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedConfinedPreMerge(h, home, repo)
	}

	gate, untrusted := resolve()
	if gate.Command != DefaultConfinedPreMerge || gate.Source != "" || len(untrusted) != 0 {
		t.Errorf("unset: got (%+v, %v), want the default from no layer", gate, untrusted)
	}
	write(repoFile, "[hooks]\nconfined-pre-merge = \"true\"\n")
	gate, untrusted = resolve()
	if gate.Command != DefaultConfinedPreMerge || !reflect.DeepEqual(untrusted, []string{repoFile}) {
		t.Errorf("repo layer: got (%+v, %v), want the default and [%s] untrusted", gate, untrusted, repoFile)
	}
	write(global, "[hooks]\nconfined-pre-merge = \"nix flake check --impure-is-not-here\"\n")
	gate, _ = resolve()
	if gate.Command != "nix flake check --impure-is-not-here" || gate.Source != global {
		t.Errorf("global: got %+v, want the global command and source", gate)
	}
	write(parent, "[hooks]\nconfined-pre-merge = \"nix build .#checks\"\n")
	gate, _ = resolve()
	if gate.Command != "nix build .#checks" || gate.Source != parent {
		t.Errorf("parent: got %+v, want the last trusted layer to win", gate)
	}
	// An empty string in a trusted layer clears back to the default, as a
	// scalar override does, so an operator can undo a parent's choice without
	// knowing the default's spelling.
	write(parent, "[hooks]\nconfined-pre-merge = \"\"\n")
	gate, _ = resolve()
	if gate.Command != DefaultConfinedPreMerge || gate.Source != "" {
		t.Errorf("cleared: got %+v, want the default", gate)
	}
}
