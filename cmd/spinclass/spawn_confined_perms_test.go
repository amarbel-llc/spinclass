package main

import (
	"os"
	"path/filepath"
	"testing"
)

// spawnWillConfine reads the spawn-session arguments the permission floor
// consults: `repo`, and the `confined` request. Any non-boolean is doubt, and
// doubt keeps the prompt.
func TestSpawnWillConfineReadsTheConfinedRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "work", "driver")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("[confinement]\nenabled = true\ndefault-spawn = false\n")
	if spawnWillConfine(map[string]any{}, repo) {
		t.Error("opt-in mode: a spawn that does not ask for confinement is unconfined")
	}
	if !spawnWillConfine(map[string]any{"confined": true}, repo) {
		t.Error("opt-in mode: confined=true should confine")
	}
	if spawnWillConfine(map[string]any{"confined": false}, repo) {
		t.Error("confined=false is not a request")
	}
	if spawnWillConfine(map[string]any{"confined": "true"}, repo) {
		t.Error("a non-bool confined is doubt and must keep the prompt")
	}

	write("")
	if spawnWillConfine(map[string]any{"confined": true}, repo) {
		t.Error("with the feature off a confined request is an error at launch, so it must not be allowed")
	}
}
