package executor

import (
	"testing"

	tap "github.com/amarbel-llc/bob/packages/tap-dancer/go"
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
