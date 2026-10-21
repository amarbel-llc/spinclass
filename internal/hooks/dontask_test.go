package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const dontAskSuffix = " — denied because this session cannot answer prompts (permission mode dontAsk)"

func runWithMode(t *testing.T, mode, tool string, toolInput map[string]any) (string, string) {
	t.Helper()
	cwd := t.TempDir()
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       tool,
		"tool_input":      toolInput,
		"cwd":             cwd,
	}
	if mode != "" {
		payload["permission_mode"] = mode
	}
	data, _ := json.Marshal(payload)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(data), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return parseHookDecision(t, stdout.Bytes())
}

// Under permission_mode dontAsk every ask becomes a deny whose reason is the
// original reason plus the suffix; other modes and an absent field keep the ask.
func TestDontAskTurnsAsksIntoDenies(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "7a7a7a7a-0000-4000-8000-000000000000") // known principal, no session
	asks := []struct {
		name, tool string
		input      map[string]any
	}{
		{"grit store, unresolved session", "mcp__plugin_spinclass_spinclass__grit_commit", map[string]any{"message": "m"}},
		{"non-grit always-ask", closeChildSessionToolName, map[string]any{"child": "x", "force": true}},
	}
	for _, a := range asks {
		t.Run(a.name, func(t *testing.T) {
			decision, askReason := runWithMode(t, "default", a.tool, a.input)
			if decision != "ask" || askReason == "" {
				t.Fatalf("precondition: default mode got %q (%q), want ask", decision, askReason)
			}
			for _, mode := range []string{"", "plan", "acceptEdits", "bypassPermissions"} {
				if d, r := runWithMode(t, mode, a.tool, a.input); d != "ask" || r != askReason {
					t.Errorf("mode %q: got %q %q, want unchanged ask %q", mode, d, r, askReason)
				}
			}
			d, r := runWithMode(t, "dontAsk", a.tool, a.input)
			if d != "deny" || r != askReason+dontAskSuffix {
				t.Errorf("dontAsk: got %q %q, want deny %q", d, r, askReason+dontAskSuffix)
			}
		})
	}
}

func TestDontAskLeavesAllowAlone(t *testing.T) {
	d, r := runWithMode(t, "dontAsk", listToolName, map[string]any{})
	if d != "allow" || strings.Contains(r, "dontAsk") {
		t.Errorf("got %q %q, want plain allow", d, r)
	}
}
