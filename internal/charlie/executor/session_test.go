package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tap "code.linenisgreat.com/tap/go/pkgs/writer"
)

func TestSessionExecutorDryRunExpandsEnvVars(t *testing.T) {
	exec := SessionExecutor{
		Entrypoint: []string{"zmx", "-g", "sc", "attach", "$SPINCLASS_SESSION_ID"},
	}
	tp := tap.TestPoint{}
	err := exec.Attach("/tmp/test", "myrepo/feat-x", nil, true, &tp)
	if err != nil {
		t.Fatal(err)
	}
	if tp.Skip != "dry run" {
		t.Errorf("Skip = %q, want 'dry run'", tp.Skip)
	}
	want := "zmx -g sc attach myrepo/feat-x"
	got := tp.Diagnostics.Extras["command"].(string)
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestSessionExecutorDryRunExpandsBranchVar(t *testing.T) {
	exec := SessionExecutor{
		Entrypoint: []string{"zellij", "-s", "$SPINCLASS_BRANCH"},
	}
	tp := tap.TestPoint{}
	err := exec.Attach("/tmp/test", "bob/eager-aspen", nil, true, &tp)
	if err != nil {
		t.Fatal(err)
	}
	want := "zellij -s eager-aspen"
	got := tp.Diagnostics.Extras["command"].(string)
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestSessionExecutorDryRunNoExpansionWithoutVars(t *testing.T) {
	exec := SessionExecutor{
		Entrypoint: []string{"fish"},
	}
	tp := tap.TestPoint{}
	err := exec.Attach("/tmp/test", "repo/branch", nil, true, &tp)
	if err != nil {
		t.Fatal(err)
	}
	want := "fish"
	got := tp.Diagnostics.Extras["command"].(string)
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestSessionExecutorAppliesUserEnv(t *testing.T) {
	// User-configured env should land in the session environment and be
	// available for argv expansion.
	exec := SessionExecutor{
		Entrypoint: []string{"zmx", "-g", "$SPINCLASS_GROUP", "attach", "$SPINCLASS_SESSION_ID"},
		Env: map[string]string{
			"SPINCLASS_GROUP": "spinclass",
		},
	}
	tp := tap.TestPoint{}
	if err := exec.Attach("/tmp/test", "repo/branch", nil, true, &tp); err != nil {
		t.Fatal(err)
	}
	want := "zmx -g spinclass attach repo/branch"
	got := tp.Diagnostics.Extras["command"].(string)
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestSessionExecutorSpinclassEnvOverridesUserEnv(t *testing.T) {
	// Spinclass-owned vars (SPINCLASS_SESSION_ID/REPO/BRANCH/WORKTREE/
	// DESCRIPTION, TMPDIR, CLAUDE_CODE_TMPDIR) must override anything the
	// user puts in [session-entry].env. The integration contract requires
	// them to be authoritative.
	exec := SessionExecutor{
		Entrypoint: []string{"echo"},
		Env: map[string]string{
			"SPINCLASS_SESSION_ID": "user-clobber",
			"SPINCLASS_REPO":       "user-clobber",
			"SPINCLASS_BRANCH":     "user-clobber",
			"SPINCLASS_WORKTREE":   "user-clobber",
			"TMPDIR":               "/tmp/user-clobber",
		},
	}
	env := exec.sessionEnv("/tmp/test", "myrepo/feat-x")
	checks := map[string]string{
		"SPINCLASS_SESSION_ID": "myrepo/feat-x",
		"SPINCLASS_REPO":       "myrepo",
		"SPINCLASS_BRANCH":     "feat-x",
		"SPINCLASS_WORKTREE":   "/tmp/test",
		"TMPDIR":               "/tmp/test/.tmp",
	}
	for k, want := range checks {
		if got := env[k]; got != want {
			t.Errorf("sessionEnv[%q] = %q, want %q", k, got, want)
		}
	}
}

// A [session-entry].env PATH still decides which entrypoint binary runs.
// It used to apply via os.Setenv, and now applies via an explicit lookup
// against the child's PATH.
func TestLookPathInResolvesAgainstSessionPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "only-here")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lookPathIn("only-here", "/nonexistent:"+dir); got != bin {
		t.Errorf("lookPathIn = %q, want %q", got, bin)
	}
	if got := lookPathIn("only-here", "/nonexistent"); got != "" {
		t.Errorf("lookPathIn on a PATH without it = %q, want \"\"", got)
	}
}

// The on-detach hook reads $SPINCLASS_SESSION_ID etc. SessionEnviron is how
// it receives them now that Attach no longer sets them process-wide.
func TestSessionEnvironCarriesIdentity(t *testing.T) {
	env := SessionExecutor{Env: map[string]string{"SPINCLASS_GROUP": "g"}}.SessionEnviron("/tmp/test", "myrepo/feat-x")
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"SPINCLASS_SESSION_ID=myrepo/feat-x", "SPINCLASS_GROUP=g", "TMPDIR=/tmp/test/.tmp"} {
		if !strings.Contains(joined, "\n"+want+"\n") {
			t.Errorf("SessionEnviron lacks %q: %v", want, env)
		}
	}
}

// spinclass#330: the session env belongs to the entrypoint child only. A
// no-attach (dry-run) Attach, which is what `sc run` and `sc start --no-attach`
// do, must not mutate the spinclass process's own env. Before the fix, the
// session's TMPDIR leaked into every later subprocess, including post-merge
// commands after the worktree was removed.
func TestSessionExecutorDoesNotMutateProcessEnv(t *testing.T) {
	t.Setenv("TMPDIR", "/sentinel/tmp")
	t.Setenv("SPINCLASS_SESSION_ID", "sentinel/session")
	exec := SessionExecutor{Entrypoint: []string{"echo"}, Env: map[string]string{"USER_ONLY": "x"}}
	tp := tap.TestPoint{}
	if err := exec.Attach("/tmp/test", "myrepo/feat-x", nil, true, &tp); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"TMPDIR":               "/sentinel/tmp",
		"SPINCLASS_SESSION_ID": "sentinel/session",
		"USER_ONLY":            "",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("os.Getenv(%q) = %q after Attach, want unchanged %q", k, got, want)
		}
	}
}
