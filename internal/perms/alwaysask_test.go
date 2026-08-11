package perms

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testfs"
)

// AlwaysAsk judges an invocation, not a tool. spawn/fork are unconditional;
// close-child-session turns on `force`, which is what discards a child's
// unmerged commits.
//
// The non-boolean rows are the point of the table. `toolInput["force"].(bool)`
// yields false for every one of them, so a naive read would auto-approve
// exactly the invocation this floor exists to catch. Anything that is not
// definitively "no force" must ask.
func TestAlwaysAsk(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		toolInput map[string]any
		wantAsk   bool
	}{
		{"spawn always asks", spawnSessionTool, nil, true},
		{"spawn asks regardless of args", spawnSessionTool, map[string]any{"force": false}, true},

		{"reap without force is silent", closeChildSessionTool, map[string]any{"child": "repo/branch"}, false},
		{"explicit force=false is silent", closeChildSessionTool, map[string]any{"force": false}, false},
		{"null force is silent", closeChildSessionTool, map[string]any{"force": nil}, false},
		{"nil input is silent", closeChildSessionTool, nil, false},

		{"force=true asks", closeChildSessionTool, map[string]any{"force": true}, true},
		{"string force asks", closeChildSessionTool, map[string]any{"force": "true"}, true},
		{"numeric force asks", closeChildSessionTool, map[string]any{"force": float64(1)}, true},
		{"string false still asks", closeChildSessionTool, map[string]any{"force": "false"}, true},

		{"unrelated tool is silent", "Read", map[string]any{"force": true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, ask := AlwaysAsk(tt.toolName, tt.toolInput)
			if ask != tt.wantAsk {
				t.Fatalf("AlwaysAsk(%q, %v) ask = %v, want %v",
					tt.toolName, tt.toolInput, ask, tt.wantAsk)
			}
			if ask && reason == "" {
				t.Error("an asking verdict must carry a reason for the prompt")
			}
			if !ask && reason != "" {
				t.Errorf("a non-asking verdict should carry no reason, got %q", reason)
			}
		})
	}
}

// A tier rule cannot buy its way past the floor. This is the mirror half of the
// rule: the PreToolUse hook's `ask` is the primary enforcement, but RunCheck
// refuses independently so the outcome does not depend on hook ordering.
func TestCheckNeverAutoApprovesForcedReap(t *testing.T) {
	tiersDir := t.TempDir()
	tier := Tier{Allow: []string{closeChildSessionTool}}
	data, _ := json.MarshalIndent(tier, "", "  ")
	testfs.MustWriteFile(t, filepath.Join(tiersDir, "global.json"), data, 0o644)

	input, _ := json.Marshal(map[string]any{
		"tool_name":  closeChildSessionTool,
		"tool_input": map[string]any{"child": "repo/branch", "force": true},
		"cwd":        "/home/user/eng/worktrees/myrepo/feature",
	})

	var out bytes.Buffer
	if err := RunCheck(bytes.NewReader(input), &out, tiersDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a tier rule auto-approved a forced reap, so unmerged work would be "+
			"discarded with no prompt; got %q", out.String())
	}
}

// The other half: allow-listing the tool still works for the safe case. Without
// this the split is pointless — the user would be back to all-or-nothing, just
// with "nothing" as the only option.
func TestCheckAutoApprovesUnforcedReapWhenListed(t *testing.T) {
	tiersDir := t.TempDir()
	tier := Tier{Allow: []string{closeChildSessionTool}}
	data, _ := json.MarshalIndent(tier, "", "  ")
	testfs.MustWriteFile(t, filepath.Join(tiersDir, "global.json"), data, 0o644)

	input, _ := json.Marshal(map[string]any{
		"tool_name":  closeChildSessionTool,
		"tool_input": map[string]any{"child": "repo/branch"},
		"cwd":        "/home/user/eng/worktrees/myrepo/feature",
	})

	var out bytes.Buffer
	if err := RunCheck(bytes.NewReader(input), &out, tiersDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Len() == 0 {
		t.Error("an explicitly allow-listed clean reap was not auto-approved")
	}
}
