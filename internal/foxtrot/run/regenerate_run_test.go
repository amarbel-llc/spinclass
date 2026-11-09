package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// A branch cut from main (flake.lock L1, gen.txt "old"), whose step committed a
// lock bump (L2) and left gen.txt stale: the situation the phase exists for.
type regenFixture struct {
	dir  string
	sha0 string // the step's HEAD
	base string
}

func newRegenFixture(t *testing.T) regenFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	commitFile(t, dir, "flake.lock", "L1")
	commitFile(t, dir, "gen.txt", "old")
	gitIn(t, dir, "checkout", "-q", "-b", "work")
	commitFile(t, dir, "flake.lock", "L2")
	return regenFixture{dir: dir, sha0: gitOut(t, dir, "rev-parse", "HEAD"), base: "main"}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGitOut(t, dir, args...))
}

func repairSweatfile(cmd string) sweatfile.Sweatfile {
	return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: &cmd}}
}

func preCommitSweatfile(cmd string) sweatfile.Sweatfile {
	return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreCommit: &cmd}}
}

func stubHook(t *testing.T, fn func(dir string) error) {
	t.Helper()
	prev := runRepair
	runRepair = func(_ context.Context, _ sweatfile.Sweatfile, wt string, _ io.Writer) error { return fn(wt) }
	t.Cleanup(func() { runRepair = prev })
}

func runRegen(t *testing.T, f regenFixture, sf sweatfile.Sweatfile) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	err := regenerateRun(context.Background(), ts, sf, f.dir, f.base)
	ts.Finish()
	return buf.String(), err
}

func writeFileIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRegenerateRunCommitsARepairsEditsAsOneSeparateCommit(t *testing.T) {
	f := newRegenFixture(t)
	stubHook(t, func(wt string) error { writeFileIn(t, wt, "gen.txt", "new"); return nil })

	stream, err := runRegen(t, f, repairSweatfile("repair"))
	if err != nil {
		t.Fatalf("regenerateRun: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("the step's HEAD %s is not the parent of the regenerate commit (%s)", f.sha0, got)
	}
	if got := gitOut(t, f.dir, "log", "-1", "--format=%s"); got != regenerateCommitSubject {
		t.Errorf("subject = %q", got)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want only gen.txt", got)
	}
	if st := gitOut(t, f.dir, "status", "--porcelain"); st != "" {
		t.Errorf("the tree is not clean afterwards: %s", st)
	}
}

// conformist-repair amends HEAD; the phase splits the amend off so the step's
// commit stays as the script wrote it.
func TestRegenerateRunSplitsARepairAmendFromTheStepsCommit(t *testing.T) {
	f := newRegenFixture(t)
	stubHook(t, func(wt string) error {
		writeFileIn(t, wt, "gen.txt", "new")
		gitIn(t, wt, "commit", "-q", "-a", "--amend", "--no-edit")
		return nil
	})

	stream, err := runRegen(t, f, repairSweatfile("repair"))
	if err != nil {
		t.Fatalf("regenerateRun: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("the step's commit was rewritten: HEAD~1 = %s, want %s", got, f.sha0)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want only gen.txt", got)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", f.sha0); got != "flake.lock" {
		t.Errorf("the step's commit now holds %q, want only flake.lock", got)
	}
}

// A repo with only [hooks].pre-commit: the hook sees staged files only, so the
// phase stages the branch's whole diff (flake.lock included, which is what
// fires a lock-triggered lane) and the hook's restaging becomes the commit.
func TestRegenerateRunStagesTheBranchDiffForAPreCommitOnlyRepo(t *testing.T) {
	f := newRegenFixture(t)
	var stagedAtRun string
	stubHook(t, func(wt string) error {
		stagedAtRun = gitOut(t, wt, "diff", "--cached", "--name-only")
		writeFileIn(t, wt, "gen.txt", "new")
		gitIn(t, wt, "add", "gen.txt")
		return nil
	})

	stream, err := runRegen(t, f, preCommitSweatfile("conformist-pre-commit"))
	if err != nil {
		t.Fatalf("regenerateRun: %v\n%s", err, stream)
	}
	if !strings.Contains(stagedAtRun, "flake.lock") {
		t.Errorf("the hook did not see the lock bump staged: %q", stagedAtRun)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD~1"); got != f.sha0 {
		t.Errorf("the step's HEAD %s is not the parent of the regenerate commit (%s)", f.sha0, got)
	}
	if got := gitOut(t, f.dir, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want only the regenerated file, not the lock", got)
	}
}

func TestRegenerateRunMakesNoCommitWhenNothingChanges(t *testing.T) {
	f := newRegenFixture(t)
	stubHook(t, func(string) error { return nil })

	stream, err := runRegen(t, f, repairSweatfile("repair"))
	if err != nil {
		t.Fatalf("regenerateRun: %v\n%s", err, stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD"); got != f.sha0 {
		t.Errorf("a clean regeneration moved HEAD from %s to %s", f.sha0, got)
	}
	if !strings.Contains(stream, "already conformant") {
		t.Errorf("a clean regeneration is not reported:\n%s", stream)
	}
}

func TestRegenerateRunFailsClosedAndRestoresTheTreeWhenTheHookFails(t *testing.T) {
	f := newRegenFixture(t)
	stubHook(t, func(wt string) error {
		writeFileIn(t, wt, "gen.txt", "half-written")
		gitIn(t, wt, "add", "gen.txt")
		return errors.New("boom")
	})

	stream, err := runRegen(t, f, preCommitSweatfile("conformist-pre-commit"))
	if err == nil {
		t.Fatalf("a failing hook must fail the phase:\n%s", stream)
	}
	if got := gitOut(t, f.dir, "rev-parse", "HEAD"); got != f.sha0 {
		t.Errorf("HEAD = %s, want the step's %s", got, f.sha0)
	}
	if st := gitOut(t, f.dir, "status", "--porcelain"); st != "" {
		t.Errorf("the tree was not restored: %s", st)
	}
}

func TestRegenerateRunRefusesATreeWithUncommittedTrackedChanges(t *testing.T) {
	f := newRegenFixture(t)
	called := false
	stubHook(t, func(string) error { called = true; return nil })
	writeFileIn(t, f.dir, "gen.txt", "edited after the step")

	if stream, err := runRegen(t, f, repairSweatfile("repair")); err == nil || !strings.Contains(err.Error(), "uncommitted tracked changes") {
		t.Fatalf("err = %v, want a dirty-tree refusal\n%s", err, stream)
	}
	if called {
		t.Error("the hook ran on a tree the lock policy did not admit")
	}
}

func TestRegenerateRunSkipsWhenNoHookIsConfigured(t *testing.T) {
	f := newRegenFixture(t)
	called := false
	stubHook(t, func(string) error { called = true; return nil })

	stream, err := runRegen(t, f, sweatfile.Sweatfile{})
	if err != nil || called {
		t.Errorf("err=%v called=%v, want a clean skip", err, called)
	}
	if !strings.Contains(stream, "nothing to run") {
		t.Errorf("the skip is not explained:\n%s", stream)
	}
}
