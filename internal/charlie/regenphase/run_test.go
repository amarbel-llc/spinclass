package regenphase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

func runRun(t *testing.T, f fixture, sf sweatfile.Sweatfile) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	err := run(context.Background(), ts, sf, f.dir, f.base)
	ts.Finish()
	return buf.String(), err
}

func TestRunCommitsARepairsEditsAsOneSeparateCommit(t *testing.T) {
	f := newFixture(t)
	stubHook(t, func(wt string) error { writeFileIn(t, wt, "gen.txt", "new"); return nil })

	stream, err := runRun(t, f, repairSweatfile("repair"))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("the branch's HEAD %s is not the parent of the regenerate commit (%s)", f.sha0, got)
	}
	if got := gitOut(t, f.dir, "log", "-1", "--format=%s"); got != CommitSubject {
		t.Errorf("subject = %q", got)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want only gen.txt", got)
	}
	if st := gitOut(t, f.dir, "status", "--porcelain"); st != "" {
		t.Errorf("the tree is not clean afterwards: %s", st)
	}
}

// conformist-repair amends HEAD; the phase splits the amend off so the
// branch's commit stays as written.
func TestRunSplitsARepairAmendFromTheBranchsCommit(t *testing.T) {
	f := newFixture(t)
	stubHook(t, func(wt string) error {
		writeFileIn(t, wt, "gen.txt", "new")
		gitIn(t, wt, "commit", "-q", "-a", "--amend", "--no-edit")
		return nil
	})

	stream, err := runRun(t, f, repairSweatfile("repair"))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("the branch's commit was rewritten: HEAD~1 = %s, want %s", got, f.sha0)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want only gen.txt", got)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", f.sha0); got != "flake.lock" {
		t.Errorf("the branch's commit now holds %q, want only flake.lock", got)
	}
}

// A repo with only [hooks].pre-commit: the hook sees staged files only, so the
// phase stages the branch's whole diff (flake.lock included, which is what
// fires a lock-triggered lane) and the hook's restaging becomes the commit.
func TestRunStagesTheBranchDiffForAPreCommitOnlyRepo(t *testing.T) {
	f := newFixture(t)
	var stagedAtRun string
	stubHook(t, func(wt string) error {
		stagedAtRun = gitOut(t, wt, "diff", "--cached", "--name-only")
		writeFileIn(t, wt, "gen.txt", "new")
		gitIn(t, wt, "add", "gen.txt")
		return nil
	})

	stream, err := runRun(t, f, preCommitSweatfile("conformist-pre-commit"))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if !strings.Contains(stagedAtRun, "flake.lock") {
		t.Errorf("the hook did not see the lock bump staged: %q", stagedAtRun)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("the branch's HEAD %s is not the parent of the regenerate commit (%s)", f.sha0, got)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want only the regenerated file, not the lock", got)
	}
}

func TestRunMakesNoCommitWhenNothingChanges(t *testing.T) {
	f := newFixture(t)
	stubHook(t, func(string) error { return nil })

	stream, err := runRun(t, f, repairSweatfile("repair"))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD"); got != f.sha0 {
		t.Errorf("a clean regeneration moved HEAD from %s to %s", f.sha0, got)
	}
	if !strings.Contains(stream, "already conformant") {
		t.Errorf("a clean regeneration is not reported:\n%s", stream)
	}
}

// conformist exits 1 when one lane errors (a cache-db timeout was seen live)
// after another lane restaged the regenerated file: the result is kept.
func TestRunKeepsWhatAHookStagedEvenWhenItExitedWithAnError(t *testing.T) {
	f := newFixture(t)
	stubHook(t, func(wt string) error {
		writeFileIn(t, wt, "gen.txt", "new")
		gitIn(t, wt, "add", "gen.txt")
		return errors.New("conformist: exit status 1")
	})

	stream, err := runRun(t, f, preCommitSweatfile("conformist-pre-commit"))
	if err != nil {
		t.Fatalf("staged changes must survive a nonzero exit: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want gen.txt", got)
	}
	if !strings.Contains(stream, "exited with an error") {
		t.Errorf("the hook's error is not reported:\n%s", stream)
	}
}

// With both hooks configured, the staged-file hook runs: ringmaster has a
// repair hook that reports "already conformant" on the same tree.
func TestRunPrefersThePreCommitHookWhenBothAreConfigured(t *testing.T) {
	f := newFixture(t)
	var stagedAtRun string
	stubHook(t, func(wt string) error {
		stagedAtRun = gitOut(t, wt, "diff", "--cached", "--name-only")
		return nil
	})
	both := repairSweatfile("repair")
	pc := "conformist-pre-commit"
	both.Hooks.PreCommit = &pc

	if stream, err := runRun(t, f, both); err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if !strings.Contains(stagedAtRun, "flake.lock") {
		t.Errorf("the staged mode did not run (the hook saw %q staged)", stagedAtRun)
	}
}

func TestRunFailsClosedAndRestoresTheTreeWhenTheHookFailsWithoutChangingAnything(t *testing.T) {
	f := newFixture(t)
	stubHook(t, func(string) error { return errors.New("boom") })

	stream, err := runRun(t, f, preCommitSweatfile("conformist-pre-commit"))
	if err == nil {
		t.Fatalf("a failing hook that changed nothing must fail the phase:\n%s", stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD"); got != f.sha0 {
		t.Errorf("HEAD = %s, want the branch's %s", got, f.sha0)
	}
	if st := gitOut(t, f.dir, "status", "--porcelain"); st != "" {
		t.Errorf("the tree was not restored: %s", st)
	}
}

func TestRunRefusesATreeWithUncommittedTrackedChanges(t *testing.T) {
	f := newFixture(t)
	calls := stubHook(t, func(string) error { return nil })
	writeFileIn(t, f.dir, "gen.txt", "edited after the step")

	if stream, err := runRun(t, f, repairSweatfile("repair")); err == nil || !strings.Contains(err.Error(), "uncommitted tracked changes") {
		t.Fatalf("err = %v, want a dirty-tree refusal\n%s", err, stream)
	}
	if *calls != 0 {
		t.Error("the hook ran on a tree the lock policy did not admit")
	}
}

func TestRunSkipsWhenNoHookIsConfigured(t *testing.T) {
	f := newFixture(t)
	calls := stubHook(t, func(string) error { return nil })

	stream, err := runRun(t, f, sweatfile.Sweatfile{})
	if err != nil || *calls != 0 {
		t.Errorf("err=%v calls=%d, want a clean skip", err, *calls)
	}
	if !strings.Contains(stream, "nothing to run") {
		t.Errorf("the skip is not explained:\n%s", stream)
	}
}
