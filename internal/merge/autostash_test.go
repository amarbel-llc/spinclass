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
	gitstore "code.linenisgreat.com/spinclass/internal/store/git"
)

// autostashRepo is a repo whose session worktree "feat" has one commit ahead
// of main, with main itself moved on so the rebase is real.
func autostashRepo(t *testing.T) (repoDir, wtPath string) {
	t.Helper()
	repoDir = setupRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wtPath = setupWorktree(t, repoDir, "feat")
	writeFile(t, wtPath, "work.txt", "w")
	runGit(t, wtPath, "add", "work.txt")
	runGit(t, wtPath, "commit", "-m", "work")
	writeFile(t, repoDir, "main.txt", "m")
	runGit(t, repoDir, "add", "main.txt")
	runGit(t, repoDir, "commit", "-m", "main moves")
	return repoDir, wtPath
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func shelfCount(t *testing.T, wtPath string) int {
	t.Helper()
	n, err := gitstore.CountShelfEntries(wtPath)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// #416: uncommitted tracked edits no longer block the merge. They are shelved
// before the rebase, restored right after the synchronous prefix, and the
// shelf entry is dropped; untracked files are never touched.
func TestMergeAutostashesTrackedEditsAndRestoresThem(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	writeFile(t, wtPath, "file.txt", "edited, not committed")
	writeFile(t, wtPath, "notes.txt", "untracked")

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feat", "main", false, true)
	if err != nil {
		t.Fatalf("merge failed: %v (points %v)", err, testDescs(testRecords(recs)))
	}
	if log := runGit(t, repoDir, "log", "--oneline", "main"); !strings.Contains(log, "work") {
		t.Errorf("the merge did not land: %s", log)
	}
	if got := readFile(t, wtPath, "file.txt"); got != "edited, not committed" {
		t.Errorf("file.txt = %q, want the uncommitted edit back", got)
	}
	if got := readFile(t, wtPath, "notes.txt"); got != "untracked" {
		t.Errorf("notes.txt = %q, want the untracked file untouched", got)
	}
	if n := shelfCount(t, wtPath); n != 0 {
		t.Errorf("%d shelf entries left, want the autostash dropped after a clean restore", n)
	}
	tests := testRecords(recs)
	if tr, ok := findTest(tests, "autostash feat"); !ok || !tr.OK || !strings.Contains(tr.Description, "file.txt") || strings.Contains(tr.Description, "notes.txt") {
		t.Errorf("want an ok autostash point naming file.txt only, got %+v (all %v)", tr, testDescs(tests))
	}
	if tr, ok := findTest(tests, "restore autostash feat"); !ok || !tr.OK || !strings.Contains(tr.Description, "file.txt") {
		t.Errorf("want an ok restore point naming file.txt, got %+v (all %v)", tr, testDescs(tests))
	}
}

// An out-of-session merge (a terminal `sc merge <target>`) of a dirty session
// lands, and keeps the worktree the restored edits live in instead of trying
// to remove it.
func TestOutOfSessionMergeKeepsAWorktreeWithRestoredEdits(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	writeFile(t, wtPath, "file.txt", "edited, not committed")
	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feat", "main", false, false)
	if err != nil {
		t.Fatalf("merge failed: %v (points %v)", err, testDescs(testRecords(recs)))
	}
	if got := readFile(t, wtPath, "file.txt"); got != "edited, not committed" {
		t.Errorf("file.txt = %q, want the edit kept in the worktree", got)
	}
	if tr, ok := findTest(testRecords(recs), "keep worktree feat"); !ok || !strings.Contains(tr.Description, "uncommitted") {
		t.Errorf("want a keep-worktree point naming the uncommitted changes, got %+v (all %v)", tr, testDescs(testRecords(recs)))
	}
}

// With autostash on, an amending repair sees a clean tree: the uncommitted
// edit is not folded into the pinned commit, and comes back afterwards. The
// shelf lives in .spinclass/, which a session worktree excludes (the default
// [git] excludes), so repair cannot stage it either.
func TestMergeAutostashKeepsUncommittedEditsOutOfRepair(t *testing.T) {
	repoDir, wtPath, _ := setupRepairRepoWithSweatfile(t, "feature",
		"[hooks]\nrepair = \"git add -A && git commit --amend --no-edit -q\"\n")
	exclude := strings.TrimSpace(runGit(t, wtPath, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
	writeFile(t, filepath.Dir(exclude), filepath.Base(exclude), ".spinclass/\n")
	writeFile(t, wtPath, "a.txt", "edited, not committed")

	pinned, tests, err := runPrepareWith(t, repoDir, wtPath, "feature", PostMergeOptions{})
	if err != nil {
		t.Fatalf("PrepareMerge: %v (points %v)", err, testDescs(tests))
	}
	if got := runGit(t, wtPath, "show", pinned+":a.txt"); got == "edited, not committed" {
		t.Error("repair amended the uncommitted edit into the pinned commit")
	}
	if files := runGit(t, wtPath, "show", "--name-only", "--format=", pinned); strings.Contains(files, ".spinclass") {
		t.Errorf("the pinned commit carries shelf files: %s", files)
	}
	if got := readFile(t, wtPath, "a.txt"); got != "edited, not committed" {
		t.Errorf("a.txt = %q, want the edit restored", got)
	}
	if n := shelfCount(t, wtPath); n != 0 {
		t.Errorf("%d shelf entries left", n)
	}
}

// #418: a file staged intent-to-add (`git add -N`, which nix builds of a dirty
// tree need) used to stop the rebase with "Cannot autostash"; the merge must
// land and the file come back, still intent-to-add.
func TestMergeAutostashCarriesAnIntentToAddFile(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	writeFile(t, wtPath, "new_test.go", "package x")
	runGit(t, wtPath, "add", "-N", "new_test.go")

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feat", "main", false, true)
	if err != nil {
		t.Fatalf("merge failed: %v (points %v)", err, testDescs(testRecords(recs)))
	}
	if log := runGit(t, repoDir, "log", "--oneline", "main"); !strings.Contains(log, "work") {
		t.Errorf("the merge did not land: %s", log)
	}
	if files := runGit(t, repoDir, "show", "--name-only", "--format=", "main"); strings.Contains(files, "new_test.go") {
		t.Errorf("the intent-to-add file was committed: %s", files)
	}
	if got := readFile(t, wtPath, "new_test.go"); got != "package x" {
		t.Errorf("new_test.go = %q, want it back", got)
	}
	if tracked := runGit(t, wtPath, "ls-files", "--", "new_test.go"); tracked != "new_test.go" {
		t.Errorf("new_test.go is no longer intent-to-add (ls-files %q)", tracked)
	}
	if n := shelfCount(t, wtPath); n != 0 {
		t.Errorf("%d shelf entries left", n)
	}
}

// A clean worktree emits no autostash points at all.
func TestMergeWithACleanWorktreeDoesNotAutostash(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feat", "main", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findTest(testRecords(recs), "autostash"); ok {
		t.Errorf("a clean merge emitted an autostash point: %v", testDescs(testRecords(recs)))
	}
}

// autostash off is today's behaviour: the rebase refuses the dirty tree,
// nothing lands, the edit stays where it was and nothing is shelved.
func TestMergeWithAutostashOffRefusesADirtyTree(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	writeFile(t, wtPath, "file.txt", "edited, not committed")
	before := runGit(t, repoDir, "rev-parse", "main")

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, err := Resolved(&mockExecutor{}, rep, ts, repoDir, wtPath, "feat", "main", false, true, PostMergeOptions{NoAutostash: true})
	ts.Finish()
	if err == nil {
		t.Fatal("a dirty-tree merge with autostash off succeeded")
	}
	if after := runGit(t, repoDir, "rev-parse", "main"); after != before {
		t.Errorf("main moved: %s -> %s", before, after)
	}
	if got := readFile(t, wtPath, "file.txt"); got != "edited, not committed" {
		t.Errorf("file.txt = %q, want the edit untouched", got)
	}
	if n := shelfCount(t, wtPath); n != 0 {
		t.Errorf("%d shelf entries, want none with autostash off", n)
	}
}

// A restore that conflicts keeps the shelf entry, names it in the result, and
// the merge still lands: the commit was pinned before the restore.
func TestMergeAutostashConflictingRestoreKeepsTheEntry(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	writeFile(t, repoDir, "file.txt", "main changed it too")
	runGit(t, repoDir, "commit", "-am", "main edits file.txt")
	writeFile(t, wtPath, "file.txt", "session edit, not committed")

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feat", "main", false, true)
	if err != nil {
		t.Fatalf("merge failed: %v (points %v)", err, testDescs(testRecords(recs)))
	}
	if log := runGit(t, repoDir, "log", "--oneline", "main"); !strings.Contains(log, "work") {
		t.Errorf("the merge did not land: %s", log)
	}
	if n := shelfCount(t, wtPath); n != 1 {
		t.Fatalf("%d shelf entries, want the conflicting autostash kept", n)
	}
	tr, ok := findTest(testRecords(recs), "restore autostash feat")
	if !ok || tr.Directive == nil || tr.Directive.Kind != "skip" || !strings.Contains(tr.Directive.Reason, "stash entry 1") {
		t.Errorf("want a skipped restore point naming the kept stash entry 1, got %+v", tr)
	}
}

// A merge that fails after the stash (here: a cancelled repair) still puts the
// edits back.
func TestMergeAutostashRestoresWhenThePrefixFails(t *testing.T) {
	repoDir, wtPath, _ := setupRepairRepoWithSweatfile(t, "feature", "[hooks]\nrepair = \"sleep 600\"\n")
	writeFile(t, wtPath, "file.txt", "edited, not committed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(500*time.Millisecond, cancel)

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, err := PrepareMerge(ctx, ts, repoDir, wtPath, "feature", "main", false, PostMergeOptions{})
	ts.Finish()
	if err == nil {
		t.Fatal("the cancelled merge succeeded")
	}
	if got := readFile(t, wtPath, "file.txt"); got != "edited, not committed" {
		t.Errorf("file.txt = %q, want the edit restored after the failed prefix", got)
	}
	if n := shelfCount(t, wtPath); n != 0 {
		t.Errorf("%d shelf entries left after a clean restore", n)
	}
}

// Edits made after the prefix, while the gate runs, are the session's own: the
// restore already happened, so nothing later overwrites them.
func TestMergeAutostashLeavesEditsMadeDuringTheGateAlone(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	writeFile(t, wtPath, "file.txt", "edited before the merge")

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	pm := PostMergeOptions{}.StampStartedAt()
	pinned, err := PrepareMerge(context.Background(), ts, repoDir, wtPath, "feat", "main", false, pm)
	if err != nil {
		ts.Finish()
		t.Fatal(err)
	}
	writeFile(t, wtPath, "file.txt", "edited while the gate ran")
	writeFile(t, wtPath, "later.txt", "new")
	if _, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts, repoDir, wtPath, "feat", "main", pinned, false, true, nil, pm); err != nil {
		ts.Finish()
		t.Fatal(err)
	}
	ts.Finish()
	if got := readFile(t, wtPath, "file.txt"); got != "edited while the gate ran" {
		t.Errorf("file.txt = %q, want the edit made during the gate", got)
	}
	if n := shelfCount(t, wtPath); n != 0 {
		t.Errorf("%d shelf entries left", n)
	}
}
