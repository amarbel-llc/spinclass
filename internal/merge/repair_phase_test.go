package merge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"
)

// writeRepoSweatfile writes a repo-root sweatfile (loaded as the repo layer of
// the hierarchy) without dirtying the worktree.
func writeRepoSweatfile(t *testing.T, repoDir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, "sweatfile"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeGlobalSweatfile writes the global sweatfile layer ($HOME is the test
// root under setupRepo). Global is an unversioned, live layer, so it carries
// the knobs a test needs without touching the committed repo sweatfile.
func writeGlobalSweatfile(t *testing.T, content string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sweatfile"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupRepairRepo builds a repo + worktree branch with one feature commit ahead
// of main, and returns (repoDir, wtPath, preRepairHEAD).
func setupRepairRepo(t *testing.T, branch string) (repoDir, wtPath, head string) {
	t.Helper()
	repoDir = setupRepo(t)
	wtPath = setupWorktree(t, repoDir, branch)
	if err := os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "a.txt")
	runGit(t, wtPath, "commit", "-m", "feature commit")
	head = runGit(t, wtPath, "rev-parse", "HEAD")
	return repoDir, wtPath, head
}

// findTest returns the first test record whose description has the given prefix.
func findTest(tests []ndjsoncrap.Test, prefix string) (ndjsoncrap.Test, bool) {
	for _, tr := range tests {
		if strings.HasPrefix(tr.Description, prefix) {
			return tr, true
		}
	}
	return ndjsoncrap.Test{}, false
}

func runPrepare(t *testing.T, repoDir, wtPath, branch string) (pinnedSha string, tests []ndjsoncrap.Test, err error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	pinnedSha, err = PrepareMerge(ts, repoDir, wtPath, branch, "main", false, PostMergeOptions{})
	ts.Finish()
	return pinnedSha, testRecords(decodeRecords(t, buf.Bytes())), err
}

// TestPrepareMergeRepairNoop: a repair command that changes nothing leaves the
// pinned sha at the pre-repair HEAD and emits an "already conformant" point.
func TestPrepareMergeRepairNoop(t *testing.T) {
	repoDir, wtPath, head := setupRepairRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, "[hooks]\nrepair = \"true\"\n")

	pinnedSha, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err != nil {
		t.Fatalf("PrepareMerge: %v", err)
	}
	if pinnedSha != head {
		t.Errorf("pinnedSha = %s, want unchanged %s", pinnedSha, head)
	}
	tr, ok := findTest(tests, "repair feature")
	if !ok {
		t.Fatalf("no repair test point in %v", testDescs(tests))
	}
	if !tr.OK {
		t.Errorf("repair point not ok: %+v", tr)
	}
	if !strings.Contains(tr.Description, "already conformant") {
		t.Errorf("repair desc = %q, want 'already conformant'", tr.Description)
	}
}

// TestPrepareMergeRepairAmend: a repair command that amends HEAD re-pins to the
// amended sha, and the amended content is what FinishMerge lands on main.
func TestPrepareMergeRepairAmend(t *testing.T) {
	repoDir, wtPath, head := setupRepairRepo(t, "feature")
	// Modify a tracked file and fold it into HEAD, mimicking conformist
	// --commit --amend. (Unsigned commits — tests don't enable gpgsign.)
	writeRepoSweatfile(t, repoDir,
		"[hooks]\nrepair = \"printf fixed > file.txt && git add file.txt && git commit --amend --no-edit\"\n")

	pinnedSha, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err != nil {
		t.Fatalf("PrepareMerge: %v", err)
	}
	if pinnedSha == head {
		t.Fatalf("pinnedSha unchanged %s; expected re-pin to amended sha", head)
	}
	if branchTip := runGit(t, wtPath, "rev-parse", "HEAD"); branchTip != pinnedSha {
		t.Errorf("branch tip %s != pinned %s (branch ref should follow the amend)", branchTip, pinnedSha)
	}
	tr, ok := findTest(tests, "repair feature")
	if !ok || !tr.OK {
		t.Fatalf("expected ok repair point, got %+v (all: %v)", tr, testDescs(tests))
	}
	if !strings.Contains(tr.Description, "amended") {
		t.Errorf("repair desc = %q, want 'amended'", tr.Description)
	}

	// End-to-end: FinishMerge must land the amended (repaired) content on main.
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	if _, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature", "main", pinnedSha, false, true, nil, PostMergeOptions{}); err != nil {
		t.Fatalf("FinishMerge: %v\n%s", err, buf.String())
	}
	ts.Finish()
	if got := strings.TrimSpace(runGit(t, repoDir, "show", "main:file.txt")); got != "fixed" {
		t.Errorf("main:file.txt = %q, want repaired %q", got, "fixed")
	}
}

// TestPrepareMergeRepairFailureAborts: a repair command that exits nonzero
// fails PrepareMerge with a not-ok repair point and no pinned sha.
func TestPrepareMergeRepairFailureAborts(t *testing.T) {
	repoDir, wtPath, _ := setupRepairRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, "[hooks]\nrepair = \"echo boom >&2; exit 1\"\n")

	pinnedSha, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err == nil {
		t.Fatalf("expected PrepareMerge to fail, got pinnedSha=%s", pinnedSha)
	}
	if pinnedSha != "" {
		t.Errorf("pinnedSha = %q, want empty on failure", pinnedSha)
	}
	tr, ok := findTest(tests, "repair feature")
	if !ok {
		t.Fatalf("no repair test point in %v", testDescs(tests))
	}
	if tr.OK {
		t.Errorf("repair point should be not-ok on failure: %+v", tr)
	}
}

