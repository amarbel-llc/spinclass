package hooks

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// gritDecisionFor runs one grit store tool call through the PreToolUse hook as
// an unconfined caller with no principal at all (no CLOWN_SESSION_ID, no hook
// session_id, no frontend mapping) and with CLOWN_UNIT set to unit (empty
// unsets it).
func gritDecisionFor(t *testing.T, unit, tool, cwd string, input map[string]any) string {
	t.Helper()
	setEnvOrUnset(t, "CLOWN_SESSION_ID", "")
	setEnvOrUnset(t, "CLOWN_UNIT", unit)
	data, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "mcp__plugin_spinclass_spinclass__grit_" + tool,
		"tool_input":      input,
		"cwd":             cwd,
	})
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(data), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, reason := parseHookDecision(t, stdout.Bytes())
	if reason == "" {
		t.Errorf("grit %s %v: decision %q carries no reason", tool, input, decision)
	}
	return decision
}

// #415: the default permission of every grit store subcommand for an
// UNCONFINED session, which has no principal mapping and is resolved from the
// hook's cwd. This is the table in spinclass-grit(7) "DEFAULT PERMISSIONS";
// keep the two in step. Everything that stays in the session's worktree, its
// own branches (feature, feature--*), its stash shelf and its own tags
// (feature--*) allows; the default branch, other branches, force and delete
// flags, other worktrees and clones elsewhere ask. Remote talk about the
// session's own refs allows (FDR 0036, kept for unconfined sessions).
//
// The same calls from a cwd in no session all ask, and inside a confined unit
// every non-force call is deferred to the serve.
func TestGritDefaultPermissionsForAnUnconfinedSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "")
	// other sits below the session's own commit, so a restack of feature moves
	// no foreign branch (one pointing into the range would ask).
	testgit.MustGit(t, wt, "branch", "other")
	testgit.MustGit(t, wt, "commit", "--allow-empty", "-m", "session work")
	testgit.MustGit(t, wt, "branch", "feature--x")
	testgit.MustGit(t, wt, "remote", "add", "origin", repo)
	side := filepath.Join(repo, ".worktrees", "feature--side")
	testgit.MustWorktreeAdd(t, repo, side, "feature--side")
	elsewhere := t.TempDir()

	const allow, ask = "allow", "ask"
	type args = map[string]any
	for _, tc := range []struct {
		tool  string
		input args
		want  string
	}{
		// Reads.
		{"stage", args{"subcommand": "status"}, allow},
		{"stage", args{"subcommand": "diff"}, allow},
		{"branch", args{"subcommand": "log"}, allow},
		{"branch", args{"subcommand": "rev-parse", "ref": "HEAD"}, allow},
		{"branch", args{"subcommand": "diff", "ref": "master"}, allow},
		{"tag", args{"subcommand": "list"}, allow},
		{"tag", args{"subcommand": "verify", "name": "v1"}, allow},
		{"worktree", args{"subcommand": "list"}, allow},
		{"remote", args{"subcommand": "fetch"}, allow},
		{"remote", args{"subcommand": "fetch", "remote": "https://example.invalid/x"}, ask},

		// The index and working tree.
		{"stage", args{"subcommand": "add", "paths": []any{"f"}}, allow},
		{"stage", args{"subcommand": "unstage"}, allow},
		{"stage", args{"subcommand": "rm", "paths": []any{"f"}}, allow},
		{"stage", args{"subcommand": "rm", "paths": []any{"f"}, "force": true}, ask},
		{"stage", args{"subcommand": "mv", "source": "f", "destination": "g"}, allow},
		{"commit", args{"message": "m"}, allow},
		{"commit", args{"amend": true}, allow},
		{"merge", args{"branch": "master"}, allow},

		// The stash shelf (private to the worktree).
		{"stash", args{"subcommand": "list"}, allow},
		{"stash", args{"subcommand": "save"}, allow},
		{"stash", args{"subcommand": "apply"}, allow},
		{"stash", args{"subcommand": "drop", "stash_ref": "stash@{0}"}, allow},
		{"stash", args{"subcommand": "save", "repo_path": elsewhere}, ask},

		// Branches.
		{"branch", args{"subcommand": "create", "name": "feature--y"}, allow},
		{"branch", args{"subcommand": "create", "name": "other2"}, ask},
		{"branch", args{"subcommand": "create", "name": "master"}, ask},
		{"branch", args{"subcommand": "checkout", "ref": "feature--x"}, allow},
		{"branch", args{"subcommand": "checkout", "ref": "master"}, ask},
		{"branch", args{"subcommand": "checkout", "new_branch": true, "name": "feature--z"}, allow},
		{"branch", args{"subcommand": "checkout", "paths": []any{"f"}}, allow},
		{"branch", args{"subcommand": "delete", "name": "feature--x"}, allow},
		{"branch", args{"subcommand": "delete", "name": "other"}, ask},
		{"branch", args{"subcommand": "delete", "name": "master"}, ask},
		{"branch", args{"subcommand": "delete", "name": "feature--x", "force": true}, ask},
		{"branch", args{"subcommand": "reset-soft"}, allow},
		{"branch", args{"subcommand": "reset-hard", "ref": "HEAD~1"}, allow},
		{"branch", args{"subcommand": "rebase", "upstream": "master"}, allow},
		{"branch", args{"subcommand": "rebase", "upstream": "feature", "rebase_branch": "master"}, ask},
		{"branch", args{"subcommand": "restack", "root": "feature", "onto": "master"}, allow},
		{"branch", args{"subcommand": "restack", "root": "other", "onto": "master"}, ask},
		{"branch", args{"subcommand": "cherry-pick", "commits": []any{"other"}}, allow},
		{"branch", args{"subcommand": "revert", "commits": []any{"HEAD"}}, allow},

		// Tags.
		{"tag", args{"subcommand": "create", "name": "feature--v1"}, allow},
		{"tag", args{"subcommand": "create", "name": "v1"}, ask},
		{"tag", args{"subcommand": "create", "name": "feature--v1", "force": true}, ask},
		{"tag", args{"subcommand": "delete", "name": "feature--v1"}, allow},
		{"tag", args{"subcommand": "delete", "name": "v1"}, ask},
		{"tag", args{"subcommand": "push", "name": "feature--v1"}, allow},
		{"tag", args{"subcommand": "push", "name": "v1"}, ask},
		{"tag", args{"subcommand": "push", "name": "feature--v1", "delete": true}, ask},

		// Remote talk.
		{"remote", args{"subcommand": "pull"}, allow},
		{"remote", args{"subcommand": "push"}, allow},
		{"remote", args{"subcommand": "push", "branch": "feature--x"}, allow},
		{"remote", args{"subcommand": "push", "branch": "master"}, ask},
		{"remote", args{"subcommand": "push", "remote_branch": "master"}, ask},
		{"remote", args{"subcommand": "push", "branch": "feature", "delete": true}, ask},
		{"remote", args{"subcommand": "push", "force_with_lease": true}, allow},
		{"remote", args{"subcommand": "push", "force": true}, ask},
		{"remote", args{"subcommand": "push-stack", "branches": []any{"feature", "feature--x"}}, allow},
		{"remote", args{"subcommand": "push-stack", "branches": []any{"feature", "master"}}, ask},

		// Worktrees and clones.
		{"worktree", args{"subcommand": "remove", "path": side}, allow},
		{"worktree", args{"subcommand": "remove", "path": wt}, ask},
		{"worktree", args{"subcommand": "remove", "path": repo}, ask},
		{"clone", args{"source": repo, "destination": ".tmp/clone"}, allow},
		{"clone", args{"source": repo, "destination": filepath.Join(elsewhere, "clone")}, ask},
	} {
		if got := gritDecisionFor(t, "", tc.tool, wt, tc.input); got != tc.want {
			t.Errorf("unconfined, cwd in the session: grit %s %v: got %q, want %q", tc.tool, tc.input, got, tc.want)
		}
		if got := gritDecisionFor(t, "", tc.tool, elsewhere, tc.input); got != ask {
			t.Errorf("unconfined, cwd in no session: grit %s %v: got %q, want ask", tc.tool, tc.input, got)
		}
		wantInUnit := allow
		if tc.input["force"] == true {
			wantInUnit = ask
		}
		if got := gritDecisionFor(t, testUnit, tc.tool, wt, tc.input); got != wantInUnit {
			t.Errorf("confined unit: grit %s %v: got %q, want %q", tc.tool, tc.input, got, wantInUnit)
		}
	}
}
