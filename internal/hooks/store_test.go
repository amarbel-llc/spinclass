package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/confinement"
)

const testUnit = "clown-agent-test.service"

// setEnvOrUnset sets name to value for the test, or unsets it when value is
// empty (as in a real worker, #392, or a hook outside a unit).
func setEnvOrUnset(t *testing.T, name, value string) {
	t.Helper()
	t.Setenv(name, value)
	if value == "" {
		_ = os.Unsetenv(name)
	}
}

// storeDecisionFor runs a grit stash PreToolUse hook call carrying the given
// hook session_id (empty omits it) with CLOWN_SESSION_ID set to env and
// CLOWN_UNIT to unit (empty unsets either).
func storeDecisionFor(t *testing.T, env, unit, hookSessionID, cwd string, toolInput map[string]any) string {
	t.Helper()
	setEnvOrUnset(t, "CLOWN_SESSION_ID", env)
	setEnvOrUnset(t, "CLOWN_UNIT", unit)
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "mcp__plugin_spinclass_spinclass__grit_stash",
		"tool_input":      toolInput,
		"cwd":             cwd,
	}
	if hookSessionID != "" {
		payload["session_id"] = hookSessionID
	}
	data, _ := json.Marshal(payload)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(data), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	return decision
}

// CLOWN_SESSION_ID is THE principal when set (a differing hook session_id is
// only logged); with it unset (a real worker) the hook session_id is the
// fallback principal. A recorded principal resolves to its session and Classify
// decides (own worktree allows, foreign repo_path asks), with or without
// CLOWN_UNIT. Outside a unit an unrecorded principal or none at all resolves
// the session from the hook's cwd (#415), so Classify decides as well; inside
// one it defers to the serve (FDR 0035 "CLOWN_UNIT").
func TestStoreToolResolvesTheHookSessionID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "driver/key")
	const hookID = "5e5e5e5e-0000-4000-8000-000000000000"
	const otherID = "6f6f6f6f-0000-4000-8000-000000000000"
	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	// A principal resolves to its session only through the frontend mapping,
	// never through a record's own principal field.
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := confinement.RecordUnconfinedPrincipal(stateDir, hookID, st.Key(), st.WorktreePath); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	drop := func(repoPath string) map[string]any {
		return map[string]any{"subcommand": "drop", "stash_ref": "stash@{0}", "repo_path": repoPath}
	}

	for _, tc := range []struct {
		name, env, hookID, repoPath, want, wantInUnit string
	}{
		{"recorded hook id, foreign repo_path", "", hookID, foreign, "ask", "ask"},
		{"recorded hook id, own worktree", "", hookID, wt, "allow", "allow"},
		{"unrecorded hook id, resolved from cwd", "", otherID, wt, "allow", "allow"},
		{"unrecorded hook id, resolved from cwd, foreign repo_path", "", otherID, foreign, "ask", "allow"},
		{"env not recorded, differing hook id recorded: env wins, resolved from cwd", otherID, hookID, wt, "allow", "allow"},
		{"env recorded, differing hook id: env resolves, own worktree", hookID, otherID, wt, "allow", "allow"},
		{"env recorded, differing hook id: env resolves, foreign repo_path", hookID, otherID, foreign, "ask", "ask"},
		{"env recorded, no hook id, own worktree", hookID, "", wt, "allow", "allow"},
		{"no principal at all", "", "", foreign, "ask", "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := storeDecisionFor(t, tc.env, "", tc.hookID, wt, drop(tc.repoPath)); got != tc.want {
				t.Errorf("outside a unit: got %q, want %q", got, tc.want)
			}
			if got := storeDecisionFor(t, tc.env, testUnit, tc.hookID, wt, drop(tc.repoPath)); got != tc.wantInUnit {
				t.Errorf("in a unit: got %q, want %q", got, tc.wantInUnit)
			}
		})
	}
}

// A confined worker's hook (CLOWN_UNIT set; clown#289 sets CLOWN_SESSION_ID in
// the unit, where the hook cannot resolve the session) passes a non-force grit
// call with a foreign repo_path through to the serve, which enforces ownership.
// It must not ask, and so must not be denied under dontAsk (the f154e30
// regression). The same principal outside a unit asks, and is denied under
// dontAsk. Force asks in both, and is denied under dontAsk.
func TestHookWithKnownButUnresolvablePrincipal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	foreign := t.TempDir()
	for _, tc := range []struct {
		name, tool, mode, unit, want string
		input                        map[string]any
	}{
		{"in unit: commit, default mode", "commit", "default", testUnit, "allow", map[string]any{"message": "m", "repo_path": foreign}},
		{"in unit: commit, dontAsk", "commit", "dontAsk", testUnit, "allow", map[string]any{"message": "m", "repo_path": foreign}},
		{"in unit: stash save, dontAsk", "stash", "dontAsk", testUnit, "allow", map[string]any{"subcommand": "save", "repo_path": foreign}},
		{"in unit: force, default mode", "branch", "default", testUnit, "ask", map[string]any{"subcommand": "delete", "name": "x", "force": true, "repo_path": foreign}},
		{"in unit: force, dontAsk", "branch", "dontAsk", testUnit, "deny", map[string]any{"subcommand": "delete", "name": "x", "force": true, "repo_path": foreign}},
		{"no unit: commit, default mode", "commit", "default", "", "ask", map[string]any{"message": "m", "repo_path": foreign}},
		{"no unit: commit, dontAsk", "commit", "dontAsk", "", "deny", map[string]any{"message": "m", "repo_path": foreign}},
		{"no unit: force, dontAsk", "branch", "dontAsk", "", "deny", map[string]any{"subcommand": "delete", "name": "x", "force": true, "repo_path": foreign}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLOWN_SESSION_ID", "7a7a7a7a-0000-4000-8000-000000000000")
			setEnvOrUnset(t, "CLOWN_UNIT", tc.unit)
			data, _ := json.Marshal(map[string]any{
				"hook_event_name": "PreToolUse",
				"tool_name":       "mcp__plugin_spinclass_spinclass__grit_" + tc.tool,
				"tool_input":      tc.input,
				"cwd":             foreign,
				"permission_mode": tc.mode,
			})
			var stdout bytes.Buffer
			if err := Run(bytes.NewReader(data), &stdout, "", foreign, false); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			decision, reason := parseHookDecision(t, stdout.Bytes())
			if decision != tc.want || reason == "" {
				t.Errorf("got %q (%q), want %q", decision, reason, tc.want)
			}
		})
	}
}

// A grit store call from a caller with no principal visible to the hook
// (CLOWN_SESSION_ID empty) is deferred to the serve inside a unit and asks
// outside one; force asks in both. Every decision carries a reason.
func TestStoreToolWithoutSession(t *testing.T) {
	setEnvOrUnset(t, "CLOWN_SESSION_ID", "")
	for _, tc := range []struct {
		unit       string
		subcommand string
		force      bool
		want       string
	}{
		{testUnit, "log", false, "allow"},
		{testUnit, "delete", false, "allow"},
		{testUnit, "delete", true, "ask"},
		{"", "log", false, "ask"},
		{"", "delete", false, "ask"},
		{"", "delete", true, "ask"},
	} {
		setEnvOrUnset(t, "CLOWN_UNIT", tc.unit)
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
			t.Errorf("unit=%q %s force=%v: got decision %q reason %q, want %s with a reason", tc.unit, tc.subcommand, tc.force, decision, reason, tc.want)
		}
	}
}
