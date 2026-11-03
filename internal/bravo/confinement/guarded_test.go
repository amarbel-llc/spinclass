package confinement

import (
	"os"
	"path/filepath"
	"testing"
)

// A worktree is guarded (spinclass#449) exactly when the frontend recorded a
// unit for it, and when nobody can tell: an unresolvable state directory
// must not turn a confined session's merge into an ordinary one.
func TestGuardedWorktree(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	wt := t.TempDir()
	if GuardedWorktree(wt) {
		t.Fatal("a worktree with no unit record or spec is not guarded")
	}
	spec := UnitSpecPath(stateDir, wt)
	if err := os.MkdirAll(filepath.Dir(spec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !GuardedWorktree(wt) {
		t.Fatal("a worktree with a unit spec is guarded")
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if !GuardedWorktree(t.TempDir()) {
		t.Fatal("an unresolvable state directory must fail closed")
	}
}

// The record is keyed on the path the spawn used; a merge or check names the
// worktree by its own cwd, which may reach it through a symlinked parent.
// Either spelling must find the record, or the guard fails open.
func TestGuardedWorktreeThroughASymlinkedParent(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	base := t.TempDir()
	real := filepath.Join(base, "real", "wt")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "wt")
	// The key is canonical, so whichever spelling wrote the record, either
	// spelling finds it.
	for _, tc := range []struct{ recorded, queried string }{{real, viaLink}, {viaLink, real}} {
		dir := t.TempDir()
		t.Setenv("XDG_STATE_HOME", dir)
		spec := UnitSpecPath(dir, tc.recorded)
		if err := os.MkdirAll(filepath.Dir(spec), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(spec, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !GuardedWorktree(tc.queried) {
			t.Errorf("recorded as %s, queried as %s: not guarded", tc.recorded, tc.queried)
		}
	}
	// A record a pre-canonical spinclass keyed by the link spelling as given
	// is still found.
	legacyDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", legacyDir)
	legacy := specPathFor(unitRecordPathKeyed(legacyDir, filepath.Clean(viaLink)))
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !GuardedWorktree(viaLink) {
		t.Error("a legacy record keyed by the given spelling is not found")
	}
}

// A stat that fails for a reason other than absence is not "no record": a
// state directory that is a file (ENOTDIR) reads as confined.
func TestGuardedWorktreeFailsClosedOnAStatErrorOtherThanAbsence(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "state-is-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", notADir)
	if !GuardedWorktree(t.TempDir()) {
		t.Fatal("an unreadable record location must fail closed")
	}
}
