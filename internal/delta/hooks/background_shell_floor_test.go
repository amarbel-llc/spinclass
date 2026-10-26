package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// clown#330: a Monitor / background-shell tool is open-world bash that streams
// output back to the session, an escape from the permission profile. Observed
// 2026-10-26: an unattended default-profile worker's tool list has no such tool,
// but a future claude release or MCP server could add one; under dontAsk the hook
// is the fail-closed floor and denies the known names outright.
const backgroundShellMarker = "background shell"

func TestDontAskDeniesBackgroundShellToolNames(t *testing.T) {
	for _, tool := range []string{"Monitor", "BashOutput", "KillShell", "KillBash", "BashBackground"} {
		t.Run(tool, func(t *testing.T) {
			d, r := runWithMode(t, "dontAsk", tool, map[string]any{"command": "date"})
			if d != "deny" || !strings.Contains(r, backgroundShellMarker) {
				t.Fatalf("dontAsk %s: got %q %q, want a deny naming the background shell", tool, d, r)
			}
		})
	}
}

func TestDontAskDeniesPowerShellRunInBackground(t *testing.T) {
	d, r := runWithMode(t, "dontAsk", "PowerShell", map[string]any{"command": "date", "run_in_background": true})
	if d != "deny" || !strings.Contains(r, backgroundShellMarker) {
		t.Fatalf("got %q %q, want the background-shell deny", d, r)
	}
}

func TestDontAskDeniesBashRunInBackground(t *testing.T) {
	for name, in := range map[string]map[string]any{
		"bool true":   {"command": "date", "run_in_background": true},
		"string true": {"command": "date", "run_in_background": "true"},
		"mixed case":  {"command": "date", "run_in_background": " True "},
		"number":      {"command": "date", "run_in_background": 1},
		"string one":  {"command": "date", "run_in_background": "1"},
	} {
		t.Run(name, func(t *testing.T) {
			d, r := runWithMode(t, "dontAsk", "Bash", in)
			if d != "deny" || !strings.Contains(r, backgroundShellMarker) {
				t.Fatalf("got %q %q, want the background-shell deny", d, r)
			}
		})
	}
}

// The floor is narrow: other modes, an ordinary foreground Bash and the job
// platform's own tools (the wakeup channel is a plugin-declared monitor that the
// harness starts itself, so it never passes through PreToolUse at all) are not
// touched by it.
func TestBackgroundShellFloorLeavesEverythingElseAlone(t *testing.T) {
	for _, tc := range []struct {
		name, mode, tool string
		in               map[string]any
	}{
		{"default mode Monitor", "default", "Monitor", map[string]any{"command": "date"}},
		{"no mode Monitor", "", "Monitor", map[string]any{"command": "date"}},
		{"dontAsk foreground Bash", "dontAsk", "Bash", map[string]any{"command": "date", "run_in_background": false}},
		{"dontAsk foreground Bash, string false", "dontAsk", "Bash", map[string]any{"command": "date", "run_in_background": "false"}},
		{"dontAsk TaskStop (a worker may stop its own task; policy call, not this floor)", "dontAsk", "TaskStop", map[string]any{"task_id": "x"}},
		{"dontAsk ringmaster job tool", "dontAsk", "mcp__plugin_clown-builtin-jobs_ringmaster__job_status", map[string]any{"job_id": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			payload := map[string]any{"hook_event_name": "PreToolUse", "tool_name": tc.tool, "tool_input": tc.in, "cwd": cwd}
			if tc.mode != "" {
				payload["permission_mode"] = tc.mode
			}
			data, _ := json.Marshal(payload)
			var stdout bytes.Buffer
			if err := Run(bytes.NewReader(data), &stdout, "", cwd, false); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(stdout.String(), backgroundShellMarker) {
				t.Fatalf("the background-shell floor fired for %s: %s", tc.name, stdout.String())
			}
		})
	}
}
