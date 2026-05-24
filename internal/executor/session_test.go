package executor

import (
	"os"
	"testing"

	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
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
	tp := tap.TestPoint{}
	if err := exec.Attach("/tmp/test", "myrepo/feat-x", nil, true, &tp); err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"SPINCLASS_SESSION_ID": "myrepo/feat-x",
		"SPINCLASS_REPO":       "myrepo",
		"SPINCLASS_BRANCH":     "feat-x",
		"SPINCLASS_WORKTREE":   "/tmp/test",
		"TMPDIR":               "/tmp/test/.tmp",
	}
	for k, want := range checks {
		if got := os.Getenv(k); got != want {
			t.Errorf("os.Getenv(%q) = %q, want %q", k, got, want)
		}
	}
}
