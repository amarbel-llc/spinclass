package hooks

import (
	"bytes"
	"encoding/json"
	"testing"

	"code.linenisgreat.com/spinclass/internal/charlie/perms"
)

func runDontAskPayload(t *testing.T, mode, agentID, tool string, toolInput map[string]any) []string {
	t.Helper()
	var names []string
	prev := perms.CountMetric
	perms.CountMetric = func(_, name string) { names = append(names, name) }
	t.Cleanup(func() { perms.CountMetric = prev })

	cwd := t.TempDir()
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       tool,
		"tool_input":      toolInput,
		"cwd":             cwd,
		"permission_mode": mode,
	}
	if agentID != "" {
		payload["agent_id"] = agentID
	}
	data, _ := json.Marshal(payload)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(data), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return names
}

func TestDontAskConversionCountsUnattendedDeny(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "7a7a7a7a-0000-4000-8000-000000000000")
	in := map[string]any{"child": "x", "force": true}

	main := runDontAskPayload(t, "dontAsk", "", closeChildSessionToolName, in)
	if len(main) != 1 || main[0] != "permission.unknown.unattended_deny.main.mcp__plugin_spinclass_spinclass__close-child-session" {
		// cwd is a tempdir with no repos/worktrees segment, hence "unknown"
		t.Errorf("main = %v", main)
	}
	sub := runDontAskPayload(t, "dontAsk", "agent-1", closeChildSessionToolName, in)
	if len(sub) != 1 || sub[0] != "permission.unknown.unattended_deny.subagent.mcp__plugin_spinclass_spinclass__close-child-session" {
		t.Errorf("sub = %v", sub)
	}
}

func TestNoUnattendedDenyCountOutsideDontAskOrWithoutAsk(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "7a7a7a7a-0000-4000-8000-000000000000")
	if got := runDontAskPayload(t, "default", "", closeChildSessionToolName, map[string]any{"child": "x", "force": true}); len(got) != 0 {
		t.Errorf("default mode counted %v", got)
	}
	if got := runDontAskPayload(t, "dontAsk", "", listToolName, map[string]any{}); len(got) != 0 {
		t.Errorf("allow verdict counted %v", got)
	}
}
