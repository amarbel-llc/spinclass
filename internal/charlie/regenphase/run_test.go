package regenphase

import (
	"bytes"
	"context"
	"errors"
	"io"
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
// after another lane restaged a file: the hook is run again, and the phase
// commits once a run exits 0.
func TestRunRunsAFailedHookAgainAndKeepsTheResultOnceItSettles(t *testing.T) {
	f := newFixture(t)
	calls := 0
	stubHook(t, func(wt string) error {
		calls++
		writeFileIn(t, wt, "gen.txt", "new")
		gitIn(t, wt, "add", "gen.txt")
		if calls == 1 {
			return errors.New("conformist: exit status 1")
		}
		return nil
	})

	stream, err := runRun(t, f, preCommitSweatfile("conformist-pre-commit"))
	if err != nil {
		t.Fatalf("a hook that settles on the second run must pass: %v\n%s", err, stream)
	}
	if calls != 3 {
		t.Errorf("hook runs = %d, want 3 (two in round 1 up to the first exit 0, one in round 2 that changes nothing)", calls)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want gen.txt", got)
	}
}

// A lane that keeps failing must not pass: the file another lane restaged is
// not committed as if the regeneration had worked, the branch is put back, and
// the hook's output is in the failure (live ringmaster cascade, 2026-11-09).
func TestRunFailsAndRestoresTheBranchWhenAHookKeepsFailingAfterStagingFiles(t *testing.T) {
	f := newFixture(t)
	calls := 0
	stubHook(t, func(wt string) error {
		calls++
		writeFileIn(t, wt, "gen.txt", "new")
		gitIn(t, wt, "add", "gen.txt")
		return errors.New("conformist: exit status 1")
	})

	stream, err := runRun(t, f, preCommitSweatfile("conformist-pre-commit"))
	if err == nil {
		t.Fatalf("a hook that never exits 0 must fail the phase:\n%s", stream)
	}
	if calls != hookAttempts {
		t.Errorf("hook runs = %d, want %d", calls, hookAttempts)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD"); got != f.sha0 {
		t.Errorf("HEAD = %s, want the branch's %s", got, f.sha0)
	}
	if st := gitOut(t, f.dir, "status", "--porcelain"); st != "" {
		t.Errorf("the tree was not restored: %s", st)
	}
}

// A generated file derived from a source the first round reformatted is stale
// after round 1 (live, ringmaster 2026-11-09): round 2 regenerates it, and the
// rounds still land as ONE commit on top of the branch's HEAD.
func TestRunRunsRoundsToAFixpointAndSquashesThemIntoOneCommit(t *testing.T) {
	f := newFixture(t)
	round := 0
	stubHook(t, func(wt string) error {
		round++
		switch round {
		case 1:
			writeFileIn(t, wt, "src.txt", "formatted")
			gitIn(t, wt, "add", "src.txt")
		case 2:
			writeFileIn(t, wt, "derived.txt", "from formatted")
			gitIn(t, wt, "add", "derived.txt")
		}
		return nil
	})

	stream, err := runRun(t, f, preCommitSweatfile("conformist-pre-commit"))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if round != 3 {
		t.Errorf("hook runs = %d, want 3 (two that changed something, one that found the fixpoint)", round)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("one commit on top of the branch's HEAD expected: HEAD~1 = %s, want %s", got, f.sha0)
	}
	files := strings.Fields(gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(files, ",") != "derived.txt,src.txt" {
		t.Errorf("the regenerate commit holds %v, want both rounds' files", files)
	}
}

// A staged hook that edits one file without restaging it must not leave the
// tree dirty for the next hook (live ringmaster, 2026-11-09: conformist's
// repair refused to run on it): the edit is taken into the commit.
func TestRunTakesAnUnstagedEditAStagedHookLeftBehind(t *testing.T) {
	f := newFixture(t)
	commitFile(t, f.dir, "gen2.txt", "old")
	f.sha0 = gitOut(t, f.dir, "rev-parse", "HEAD")
	var statusAtRepair string
	stubBoth(t,
		func(wt string) error {
			writeFileIn(t, wt, "gen.txt", "new")
			gitIn(t, wt, "add", "gen.txt")
			writeFileIn(t, wt, "gen2.txt", "edited, not restaged")
			return nil
		},
		func(wt string) error {
			statusAtRepair = gitOut(t, wt, "status", "--porcelain")
			return nil
		})

	stream, err := runRun(t, f, bothHooks())
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if statusAtRepair != "" {
		t.Errorf("the repair hook saw a dirty tree: %q", statusAtRepair)
	}
	files := strings.Fields(gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(files, ",") != "gen.txt,gen2.txt" {
		t.Errorf("the regenerate commit holds %v, want both files", files)
	}
}

func bothHooks() sweatfile.Sweatfile {
	repair, pre := "the-repair", "the-pre-commit"
	return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: &repair, PreCommit: &pre}}
}

// stubBoth runs fnPre for [hooks].pre-commit and fnRepair for [hooks].repair
// (the phase hands each to the runner as the sweatfile's repair command) and
// records the order.
func stubBoth(t *testing.T, fnPre, fnRepair func(wt string) error) *[]string {
	t.Helper()
	var order []string
	prev := runRepair
	runRepair = func(_ context.Context, sf sweatfile.Sweatfile, wt string, _ io.Writer) error {
		switch *sf.Hooks.Repair {
		case "the-pre-commit":
			order = append(order, "pre-commit")
			return fnPre(wt)
		case "the-repair":
			order = append(order, "repair")
			return fnRepair(wt)
		}
		t.Errorf("unexpected hook command %q", *sf.Hooks.Repair)
		return nil
	}
	t.Cleanup(func() { runRepair = prev })
	return &order
}

// Operator direction, 2026-11-09: with both hooks configured, run both. The
// staged hook goes first over the branch diff; the repair hook then runs on the
// clean tree, and its plain edits are folded into the same commit.
func TestRunRunsBothHooksAndCommitsTheUnionAsOneCommit(t *testing.T) {
	f := newFixture(t)
	commitFile(t, f.dir, "gen2.txt", "old")
	f.sha0 = gitOut(t, f.dir, "rev-parse", "HEAD")
	var stagedAtPre, statusAtRepair string
	order := stubBoth(t,
		func(wt string) error {
			stagedAtPre = gitOut(t, wt, "diff", "--cached", "--name-only")
			writeFileIn(t, wt, "gen.txt", "new")
			gitIn(t, wt, "add", "gen.txt")
			return nil
		},
		func(wt string) error {
			statusAtRepair = gitOut(t, wt, "status", "--porcelain")
			writeFileIn(t, wt, "gen2.txt", "new")
			return nil
		})

	stream, err := runRun(t, f, bothHooks())
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if strings.Join(*order, ",") != "pre-commit,repair,pre-commit,repair" {
		t.Errorf("order = %v, want pre-commit then repair, and the same again in the round that finds nothing changed", *order)
	}
	if !strings.Contains(stagedAtPre, "flake.lock") {
		t.Errorf("the staged hook did not see the lock bump staged: %q", stagedAtPre)
	}
	if statusAtRepair != "" {
		t.Errorf("the repair hook did not run on a clean tree: %q", statusAtRepair)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("one commit on top of the branch's HEAD expected: HEAD~1 = %s, want %s", got, f.sha0)
	}
	files := strings.Fields(gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(files, ",") != "gen.txt,gen2.txt" {
		t.Errorf("the regenerate commit holds %v, want the union gen.txt and gen2.txt", files)
	}
}

// A repair tool that amends HEAD (conformist --commit --amend) amends the
// regenerate commit, which is the fold.
func TestRunFoldsARepairToolsAmendIntoTheRegenerateCommit(t *testing.T) {
	f := newFixture(t)
	commitFile(t, f.dir, "gen2.txt", "old")
	f.sha0 = gitOut(t, f.dir, "rev-parse", "HEAD")
	stubBoth(t,
		func(wt string) error {
			writeFileIn(t, wt, "gen.txt", "new")
			gitIn(t, wt, "add", "gen.txt")
			return nil
		},
		func(wt string) error {
			writeFileIn(t, wt, "gen2.txt", "new")
			gitIn(t, wt, "commit", "-q", "-a", "--amend", "--no-edit")
			return nil
		})

	stream, err := runRun(t, f, bothHooks())
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("the branch's HEAD was rewritten: HEAD~1 = %s, want %s", got, f.sha0)
	}
	files := strings.Fields(gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(files, ",") != "gen.txt,gen2.txt" {
		t.Errorf("the regenerate commit holds %v, want the union", files)
	}
}

// The staged hook finds nothing to change but the repair hook does (the repair
// amends the branch's own commit): the repair's result is still split off.
func TestRunStillCommitsARepairsResultWhenTheStagedHookChangesNothing(t *testing.T) {
	f := newFixture(t)
	stubBoth(t,
		func(string) error { return nil },
		func(wt string) error {
			writeFileIn(t, wt, "gen.txt", "new")
			gitIn(t, wt, "commit", "-q", "-a", "--amend", "--no-edit")
			return nil
		})

	stream, err := runRun(t, f, bothHooks())
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("the branch's commit was rewritten: HEAD~1 = %s, want %s", got, f.sha0)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want gen.txt", got)
	}
}

// With both hooks configured, either one still failing after its retries fails
// the phase, even when the other changed something.
func TestRunFailsWhenEitherHookKeepsFailing(t *testing.T) {
	f := newFixture(t)
	stubBoth(t,
		func(string) error { return errors.New("pre boom") },
		func(wt string) error { writeFileIn(t, wt, "gen.txt", "new"); return nil })
	if stream, err := runRun(t, f, bothHooks()); err == nil {
		t.Fatalf("a staged hook that keeps failing must fail the phase:\n%s", stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD"); got != f.sha0 {
		t.Errorf("HEAD = %s, want the branch's %s", got, f.sha0)
	}

	g := newFixture(t)
	stubBoth(t,
		func(string) error { return errors.New("pre boom") },
		func(string) error { return errors.New("repair boom") })
	if stream, err := runRun(t, g, bothHooks()); err == nil {
		t.Fatalf("both hooks failing with nothing changed must fail:\n%s", stream)
	}
	if got := gitOut(t, g.dir, "rev-parse", "HEAD"); got != g.sha0 {
		t.Errorf("HEAD = %s, want the branch's %s", got, g.sha0)
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
