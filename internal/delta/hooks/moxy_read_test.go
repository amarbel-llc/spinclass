package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

const (
	folioRead  = "mcp__plugin_moxy_moxy__folio_read"
	folioLs    = "mcp__plugin_moxy_moxy__folio_ls"
	folioGlob  = "mcp__plugin_moxy_moxy__folio_glob"
	folioTree  = "mcp__plugin_moxy_moxy__folio_tree"
	folioFile  = "mcp__plugin_moxy_moxy__folio_file-type"
	folioWrite = "mcp__plugin_moxy_moxy__folio_write"
	folioRm    = "mcp__plugin_moxy_moxy__folio_rm"
	moxyBranch = "mcp__plugin_moxy_moxy__grit_branch"
	moxyStage  = "mcp__plugin_moxy_moxy__grit_stage"
)

type moxyReadWorld struct {
	own, child, pending, other, collide, outside string
}

// newMoxyReadWorld lays out sibling worktrees and stands in for the session
// index: the caller ("me") owns `own`, holds an accepted handle on `child`, has
// only a pending grant on `pending`, and has nothing on `other`.
func newMoxyReadWorld(t *testing.T) moxyReadWorld {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := moxyReadWorld{
		own:     filepath.Join(root, "wt-a"),
		child:   filepath.Join(root, "wt-child"),
		pending: filepath.Join(root, "wt-pending"),
		other:   filepath.Join(root, "wt-other"),
		collide: filepath.Join(root, "wt-abc"),
		outside: filepath.Join(root, "elsewhere"),
	}
	for _, d := range []string{
		w.own, w.child, w.pending, w.other, w.collide, w.outside,
		filepath.Join(w.child, ".spinclass"), filepath.Join(w.own, ".spinclass"), filepath.Join(w.child, "sub"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		filepath.Join(w.own, "f.txt"), filepath.Join(w.child, "f.txt"), filepath.Join(w.child, "sub", "g.txt"),
		filepath.Join(w.pending, "f.txt"), filepath.Join(w.other, "f.txt"), filepath.Join(w.collide, "f.txt"),
		filepath.Join(w.outside, "f.txt"), filepath.Join(w.child, ".spinclass", "state.json"),
	} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(w.other, filepath.Join(w.own, "link-out")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOWN_SESSION_ID", "me")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := loadSessionStates
	t.Cleanup(func() { loadSessionStates = old })
	loadSessionStates = func() []session.State {
		mk := func(repo, branch, wt string) session.State {
			return session.State{RepoPath: repo, Branch: branch, WorktreePath: wt}
		}
		own := mk("/r/repo", "a", w.own)
		child := mk("/r/repo", "child", w.child)
		child.Holders = []string{"me"}
		pending := mk("/r/repo", "pending", w.pending)
		pending.PendingHandles = []string{"me"}
		pending.Holders = []string{"someone-else"}
		other := mk("/r/repo", "other", w.other)
		other.Holders = []string{"someone-else"}
		return []session.State{own, child, pending, other}
	}
	return w
}

func moxyReadDecision(t *testing.T, w moxyReadWorld, tool string, input map[string]any) (string, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": input, "cwd": w.own,
	})
	var out bytes.Buffer
	if err := Run(bytes.NewReader(payload), &out, "", w.own, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out.String()) == "" {
		return "", ""
	}
	return parseHookDecision(t, out.Bytes())
}

