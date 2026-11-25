package main

import (
	"bytes"
	"errors"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

func khLauncherFor(t *testing.T) (*keyholder.Launcher, error) {
	t.Helper()
	return keyholderLauncherFn()
}

// A launch that fails after this start created the pair stops the pair; a pair
// the start only found already running is left alone.
func TestKeyholderUndoStopsAPairOnlyWhenThisCallCreatedIt(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	khUseFake(t, f)

	_, undo, err := keyholderSessionEnvUndo(home, repo, "repo/a", &bytes.Buffer{})
	if err != nil || undo == nil {
		t.Fatalf("a first start returned (undo=%v, %v), want an undo", undo != nil, err)
	}
	l, _ := khLauncherFor(t)
	if _, ok, _ := l.Running(t.Context(), "repo/a"); !ok {
		t.Fatal("the pair is not running after the first start")
	}
	undo()
	if _, ok, _ := l.Running(t.Context(), "repo/a"); ok {
		t.Error("undo left the pair this call created running")
	}

	// A second session start finds a live pair: no undo.
	if _, _, err := keyholderSessionEnvUndo(home, repo, "repo/b", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	_, undo2, err := keyholderSessionEnvUndo(home, repo, "repo/b", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if undo2 != nil {
		t.Error("a start that found a live pair returned an undo, which would stop a session's pair")
	}
}

func TestKeyholderOffReturnsNoUndo(t *testing.T) {
	home, repo := khHome(t, "")
	khUseFake(t, newKhFake(t))
	env, undo, err := keyholderSessionEnvUndo(home, repo, "repo/a", &bytes.Buffer{})
	if env != nil || undo != nil || err != nil {
		t.Errorf("off: (%v, undo=%v, %v)", env, undo != nil, err)
	}
}

// A failed `sc start` that left no session behind stops the pair it launched; one
// that left a session (it ran, or failed after creating it) keeps it for close.
func TestAFailedStartWithNoSessionStopsThePairAndASessionKeepsIt(t *testing.T) {
	rp := worktree.ResolvedPath{RepoPath: t.TempDir(), Branch: "b", SessionKey: "r/b"}
	boom := errors.New("worktree creation failed")
	prev := sessionExistsFn
	t.Cleanup(func() { sessionExistsFn = prev })

	exists := false
	sessionExistsFn = func(worktree.ResolvedPath) bool { return exists }
	stopped := 0
	stopKeyholderAfterFailedStart(boom, func() { stopped++ }, rp)
	if stopped != 1 {
		t.Errorf("a failed start with no session stopped the pair %d times, want 1", stopped)
	}
	stopKeyholderAfterFailedStart(nil, func() { stopped++ }, rp)
	if stopped != 1 {
		t.Error("a successful start stopped the pair")
	}
	stopKeyholderAfterFailedStart(boom, nil, rp)
	if stopped != 1 {
		t.Error("nil undo was called")
	}
	exists = true // the session was created before the failure (or ran and exited with an error)
	stopKeyholderAfterFailedStart(boom, func() { stopped++ }, rp)
	if stopped != 1 {
		t.Error("a failed start that left a session stopped its pair; `sc close` owns it")
	}
}
