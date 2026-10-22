package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/confinement"
)

const bindTestPrincipal = "5e5e5e5e-0000-4000-8000-000000000000"

// The SessionStart hook of an unconfined worktree session runs in the frontend
// and writes the principal mapping into frontend state, so the principal resolves
// to its session through confinement.SessionKeyOfPrincipal.
func TestSessionStartBindsAnUnconfinedPrincipalMapping(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	setEnvOrUnset(t, "CLOWN_UNIT", "")
	t.Setenv("CLOWN_SESSION_ID", bindTestPrincipal)
	_, wt := initSpawnedWorktree(t, "driver/key")

	fireSessionStart(t, wt)

	if _, err := os.Stat(confinement.PrincipalMappingPath(state, bindTestPrincipal)); err != nil {
		t.Fatalf("the hook did not write the frontend principal mapping: %v", err)
	}
	if key, err := confinement.SessionKeyOfPrincipal(bindTestPrincipal); err != nil || key != "myrepo/feature" {
		t.Errorf("SessionKeyOfPrincipal = %q, %v; want myrepo/feature", key, err)
	}
}

// Inside a unit (CLOWN_UNIT set) the hook is the worker's own and must write no
// mapping: a confined worker's mapping comes from the unit record, never from
// anything the worker's hook says.
func TestSessionStartInsideAUnitWritesNoPrincipalMapping(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLOWN_SESSION_ID", bindTestPrincipal)
	_, wt := initSpawnedWorktree(t, "driver/key")
	t.Setenv("CLOWN_UNIT", testUnit)

	fireSessionStart(t, wt)

	if _, err := os.Stat(confinement.PrincipalMappingPath(state, bindTestPrincipal)); err == nil {
		t.Error("a hook running inside a unit wrote a frontend principal mapping")
	}
	if key, err := confinement.SessionKeyOfPrincipal(bindTestPrincipal); err == nil {
		t.Errorf("the principal resolved to %q although no mapping should exist", key)
	}
	if entries, _ := os.ReadDir(filepath.Join(state, "spinclass", "principals")); len(entries) != 0 {
		t.Errorf("principals dir is not empty: %v", entries)
	}
}

// A worktree that was launched confined (it has a frontend unit record) never
// gets the unconfined mapping from a hook, even one that runs outside a unit.
func TestSessionStartForAConfinedLaunchWritesNoUnconfinedMapping(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	setEnvOrUnset(t, "CLOWN_UNIT", "")
	t.Setenv("CLOWN_SESSION_ID", bindTestPrincipal)
	_, wt := initSpawnedWorktree(t, "driver/key")
	rec := confinement.UnitRecordPath(state, wt)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	fireSessionStart(t, wt)

	if _, err := os.Stat(confinement.PrincipalMappingPath(state, bindTestPrincipal)); err == nil {
		t.Error("the hook wrote an unconfined mapping for a worktree launched confined")
	}
}