// TestPrepareMergeRepairDisabled: disable-repair suppresses the phase even with
// a (failing) repair command set — no repair point, merge proceeds.
func TestPrepareMergeRepairDisabled(t *testing.T) {
	repoDir, wtPath, head := setupRepairRepo(t, "feature")
	writeRepoSweatfile(t, repoDir,
		"[hooks]\nrepair = \"exit 1\"\ndisable-repair = true\n")

	pinnedSha, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err != nil {
		t.Fatalf("PrepareMerge should ignore disabled repair, got %v", err)
	}
	if pinnedSha != head {
		t.Errorf("pinnedSha = %s, want %s", pinnedSha, head)
	}
	if tr, ok := findTest(tests, "repair feature"); ok {
		t.Errorf("disabled repair should emit no repair point, got %+v", tr)
	}
}

// TestResolvedRepairNoopCompletes drives the full Resolved path (PrepareMerge +
// FinishMerge) with an active no-op repair, mirroring what `sc merge` runs, to
// localize the bats hang. inSession=true keeps the worktree (focus on whether
// the repair-active merge completes at all).
func TestResolvedRepairNoopCompletes(t *testing.T) {
	repoDir, _, _ := setupRepairRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, "[hooks]\nrepair = \"true\"\n")
	wtPath := filepath.Join(repoDir, ".worktrees", "feature")

	done := make(chan struct{})
	go func() {
		defer close(done)
		recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature", "main", false, true)
		if err != nil {
			t.Errorf("Resolved: %v", err)
		}
		if tr, ok := findTest(testRecords(recs), "repair feature"); !ok || !tr.OK {
			t.Errorf("expected ok repair point, got %+v", tr)
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Resolved with active repair did not complete within 20s (reproduces the bats hang)")
	}
}

// TestPrepareMergeRefusesDirtyWorktreeBeforeRepair (#345): an amending repair
// would fold uncommitted tracked edits into the pinned commit, so the merge is
// refused before repair runs and the edits are left untouched.
func TestPrepareMergeRefusesDirtyWorktreeBeforeRepair(t *testing.T) {
	repoDir, wtPath, head := setupRepairRepo(t, "feature")
	writeRepoSweatfile(t, repoDir,
		"[hooks]\nrepair = \"git add -A && git commit --amend --no-edit -q\"\n")
	// Without autostash git's own rebase refuses a dirty tree; with it (the
	// #200 / queued-merge scenario) the rebase succeeds and the edit survives.
	runGit(t, wtPath, "config", "rebase.autoStash", "true")
	dirty := filepath.Join(wtPath, "a.txt")
	if err := os.WriteFile(dirty, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	pinnedSha, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err == nil {
		t.Fatalf("expected refusal, got pinnedSha=%s (points: %v; HEAD %s, was %s)",
			pinnedSha, testDescs(tests), runGit(t, wtPath, "rev-parse", "HEAD"), head)
	}
	if !strings.Contains(err.Error(), "uncommitted changes") || !strings.Contains(err.Error(), "a.txt") {
		t.Errorf("err = %v, want uncommitted changes + a.txt", err)
	}
	if tr, ok := findTest(tests, "dirty check feature"); !ok || tr.OK {
		t.Errorf("want failing dirty check point, got %+v (all: %v)", tr, testDescs(tests))
	}
	if tr, ok := findTest(tests, "repair feature"); ok {
		t.Errorf("repair must not run, got %+v", tr)
	}
	if got := runGit(t, wtPath, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want unchanged %s", got, head)
	}
	if b, _ := os.ReadFile(dirty); string(b) != "edited" {
		t.Errorf("dirty edit lost: a.txt = %q", b)
	}
}

// TestPrepareMergeUntrackedFilesDoNotBlockRepair: untracked files cannot be
// amended into a commit by the guard's concern, so they do not trigger it.
func TestPrepareMergeUntrackedFilesDoNotBlockRepair(t *testing.T) {
	repoDir, wtPath, _ := setupRepairRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, "[hooks]\nrepair = \"true\"\n")
	if err := os.WriteFile(filepath.Join(wtPath, "scratch.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err != nil {
		t.Fatalf("PrepareMerge: %v", err)
	}
	if tr, ok := findTest(tests, "repair feature (already conformant)"); !ok || !tr.OK {
		t.Errorf("want ok already-conformant repair point, got %v", testDescs(tests))
	}
	if tr, ok := findTest(tests, "dirty check"); ok {
		t.Errorf("unexpected dirty check point: %+v", tr)
	}
}

// TestPrepareMergeNoRepairSkipsDirtyCheck: without an active repair phase the
// guard never runs, so a dirty worktree behaves exactly as before.
func TestPrepareMergeNoRepairSkipsDirtyCheck(t *testing.T) {
	repoDir, wtPath, _ := setupRepairRepo(t, "feature")
	if err := os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	// autoStash lets the rebase get past the dirty tree, so the (absent) guard
	// is what is actually under test.
	runGit(t, wtPath, "config", "rebase.autoStash", "true")

	_, tests, err := runPrepare(t, repoDir, wtPath, "feature")
	if err != nil {
		t.Fatalf("PrepareMerge should proceed without repair: %v (points: %v)", err, testDescs(tests))
	}
	if tr, ok := findTest(tests, "dirty check"); ok {
		t.Errorf("unexpected dirty check point: %+v", tr)
	}
}

func testDescs(tests []ndjsoncrap.Test) []string {
	out := make([]string, len(tests))
	for i, tr := range tests {
		out[i] = tr.Description
	}
	return out
}
