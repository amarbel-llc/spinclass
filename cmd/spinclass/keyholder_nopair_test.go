package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

const khModeOn = "[keyholder]\nmode = \"on\"\n"

func noPairState(t *testing.T, repo, key, spawnedBy string) *session.State {
	t.Helper()
	return &session.State{SessionKey: key, RepoPath: repo, WorktreePath: t.TempDir(), SessionState: session.StateInactive, SpawnedBy: spawnedBy}
}

// A-M2: turning the switch on starts nothing for a session that already runs, so
// a spawned worker with no pair is on the operator's agent and nothing said so.
// `sc list`, session-health and `sc validate` now say "holder: none, operator
// agent" for it, only while the switch is on and only for a worker.
func TestAWorkerWithNoPairIsSaidToRunOnTheOperatorsAgentWhileTheSwitchIsOn(t *testing.T) {
	home, repo := khHome(t, khModeOn)
	t.Setenv("HOME", home)
	st := noPairState(t, repo, "repo/old", "repo/driver")
	if got := keyholderNoPairNote(st); got != keyholderNoPairText {
		t.Errorf("a spawned worker with no pair, switch on: note %q, want %q", got, keyholderNoPairText)
	}
	for _, want := range []string{"holder: none, operator agent", "close and respawn"} {
		if !strings.Contains(keyholderNoPairText, want) {
			t.Errorf("the note does not say %q: %q", want, keyholderNoPairText)
		}
	}
}

func TestTheNoPairNoteIsQuietWhenTheSwitchIsOff(t *testing.T) {
	home, repo := khHome(t, "")
	t.Setenv("HOME", home)
	if got := keyholderNoPairNote(noPairState(t, repo, "repo/old", "repo/driver")); got != "" {
		t.Errorf("switch off: note %q, want none", got)
	}
}

// The operator's own session is a driver (rule A): it keeps his agent by rule, so
// it is not a finding.
func TestTheNoPairNoteIsQuietForADriverAndForASessionWithAPair(t *testing.T) {
	home, repo := khHome(t, khModeOn)
	t.Setenv("HOME", home)
	base := keyholderStateBaseFn()
	if got := keyholderNoPairNote(noPairState(t, repo, "repo/mine", "")); got != "" {
		t.Errorf("a session no spawner started: note %q, want none (a driver)", got)
	}
	if _, err := keyholder.RecordKind(base, "repo/rec-driver", keyholder.KindDriver); err != nil {
		t.Fatal(err)
	}
	if got := keyholderNoPairNote(noPairState(t, repo, "repo/rec-driver", "")); got != "" {
		t.Errorf("a recorded driver: note %q, want none", got)
	}
	// A worker that has a pair is not "none": its own note speaks.
	dir := keyholder.SessionDir(base, "repo/has-pair")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "record.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := keyholderNoPairNote(noPairState(t, repo, "repo/has-pair", "repo/driver")); got != "" {
		t.Errorf("a worker with a pair: note %q, want none", got)
	}
}

// The recorded kind decides for a session no spawner started; a spawner wins over
// a recorded driver (a session cannot write the spawner's record, but a record
// of kind is a file in the account's own state).
func TestTheNoPairNoteFollowsTheRecordedKindAndTheSpawner(t *testing.T) {
	home, repo := khHome(t, khModeOn)
	t.Setenv("HOME", home)
	base := keyholderStateBaseFn()
	if _, err := keyholder.RecordKind(base, "repo/rec-worker", keyholder.KindWorker); err != nil {
		t.Fatal(err)
	}
	if got := keyholderNoPairNote(noPairState(t, repo, "repo/rec-worker", "")); got != keyholderNoPairText {
		t.Errorf("a recorded worker: note %q, want %q", got, keyholderNoPairText)
	}
	if _, err := keyholder.RecordKind(base, "repo/spawned-rec-driver", keyholder.KindDriver); err != nil {
		t.Fatal(err)
	}
	if got := keyholderNoPairNote(noPairState(t, repo, "repo/spawned-rec-driver", "repo/driver")); got != keyholderNoPairText {
		t.Errorf("a spawned session whose kind record says driver: note %q, want %q (the spawner's record wins)", got, keyholderNoPairText)
	}
}

func TestTheNoPairNoteIsQuietForAClosedAndForAnImplicitSession(t *testing.T) {
	home, repo := khHome(t, khModeOn)
	t.Setenv("HOME", home)
	gone := noPairState(t, repo, "repo/gone", "repo/driver")
	gone.WorktreePath = filepath.Join(t.TempDir(), "no-such-worktree")
	if got := keyholderNoPairNote(gone); got != "" {
		t.Errorf("a session whose worktree is gone: note %q, want none", got)
	}
	implicit := noPairState(t, repo, "repo/(main)", "repo/driver")
	implicit.Kind = session.KindImplicit
	if got := keyholderNoPairNote(implicit); got != "" {
		t.Errorf("an implicit session: note %q, want none (not given a holder in this build)", got)
	}
}

// Each session is judged by ITS OWN repo, never by where the caller stands:
// run from a tree whose layer says on, a worker under a tree that says nothing
// is not listed, and one under the on tree still is.
func TestTheNoPairNoteFollowsTheSessionsOwnRepoNotTheCallersDirectory(t *testing.T) {
	home, offRepo := khHome(t, "")
	t.Setenv("HOME", home)
	onTree := filepath.Join(home, "on-tree")
	onRepo := filepath.Join(onTree, "repo")
	if err := os.MkdirAll(onRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onTree, "sweatfile"), []byte(khModeOn), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := keyholderDriverRepoFn
	keyholderDriverRepoFn = func(string) string { return onRepo }
	t.Cleanup(func() { keyholderDriverRepoFn = prev })

	if got := keyholderNoPairNote(noPairState(t, offRepo, "repo/elsewhere", "repo/driver")); got != "" {
		t.Errorf("a worker whose own repo has the switch off, seen from an on directory: note %q, want none", got)
	}
	if got := keyholderNoPairNote(noPairState(t, onRepo, "repo/inside", "repo/driver")); got != keyholderNoPairText {
		t.Errorf("a worker whose own repo has the switch on: note %q, want %q", got, keyholderNoPairText)
	}
	// A launch still takes the driver's context: only the listing changed.
	sw, err := resolveKeyholderSwitch(home, offRepo)
	if err != nil || !sw.On {
		t.Errorf("a launch from an on directory into an off repo: on=%v err=%v, want on", sw.On, err)
	}
}

// The list renderers use the pair's own note first, then this one.
func TestTheListNoteIsThePairsNoteElseTheNoPairNote(t *testing.T) {
	home, repo := khHome(t, khModeOn)
	t.Setenv("HOME", home)
	prev := keyholderNoteFn
	t.Cleanup(func() { keyholderNoteFn = prev })
	st := noPairState(t, repo, "repo/old", "repo/driver")
	keyholderNoteFn = func(string) string { return "keyholder: DEAD" }
	if got := keyholderListNote(st); got != "keyholder: DEAD" {
		t.Errorf("with a pair note: %q, want it to win", got)
	}
	keyholderNoteFn = func(string) string { return "" }
	if got := keyholderListNote(st); got != keyholderNoPairText {
		t.Errorf("with none: %q, want %q", got, keyholderNoPairText)
	}
}
