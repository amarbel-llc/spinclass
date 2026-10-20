package hooks

import (
	"bytes"
	"testing"
)

// A grit store call from a caller with no resolvable session is deferred to the
// serve (allow, with the reason saying so); force still prompts.
func TestStoreToolWithoutSessionDefersToTheServe(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	for _, tc := range []struct {
		subcommand string
		force      bool
		want       string
	}{
		{"log", false, "allow"},
		{"delete", false, "allow"},
		{"delete", true, "ask"},
	} {
		cwd := t.TempDir()
		input := makeInput(
			"mcp__plugin_spinclass_spinclass__grit_branch",
			map[string]any{"subcommand": tc.subcommand, "name": "x", "force": tc.force}, cwd,
		)
		var stdout bytes.Buffer
		if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		decision, reason := parseHookDecision(t, stdout.Bytes())
		if decision != tc.want || reason == "" {
			t.Errorf("%s force=%v: got decision %q reason %q, want %s with a reason", tc.subcommand, tc.force, decision, reason, tc.want)
		}
	}
}