func TestMoxyReadAutoAllow(t *testing.T) {
	w := newMoxyReadWorld(t)
	tests := []struct {
		name       string
		tool       string
		input      map[string]any
		want       string // "allow" or "" for no decision
		wantReason string
	}{
		{"own folio_read", folioRead, map[string]any{"file_path": filepath.Join(w.own, "f.txt")}, "allow", "own worktree"},
		{"held child folio_read", folioRead, map[string]any{"file_path": filepath.Join(w.child, "sub", "g.txt")}, "allow", "held child"},
		{"held child key named", folioLs, map[string]any{"path": w.child}, "allow", "repo/child"},
		{"folio_ls own", folioLs, map[string]any{"path": w.own}, "allow", ""},
		{"folio_tree child", folioTree, map[string]any{"path": w.child}, "allow", ""},
		{"folio_file-type child", folioFile, map[string]any{"path": filepath.Join(w.child, "f.txt")}, "allow", ""},
		{"glob child", folioGlob, map[string]any{"path": w.child, "pattern": "**/*.txt"}, "allow", ""},
		{"glob without path uses cwd", folioGlob, map[string]any{"pattern": "*.txt"}, "allow", "own worktree"},
		{"ls without path uses cwd", folioLs, map[string]any{}, "allow", "own worktree"},
		{"read relative path resolves against cwd", folioRead, map[string]any{"file_path": "f.txt"}, "allow", "own worktree"},
		{"grit_branch log held child", moxyBranch, map[string]any{"subcommand": "log", "repo_path": w.child}, "allow", "held child"},
		{"grit_branch rev-parse", moxyBranch, map[string]any{"subcommand": "rev-parse", "repo_path": w.child}, "allow", ""},
		{"grit_branch diff with paths", moxyBranch, map[string]any{"subcommand": "diff", "repo_path": w.child, "paths": []any{"f.txt", "sub"}}, "allow", ""},
		{"grit_stage status child", moxyStage, map[string]any{"subcommand": "status", "repo_path": w.child}, "allow", ""},
		{"grit_stage diff no repo_path", moxyStage, map[string]any{"subcommand": "diff"}, "allow", "own worktree"},

		{"pending-handle child", folioRead, map[string]any{"file_path": filepath.Join(w.pending, "f.txt")}, "", ""},
		{"unrelated session", folioRead, map[string]any{"file_path": filepath.Join(w.other, "f.txt")}, "", ""},
		{"outside every root", folioRead, map[string]any{"file_path": filepath.Join(w.outside, "f.txt")}, "", ""},
		{"dotdot escape", folioRead, map[string]any{"file_path": filepath.Join(w.own, "..", "elsewhere", "f.txt")}, "", ""},
		{"symlink escape", folioRead, map[string]any{"file_path": filepath.Join(w.own, "link-out", "f.txt")}, "", ""},
		{"prefix collision", folioRead, map[string]any{"file_path": filepath.Join(w.collide, "f.txt")}, "", ""},
		{"other session's .spinclass under held child", folioRead, map[string]any{"file_path": filepath.Join(w.child, ".spinclass", "state.json")}, "", ""},
		{"other session's .spinclass dir listing", folioLs, map[string]any{"path": filepath.Join(w.child, ".spinclass")}, "", ""},
		{"nonexistent path under held child", folioRead, map[string]any{"file_path": filepath.Join(w.child, "nope", "x")}, "allow", ""},
		{"folio_write", folioWrite, map[string]any{"file_path": filepath.Join(w.own, "n.txt"), "content": "x"}, "", ""},
		{"folio_rm", folioRm, map[string]any{"paths": filepath.Join(w.own, "f.txt")}, "", ""},
		{"grit_branch checkout", moxyBranch, map[string]any{"subcommand": "checkout", "ref": "x", "repo_path": w.own}, "", ""},
		{"grit_branch reset-hard", moxyBranch, map[string]any{"subcommand": "reset-hard", "ref": "x", "repo_path": w.own}, "", ""},
		{"grit_branch missing subcommand", moxyBranch, map[string]any{"repo_path": w.own}, "", ""},
		{"grit_stage add", moxyStage, map[string]any{"subcommand": "add", "paths": []any{"f.txt"}, "repo_path": w.own}, "", ""},
		{"grit_branch log with escaping paths entry", moxyBranch, map[string]any{"subcommand": "log", "repo_path": w.own, "paths": []any{"../wt-other/f.txt"}}, "", ""},
		{"glob pattern with dotdot", folioGlob, map[string]any{"path": w.own, "pattern": "../wt-other/*"}, "", ""},
		{"glob pattern dotdot mid", folioGlob, map[string]any{"path": w.own, "pattern": "a/../../x"}, "", ""},
		{"glob absolute pattern", folioGlob, map[string]any{"path": w.own, "pattern": "/etc/*"}, "", ""},
		{"madder blob", folioRead, map[string]any{"file_path": "madder://blobs/blake2b256-abc"}, "", ""},
		{"non-string path", folioRead, map[string]any{"file_path": 3}, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := moxyReadDecision(t, w, tc.tool, tc.input)
			if got != tc.want {
				t.Fatalf("decision = %q (%q), want %q", got, reason, tc.want)
			}
			if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Errorf("reason %q lacks %q", reason, tc.wantReason)
			}
		})
	}
}

// The principal's accepted handle is the only authority: with no principal the
// held child is not readable.
func TestMoxyReadNoPrincipalHoldsNothing(t *testing.T) {
	w := newMoxyReadWorld(t)
	t.Setenv("CLOWN_SESSION_ID", "")
	if got, reason := moxyReadDecision(t, w, folioRead, map[string]any{"file_path": filepath.Join(w.child, "f.txt")}); got != "" {
		t.Fatalf("decision = %q (%q), want none", got, reason)
	}
}
