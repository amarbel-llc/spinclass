package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// memRebases is an in-memory store.RebaseRecords.
type memRebases map[string]string

func (m memRebases) Read(w string) (string, bool) { v, ok := m[w]; return v, ok }
func (m memRebases) Write(w, head string) error   { m[w] = head; return nil }
func (m memRebases) Clear(w string)               { delete(m, w) }

// conflictFixture commits a.txt differently on the session branch, a foreign
// branch's worktree and main, so that rebasing either onto main stops on a
// conflict.
func conflictFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	for dir, content := range map[string]string{f.worktree: "session", f.foreignWorktree: "foreign", f.repo: "main"} {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", "a.txt")
		runGit(t, dir, "commit", "-m", "a.txt "+content)
	}
	return f
}

func startPlainRebase(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "rebase", "main")
	if err := cmd.Run(); err == nil {
		t.Fatal("the rebase was expected to stop on a conflict")
	}
}

// spinclass#456: a rebase grit started is on record, and the record lets ANY
// caller, confined or not, continue, skip or abort it and stage the
// resolution, provided head-name still agrees with the record. Without a
// record a confined caller stays refused (#455), and a head-name that
// disagrees with the record asks.
func TestClassifyMidRebaseOnRecordIsJudgedOnTheRecordedBranch(t *testing.T) {
	f := conflictFixture(t)
	startPlainRebase(t, f.worktree)
	startPlainRebase(t, f.foreignWorktree)

	own := []classifyCase{
		{name: "continue", tool: "branch", sub: "rebase", args: map[string]any{"continue": true}},
		{name: "skip", tool: "branch", sub: "rebase", args: map[string]any{"skip": true}},
		{name: "abort", tool: "branch", sub: "rebase", args: map[string]any{"abort": true}},
		{name: "stage the resolution", tool: "stage", sub: "add", args: map[string]any{"paths": []any{"a.txt"}}},
	}
	for _, conf := range []store.Confinement{store.ConfinementConfined, store.ConfinementUnresolved, store.ConfinementUnconfined} {
		t.Run(confinementNames[conf]+" with a matching record", func(t *testing.T) {
			for _, c := range own {
				s := f.own()
				s.Confinement = conf
				s.Rebases = memRebases{f.worktree: "refs/heads/" + sessionBranch}
				if got := New().Classify(s, op(t, c.tool, c.sub, c.args)); got != store.Allow {
					t.Errorf("%s = %v, want allow", c.name, got)
				}
			}
		})
	}

	t.Run("confined, no record: still ask (#455)", func(t *testing.T) {
		s := f.own()
		s.Confinement = store.ConfinementConfined
		s.Rebases = memRebases{}
		if got := New().Classify(s, op(t, "branch", "rebase", map[string]any{"continue": true})); got != store.Ask {
			t.Errorf("continue with no record = %v, want ask", got)
		}
	})

	t.Run("confined, record names another branch than head-name: ask", func(t *testing.T) {
		s := f.own()
		s.Confinement = store.ConfinementConfined
		s.Rebases = memRebases{f.worktree: "refs/heads/B2"}
		if got := New().Classify(s, op(t, "branch", "rebase", map[string]any{"continue": true})); got != store.Ask {
			t.Errorf("continue with a mismatching record = %v, want ask", got)
		}
	})

	t.Run("confined, a record for a foreign branch is not ownership", func(t *testing.T) {
		s := f.own()
		s.Dir, s.Confinement = f.foreignWorktree, store.ConfinementConfined
		// head-name in the foreign worktree is B2; the record agrees with it.
		s.Rebases = memRebases{f.foreignWorktree: "refs/heads/B2"}
		if got := New().Classify(s, op(t, "branch", "rebase", map[string]any{"continue": true})); got != store.Ask {
			t.Errorf("continue of a foreign rebase = %v, want ask", got)
		}
	})

	t.Run("confined, a record that names our branch cannot launder a foreign head-name", func(t *testing.T) {
		s := f.own()
		s.Dir, s.Confinement = f.foreignWorktree, store.ConfinementConfined
		s.Rebases = memRebases{f.foreignWorktree: "refs/heads/" + sessionBranch}
		if got := New().Classify(s, op(t, "branch", "rebase", map[string]any{"continue": true})); got != store.Ask {
			t.Errorf("a record disagreeing with head-name = %v, want ask", got)
		}
	})
}

// A confined caller's rebase that stops on a conflict is recorded; abort ends
// it and clears the record; a rebase that finishes leaves none.
func TestDoRecordsAConflictedRebaseAndClearsItWhenItEnds(t *testing.T) {
	f := conflictFixture(t)
	recs := memRebases{}
	s := f.own()
	s.Confinement = store.ConfinementConfined
	s.Rebases = recs

	_, err := New().Do(context.Background(), s, op(t, "branch", "rebase", map[string]any{"upstream": "main"}))
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("rebase err = %v, want a conflict", err)
	}
	if got := recs[f.worktree]; got != "refs/heads/"+sessionBranch {
		t.Fatalf("record after the conflicted start = %q, want the session branch", got)
	}

	// The worker may now continue its own rebase through the classifier.
	if got := New().Classify(s, op(t, "branch", "rebase", map[string]any{"continue": true})); got != store.Allow {
		t.Errorf("continue after a recorded start = %v, want allow", got)
	}

	if _, err := New().Do(context.Background(), s, op(t, "branch", "rebase", map[string]any{"abort": true})); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if _, ok := recs[f.worktree]; ok {
		t.Errorf("the record survived the abort: %v", recs)
	}
}

// A rebase that completes cleanly writes no record.
func TestDoLeavesNoRecordForARebaseThatFinishes(t *testing.T) {
	f := newFixture(t)
	recs := memRebases{}
	s := f.own()
	s.Confinement = store.ConfinementConfined
	s.Rebases = recs
	if _, err := New().Do(context.Background(), s, op(t, "branch", "rebase", map[string]any{"upstream": "main"})); err != nil {
		t.Fatalf("rebase: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("records after a clean rebase = %v, want none", recs)
	}
}
