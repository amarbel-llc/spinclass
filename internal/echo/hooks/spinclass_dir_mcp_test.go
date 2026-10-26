package hooks

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// #177: the .spinclass guard also covers moxy tools, by the same path
// arguments the #176 rewrite reads (pathArgKeys).
func TestMoxyToolsTouchingSpinclassDirAreDenied(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	inWorktree := filepath.Join(worktree, ".spinclass", "env")
	inMain := filepath.Join(main, ".spinclass", "state.json")

	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"folio_write file_path", "mcp__plugin_moxy_moxy__folio_write", map[string]any{"file_path": inWorktree, "content": "x"}},
		{"folio_read file_path", "mcp__plugin_moxy_moxy__folio_read", map[string]any{"file_path": inWorktree}},
		{"folio_cp destination in the parent checkout", "mcp__plugin_moxy_moxy__folio_cp", map[string]any{"source": filepath.Join(worktree, "a"), "destination": inMain}},
		{"folio_rm space-separated paths", "mcp__plugin_moxy_moxy__folio_rm", map[string]any{"paths": filepath.Join(worktree, "a") + " " + inWorktree}},
		{"grit_stage paths array", "mcp__plugin_moxy_moxy__grit_stage", map[string]any{"subcommand": "add", "paths": []any{inWorktree}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			if err := Run(bytes.NewReader(makeInput(tc.tool, tc.args, worktree)), &stdout, main, worktree, false); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if stdout.Len() == 0 {
				t.Fatal("expected a deny, got no output")
			}
			decision, reason := parseHookDecision(t, stdout.Bytes())
			if decision != "deny" || !strings.Contains(reason, ".spinclass") {
				t.Errorf("decision %q reason %q, want a .spinclass deny", decision, reason)
			}
		})
	}
}

// A moxy tool's path outside .spinclass, or a relative value (which this hook
// cannot resolve against the tool's own cwd), is not denied by the guard.
func TestMoxyToolsOutsideSpinclassDirAreNotDenied(t *testing.T) {
	worktree := t.TempDir()
	for _, args := range []map[string]any{
		{"file_path": filepath.Join(worktree, "ok.go"), "content": "x"},
		{"file_path": ".spinclass/env", "content": "x"},
	} {
		var stdout bytes.Buffer
		if err := Run(bytes.NewReader(makeInput("mcp__plugin_moxy_moxy__folio_write", args, worktree)), &stdout, "", worktree, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stdout.Len() == 0 {
			continue
		}
		if decision, reason := parseHookDecision(t, stdout.Bytes()); decision == "deny" && strings.Contains(reason, ".spinclass") {
			t.Errorf("%v: unexpected .spinclass deny: %q", args, reason)
		}
	}
}
