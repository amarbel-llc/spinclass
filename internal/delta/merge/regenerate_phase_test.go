package merge

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/charlie/regenphase"
)

// The merge path runs the regenerate phase too (the plan of 2026-11-09, Q5): a
// branch that bumped flake.lock gets the repo's hook run under the new lock and
// its result committed as its own commit, before REPAIR and the pin.

const regenRepairSweatfile = "[hooks]\nrepair = \"echo new > gen.txt\"\n"

type admissionSpy struct {
	refuse error
	calls  int
}

func spyAdmission(t *testing.T, refuse error) *admissionSpy {
	t.Helper()
	spy := &admissionSpy{refuse: refuse}
	prev := admitLock
	admitLock = func(context.Context, string, []string) error { spy.calls++; return spy.refuse }
	t.Cleanup(func() { admitLock = prev })
	return spy
}

// regenRepo: main holds flake.lock L1 and gen.txt "old"; feat bumps the lock.
func regenRepo(t *testing.T) (repoDir, wtPath, global string) {
	t.Helper()
	repoDir, global = mergeFixture(t)
	writeFile(t, repoDir, "flake.lock", "L1")
	writeFile(t, repoDir, "gen.txt", "old")
	runGit(t, repoDir, "add", "flake.lock", "gen.txt")
	runGit(t, repoDir, "commit", "-m", "lock and generated file")
	wtPath = setupWorktree(t, repoDir, "feat")
	writeFile(t, wtPath, "flake.lock", "L2")
	runGit(t, wtPath, "add", "flake.lock")
	runGit(t, wtPath, "commit", "-m", "bump the lock")
	return repoDir, wtPath, global
}

func prepareWith(t *testing.T, repoDir, wtPath string, pm PostMergeOptions) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, err := PrepareMergeWith(context.Background(), rep, ts, repoDir, wtPath, "feat", "main", false, pm)
	ts.Finish()
	return buf.String(), err
}

func TestPrepareMergeRegeneratesUnderTheNewLockAsItsOwnCommit(t *testing.T) {
	repoDir, wtPath, global := regenRepo(t)
	writeSweatfile(t, global, regenRepairSweatfile)
	spy := spyAdmission(t, nil)
	lockBump := runGit(t, wtPath, "rev-parse", "HEAD")

	stream, err := prepareWith(t, repoDir, wtPath, PostMergeOptions{})
	if err != nil {
		t.Fatalf("prepare: %v\n%s", err, stream)
	}
	if spy.calls != 1 {
		t.Errorf("admission calls = %d, want 1", spy.calls)
	}
	if got := runGit(t, wtPath, "log", "-1", "--format=%s"); got != regenphase.CommitSubject {
		t.Errorf("tip subject = %q, want the regenerate commit", got)
	}
	if got := runGit(t, wtPath, "rev-parse", "HEAD~1"); got != lockBump {
		t.Errorf("the branch's own commit was rewritten: HEAD~1 = %s, want %s", got, lockBump)
	}
	if got := runGit(t, wtPath, "show", "--name-only", "--format=", "HEAD"); got != "gen.txt" {
		t.Errorf("the regenerate commit holds %q, want only gen.txt", got)
	}
}

func TestPrepareMergeFailsWhenTheLockPolicyRefusesTheBump(t *testing.T) {
	repoDir, wtPath, global := regenRepo(t)
	writeSweatfile(t, global, regenRepairSweatfile)
	spyAdmission(t, errors.New("the lock names an input the gate does not accept"))
	before := runGit(t, wtPath, "rev-parse", "HEAD")

	stream, err := prepareWith(t, repoDir, wtPath, PostMergeOptions{})
	if err == nil {
		t.Fatalf("a refused lock must fail the prepare:\n%s", stream)
	}
	if after := runGit(t, wtPath, "rev-parse", "HEAD"); after != before {
		t.Errorf("a refused lock moved HEAD from %s to %s", before, after)
	}
}

func TestPrepareMergeSkipRegenerateNeverTouchesTheLockPolicy(t *testing.T) {
	repoDir, wtPath, global := regenRepo(t)
	writeSweatfile(t, global, regenRepairSweatfile)
	spy := spyAdmission(t, errors.New("must not be reached"))

	stream, err := prepareWith(t, repoDir, wtPath, PostMergeOptions{SkipRegenerate: true})
	if err != nil {
		t.Fatalf("prepare: %v\n%s", err, stream)
	}
	if spy.calls != 0 {
		t.Errorf("SkipRegenerate reached the lock policy %d time(s)", spy.calls)
	}
	if got := runGit(t, wtPath, "log", "--format=%s", "main..HEAD"); strings.Contains(got, regenphase.CommitSubject) {
		t.Errorf("a regenerate commit was made despite SkipRegenerate:\n%s", got)
	}
}

func TestPrepareMergeLeavesABranchWithAnUnchangedLockAlone(t *testing.T) {
	repoDir, global := mergeFixture(t)
	writeSweatfile(t, global, regenRepairSweatfile)
	wtPath := setupWorktree(t, repoDir, "feat")
	writeFile(t, wtPath, "work.txt", "w")
	runGit(t, wtPath, "add", "work.txt")
	runGit(t, wtPath, "commit", "-m", "work")
	spy := spyAdmission(t, errors.New("must not be reached"))

	stream, err := prepareWith(t, repoDir, wtPath, PostMergeOptions{})
	if err != nil {
		t.Fatalf("prepare: %v\n%s", err, stream)
	}
	if spy.calls != 0 {
		t.Errorf("an unchanged lock reached the lock policy %d time(s)", spy.calls)
	}
	if strings.Contains(stream, "regenerate") {
		t.Errorf("an unchanged lock added to the stream:\n%s", stream)
	}
}
