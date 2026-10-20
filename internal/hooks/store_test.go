package hooks

import (
	"bytes"
	"testing"
)

// A grit store call from a caller with no resolvable session must prompt, not
// fall through to the allow-lists, and the ask must explain itself.
func TestStoreToolWithoutSessionAsks(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	for _, subcommand := range []string{"log", "delete"} {
		cwd := t.TempDir()
		input := makeInput(
			"mcp__plugin_spinclass_spinclass__grit_branch",
			map[string]any{"subcommand": subcommand, "name": "x"}, cwd,
		)
		var stdout bytes.Buffer
		if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		decision, reason := parseHookDecision(t, stdout.Bytes())
		if decision != "ask" || reason == "" {
			t.Errorf("%s: got decision %q reason %q, want ask with a reason", subcommand, decision, reason)
		}
	}
}
