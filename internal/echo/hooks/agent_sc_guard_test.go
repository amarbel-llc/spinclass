package hooks

import (
	"bytes"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testfs"
)

// runBashHook runs the PreToolUse hook for a Bash command and returns the
// permissionDecision ("" when the hook stayed silent) and its reason.
func runBashHook(t *testing.T, command string, disallowMainWorktree bool) (decision, reason string) {
	t.Helper()
	mainRepo := t.TempDir()
	worktree := t.TempDir()
	input := makeInput("Bash", map[string]any{"command": command}, worktree)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, mainRepo, worktree, disallowMainWorktree); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		return "", ""
	}
	var result map[string]any
	testfs.MustUnmarshal(t, stdout.Bytes(), &result)
	hso := result["hookSpecificOutput"].(map[string]any)
	decision, _ = hso["permissionDecision"].(string)
	reason, _ = hso["permissionDecisionReason"].(string)
	return decision, reason
}

func TestBashScVerbPolicy(t *testing.T) {
	const mcp = "mcp__plugin_spinclass_spinclass__"
	denied := []struct {
		name, command, wantInReason string
	}{
		{"merge", "sc merge", mcp + "merge-this-session"},
		{"restart child forced", "spinclass restart-child-session --child x --force", mcp + "restart-child-session"},
		{"close child", "sc close-child-session x", mcp + "close-child-session"},
		{"chained after git", "git status && sc spawn --brief hi", mcp + "spawn-session"},
		{"env wrapper", "env FOO=1 sc grant-session-handle --child x --to y", mcp + "grant-session-handle"},
		{"nix store path", "/nix/store/abc-spinclass-0.2.1/bin/spinclass merge", mcp + "merge-this-session"},
		{"relative result path", "./result/bin/sc merge", mcp + "merge-this-session"},
		{"after cd and semicolon", "cd /tmp; sc close", "operator"},
		{"piped", "echo hi | sc cancel-restart", mcp + "cancel-restart"},
		{"unknown verb", "sc frobnicate", "operator"},
		{"leading assignment", "FOO=1 sc merge", mcp + "merge-this-session"},
		{"global flag with value", "sc --format tap merge", mcp + "merge-this-session"},
		{"global flag with equals", "sc --format=tap merge", mcp + "merge-this-session"},
		{"short verbose flag", "sc -v merge", mcp + "merge-this-session"},
		{"nohup wrapper", "nohup sc merge", mcp + "merge-this-session"},
		{"sudo env wrapper", "sudo -E env A=b spinclass merge", mcp + "merge-this-session"},
		{"newline separated", "git log\nsc merge", mcp + "merge-this-session"},
		{"or chain", "false || sc restart-this-session", mcp + "restart-this-session"},
		{"perms edit", "sc perms edit", "operator"},
		{"list-handles accepting", "sc list-handles --accept", "operator"},
		{"unparseable but sc-looking", `sc merge "unterminated`, "operator"},
		{"help flag after other args", "sc close-child-session x --force -h", mcp + "close-child-session"},
		{"help flag after a positional", "sc merge foo --help", mcp + "merge-this-session"},
		{"command wrapper", "command sc merge", mcp + "merge-this-session"},
		{"exec wrapper", "exec spinclass merge", mcp + "merge-this-session"},
		{"time wrapper", "time sc merge", mcp + "merge-this-session"},
		{"nice with a value flag", "nice -n 10 sc merge", mcp + "merge-this-session"},
		{"sudo with a user", "sudo -u someone sc merge", mcp + "merge-this-session"},
		{"quoted binary", `"sc" merge`, mcp + "merge-this-session"},
		{"single-quoted verb", `sc 'merge'`, mcp + "merge-this-session"},
		{"quoted store path", `'/nix/store/abc-spinclass-0.2.1/bin/spinclass' restart-this-session`, mcp + "restart-this-session"},
		{"in a subshell", "(cd /tmp && sc merge)", mcp + "merge-this-session"},
		{"in backticks", "echo `sc merge`", mcp + "merge-this-session"},
		{"background job", "sc merge &", mcp + "merge-this-session"},
	}
	for _, tc := range denied {
		for _, disallow := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				decision, reason := runBashHook(t, tc.command, disallow)
				if decision != "deny" {
					t.Fatalf("%q (disallowMainWorktree=%t): decision = %q, want deny", tc.command, disallow, decision)
				}
				if !strings.Contains(reason, tc.wantInReason) {
					t.Errorf("reason %q should contain %q", reason, tc.wantInReason)
				}
				if !strings.Contains(reason, "#422") {
					t.Errorf("reason %q should cite #422", reason)
				}
			})
		}
	}

	allowed := []struct{ name, command string }{
		{"list", "sc list"},
		{"global flag then list", "sc --format tap list"},
		{"version", "spinclass version"},
		{"validate", "sc validate"},
		{"whoami", "sc whoami"},
		{"list-handles", "sc list-handles"},
		{"perms list spaced", "sc perms list"},
		{"perms list hyphenated", "sc perms-list"},
		{"implicit-session-key", "sc implicit-session-key --claude-session-id abc"},
		{"merge help", "sc merge --help"},
		{"bare sc", "sc"},
		{"top-level help flag", "sc --help"},
		{"top-level short help", "sc -h"},
		{"help verb", "sc help"},
		{"read-only nix store path", "/nix/store/abc-spinclass-0.2.1/bin/spinclass list"},
		{"list in chain", "git status && sc list"},
		{"git log", "git log"},
		{"sc as argument", "echo sc merge"},
		{"scp", "scp file host:"},
		{"file named sc.go", "cat sc.go"},
		{"sc in quoted string", `git commit -m "sc merge now"`},
		{"unparseable non-sc", `echo "unterminated`},
	}
	for _, tc := range allowed {
		for _, disallow := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				decision, reason := runBashHook(t, tc.command, disallow)
				if decision == "deny" {
					t.Fatalf("%q (disallowMainWorktree=%t) denied: %s", tc.command, disallow, reason)
				}
			})
		}
	}
}
