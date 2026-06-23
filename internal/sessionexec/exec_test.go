package sessionexec

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/session"
)

// TestIdentityEnvVars asserts IdentityEnv layers the SPINCLASS_* identity
// (plus TMPDIR/CLAUDE_CODE_TMPDIR) over the process env. The exact var list
// is load-bearing — it mirrors executor.SessionExecutor.Attach.
func TestIdentityEnvVars(t *testing.T) {
	st := &session.State{
		SessionKey:   "myrepo/quiet-oak",
		RepoPath:     "/repos/myrepo",
		Branch:       "quiet-oak",
		WorktreePath: "/repos/myrepo/.worktrees/quiet-oak",
		Description:  "a session",
	}
	env := IdentityEnv(st)

	want := map[string]string{
		"SPINCLASS_SESSION_ID":  "myrepo/quiet-oak",
		"SPINCLASS_REPO":        "myrepo",
		"SPINCLASS_BRANCH":      "quiet-oak",
		"SPINCLASS_WORKTREE":    "/repos/myrepo/.worktrees/quiet-oak",
		"SPINCLASS_DESCRIPTION": "a session",
		"TMPDIR":                "/repos/myrepo/.worktrees/quiet-oak/.tmp",
		"CLAUDE_CODE_TMPDIR":    "/repos/myrepo/.worktrees/quiet-oak/.tmp",
	}
	got := lastValues(env, want)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// TestCommandInDir asserts CommandIn sets the working directory and env on
// the returned command and does not touch stdio (the caller owns it).
func TestCommandInDir(t *testing.T) {
	dir := t.TempDir()
	cmd := CommandIn(dir, []string{"echo", "hi"}, []string{"FOO=bar"})

	if cmd.Dir != dir {
		t.Errorf("Dir = %q, want %q", cmd.Dir, dir)
	}
	if !slices.Contains(cmd.Env, "FOO=bar") {
		t.Errorf("Env missing FOO=bar: %v", cmd.Env)
	}
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Error("CommandIn must leave stdio nil for the caller to set")
	}
}

// TestCommandInDefaultsToShell asserts an empty util resolves to a shell so a
// bare invocation has something to exec.
func TestCommandInDefaultsToShell(t *testing.T) {
	cmd := CommandIn(t.TempDir(), nil, nil)
	if cmd.Path == "" || filepath.Base(cmd.Args[len(cmd.Args)-1]) == "" {
		t.Fatalf("empty util produced no command: %v", cmd.Args)
	}
	// The final argv element is the shell (possibly direnv-wrapped before it).
	last := cmd.Args[len(cmd.Args)-1]
	if !strings.Contains(last, "sh") {
		t.Errorf("default command should be a shell, got %q (argv %v)", last, cmd.Args)
	}
}

// lastValues extracts, for each wanted key, the value of its last occurrence
// in env (os/exec semantics: the last duplicate wins).
func lastValues(env []string, keys map[string]string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, e := range env {
		i := strings.IndexByte(e, '=')
		if i < 0 {
			continue
		}
		k := e[:i]
		if _, ok := keys[k]; ok {
			out[k] = e[i+1:]
		}
	}
	return out
}
