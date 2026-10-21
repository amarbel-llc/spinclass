package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
)

// storeDecisionFor runs a grit stash PreToolUse hook call carrying the given
// hook session_id (empty omits it) with CLOWN_SESSION_ID set to env (empty
// unsets it, as in a real worker, #392).
func storeDecisionFor(t *testing.T, env, hookSessionID, cwd string, toolInput map[string]any) string {
	t.Helper()
	t.Setenv("CLOWN_SESSION_ID", env)
	if env == "" {
		_ = os.Unsetenv("CLOWN_SESSION_ID")
	}
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
// decides (own worktree allows, foreign repo_path asks); an unrecorded one
// asks; no principal at all defers to the serve.
func TestStoreToolResolvesTheHookSessionID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "driver/key")
	const hookID = "5e5e5e5e-0000-4000-8000-000000000000"
	const otherID = "6f6f6f6f-0000-4000-8000-000000000000"
	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	st.Principal = hookID
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	drop := func(repoPath string) map[string]any {
		return map[string]any{"subcommand": "drop", "stash_ref": "stash@{0}", "repo_path": repoPath}
	}

	for _, tc := range []struct {
		name, env, hookID, repoPath, want string
	}{
		{"recorded hook id, foreign repo_path", "", hookID, foreign, "ask"},
		{"recorded hook id, own worktree", "", hookID, wt, "allow"},
		{"unrecorded hook id", "", otherID, wt, "ask"},
		{"env not recorded, differing hook id recorded: env wins, unresolved asks", otherID, hookID, wt, "ask"},
		{"env recorded, differing hook id: env resolves, own worktree", hookID, otherID, wt, "allow"},
		{"env recorded, differing hook id: env resolves, foreign repo_path", hookID, otherID, foreign, "ask"},
		{"env recorded, no hook id, own worktree", hookID, "", wt, "allow"},
		{"no principal at all defers", "", "", foreign, "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := storeDecisionFor(t, tc.env, tc.hookID, wt, drop(tc.repoPath)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A grit store call from a caller with no principal visible to the hook
// (CLOWN_SESSION_ID empty, a confined caller) is deferred to the serve (allow,
// with the reason saying so); force still prompts.
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
