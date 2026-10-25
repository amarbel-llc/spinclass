package perms

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
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

		// A forced restart kills the current turn and any running job.
		{"restart-this without force is silent", restartThisSessionTool, map[string]any{"reason": "x"}, false},
		{"restart-this nil input is silent", restartThisSessionTool, nil, false},
		{"restart-this force=false is silent", restartThisSessionTool, map[string]any{"force": false}, false},
		{"restart-this null force is silent", restartThisSessionTool, map[string]any{"force": nil}, false},
		{"restart-this force=true asks", restartThisSessionTool, map[string]any{"force": true}, true},
		{"restart-this string force asks", restartThisSessionTool, map[string]any{"force": "true"}, true},
		{"restart-this string false still asks", restartThisSessionTool, map[string]any{"force": "false"}, true},
		{"restart-this numeric force asks", restartThisSessionTool, map[string]any{"force": float64(1)}, true},
		{"restart-child without force is silent", restartChildSessionTool, map[string]any{"child": "r/b"}, false},
		{"restart-child force=false is silent", restartChildSessionTool, map[string]any{"child": "r/b", "force": false}, false},
		{"restart-child null force is silent", restartChildSessionTool, map[string]any{"force": nil}, false},
		{"restart-child force=true asks", restartChildSessionTool, map[string]any{"child": "r/b", "force": true}, true},
		{"restart-child string force asks", restartChildSessionTool, map[string]any{"force": "true"}, true},
		{"restart-child numeric force asks", restartChildSessionTool, map[string]any{"force": float64(1)}, true},

		{"grant always asks", grantSessionHandleTool, nil, true},
		{"grant asks regardless of rights named", grantSessionHandleTool, map[string]any{"rights": "observe"}, true},

		{"release always asks", releaseSessionHandleTool, nil, true},
		{"release asks regardless of args", releaseSessionHandleTool, map[string]any{"child": "repo/branch"}, true},

		{"list-handles without accept is silent", listHandlesTool, nil, false},
		{"list-handles with accept=false is silent", listHandlesTool, map[string]any{"accept": false}, false},
		{"list-handles with null accept is silent", listHandlesTool, map[string]any{"accept": nil}, false},
		{"list-handles with accept=true asks", listHandlesTool, map[string]any{"accept": true}, true},
		{"list-handles with string accept asks", listHandlesTool, map[string]any{"accept": "true"}, true},
		{"list-handles with numeric accept asks", listHandlesTool, map[string]any{"accept": float64(1)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, ask := AlwaysAsk(tt.toolName, tt.toolInput, "/cwd")
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

// A confined spawn is the one carve-out from the spawn floor (#382). The
// verdict comes from the installed SpawnConfined seam, which receives the tool
// input and cwd; with no seam installed, or one that says unconfined, spawn
// still asks.
func TestAlwaysAskSpawnCarveOutForConfined(t *testing.T) {
	prev := SpawnConfined
	t.Cleanup(func() { SpawnConfined = prev })

	var gotInput map[string]any
	var gotCwd string
	SpawnConfined = func(in map[string]any, cwd string) bool {
		gotInput, gotCwd = in, cwd
		return in["repo"] == "confined-repo"
	}

	if reason, ask := AlwaysAsk(spawnSessionTool, map[string]any{"repo": "confined-repo"}, "/driver"); ask || reason != "" {
		t.Errorf("a confined spawn should not ask, got ask=%v reason=%q", ask, reason)
	}
	if gotCwd != "/driver" || gotInput["repo"] != "confined-repo" {
		t.Errorf("the seam must see the invocation's input and cwd, got %v %q", gotInput, gotCwd)
	}
	if _, ask := AlwaysAsk(spawnSessionTool, map[string]any{"repo": "other"}, "/driver"); !ask {
		t.Error("an unconfined spawn must still ask")
	}

	SpawnConfined = nil
	if _, ask := AlwaysAsk(spawnSessionTool, map[string]any{"repo": "confined-repo"}, "/driver"); !ask {
		t.Error("with no seam installed spawn must ask")
	}
}

// A spawn that opts out of confinement ALWAYS asks, even when the seam would
// call it confined, until a session can be blessed to run unconfined children
// (stage 2, not built). Any doubt about `unconfined` or `confined` asks too:
// a non-bool is never read as "not set".
func TestAlwaysAskSpawnUnconfinedRequest(t *testing.T) {
	prev := SpawnConfined
	t.Cleanup(func() { SpawnConfined = prev })
	SpawnConfined = func(map[string]any, string) bool { return true }

	for _, tc := range []struct {
		name    string
		in      map[string]any
		wantAsk bool
	}{
		{"unconfined=true asks", map[string]any{"unconfined": true}, true},
		{"unconfined=true asks even with confined=true", map[string]any{"unconfined": true, "confined": true}, true},
		{"string unconfined asks", map[string]any{"unconfined": "true"}, true},
		{"string false unconfined asks", map[string]any{"unconfined": "false"}, true},
		{"numeric unconfined asks", map[string]any{"unconfined": float64(1)}, true},
		{"unconfined=false is not an opt-out", map[string]any{"unconfined": false}, false},
		{"null unconfined is not an opt-out", map[string]any{"unconfined": nil}, false},
		{"absent unconfined is not an opt-out", map[string]any{"confined": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, ask := AlwaysAsk(spawnSessionTool, tc.in, "/driver")
			if ask != tc.wantAsk {
				t.Fatalf("ask = %v, want %v", ask, tc.wantAsk)
			}
			if ask && !strings.Contains(reason, "unconfined") {
				t.Errorf("the reason must name the opt-out, got %q", reason)
			}
		})
	}
}

// The carve-out is spawn-only: another tool never consults the seam.
func TestAlwaysAskSeamIgnoredForOtherTools(t *testing.T) {
	prev := SpawnConfined
	t.Cleanup(func() { SpawnConfined = prev })
	SpawnConfined = func(map[string]any, string) bool { return true }

	if _, ask := AlwaysAsk(grantSessionHandleTool, nil, "/cwd"); !ask {
		t.Error("a confined-spawn verdict must not loosen grant-session-handle")
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

// The forced-restart prompt names what a forced restart destroys.
func TestAlwaysAskForcedRestartReasonNamesTheCost(t *testing.T) {
	for _, tool := range []string{restartThisSessionTool, restartChildSessionTool} {
		reason, ask := AlwaysAsk(tool, map[string]any{"force": true}, "/cwd")
		if !ask || !strings.Contains(reason, "turn") || !strings.Contains(reason, "job") {
			t.Errorf("%s: want an ask whose reason names the turn and the job, got ask=%v %q", tool, ask, reason)
		}
	}
}

// A tier rule cannot buy its way past the floor for a forced restart either.
func TestCheckNeverAutoApprovesForcedRestart(t *testing.T) {
	for _, tool := range []string{restartThisSessionTool, restartChildSessionTool} {
		tiersDir := t.TempDir()
		data, _ := json.MarshalIndent(Tier{Allow: []string{tool}}, "", "  ")
		testfs.MustWriteFile(t, filepath.Join(tiersDir, "global.json"), data, 0o644)

		run := func(in map[string]any) string {
			input, _ := json.Marshal(map[string]any{
				"tool_name": tool, "tool_input": in,
				"cwd": "/home/user/eng/worktrees/myrepo/feature",
			})
			var out bytes.Buffer
			if err := RunCheck(bytes.NewReader(input), &out, tiersDir); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			return out.String()
		}
		if got := run(map[string]any{"force": true}); got != "" {
			t.Errorf("%s: a tier rule auto-approved a forced restart: %q", tool, got)
		}
		if got := run(map[string]any{"reason": "x"}); got == "" {
			t.Errorf("%s: an allow-listed unforced restart was not auto-approved", tool)
		}
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
