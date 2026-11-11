package main

import "testing"

// The unattended carve-out never applies to a driver that is itself unattended,
// and any argument that does not parse cleanly keeps the prompt (spinclass#476).
func TestSpawnWillBeUnattendedKeepsItsBounds(t *testing.T) {
	t.Setenv("CLOWN_UNATTENDED", "1")
	if spawnWillBeUnattended(map[string]any{}, t.TempDir()) {
		t.Error("an unattended driver's spawn must still prompt")
	}

	t.Setenv("CLOWN_UNATTENDED", "")
	for name, in := range map[string]map[string]any{
		"non-string repo":    {"repo": 3.0},
		"non-bool confined":  {"confined": "yes"},
		"repo that is a map": {"repo": map[string]any{}},
	} {
		if spawnWillBeUnattended(in, t.TempDir()) {
			t.Errorf("%s: want the prompt kept", name)
		}
	}
}
