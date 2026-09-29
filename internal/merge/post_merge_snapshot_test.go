package merge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"
)

// kroneTarget is a sweatfile body declaring one named [[post-merge]] target.
func kroneTarget(command string) string {
	return fmt.Sprintf("[[post-merge]]\nname = \"krone\"\ncommand = %q\n", command)
}

// midMergeSweatfileEdit returns a between-hook that overwrites the live
// worktree sweatfile, simulating an edit in the async-merge window.
func midMergeSweatfileEdit(t *testing.T, wtPath, content string) func() {
	return func() {
		if err := os.WriteFile(filepath.Join(wtPath, "sweatfile"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// assertKroneRan asserts the krone post-merge node ran and its output holds
// want and not notWant.
func assertKroneRan(t *testing.T, recs []ndjsoncrap.Record, want, notWant string) {
	t.Helper()
	n, ok := findNode(recs, "post-merge krone")
	if !ok {
		t.Fatalf("no post-merge krone node: %v", nodeNames(recs))
	}
	if !strings.Contains(n.output, want) || strings.Contains(n.output, notWant) {
		t.Errorf("krone output = %q, want %s and not %s", n.output, want, notWant)
	}
}

// assertUnqueuedPath asserts the merge did not go through the queued landing
// (no landing-fetch point), i.e. [hooks].disable-merge-queue took effect.
func assertUnqueuedPath(t *testing.T, recs []ndjsoncrap.Record) {
	t.Helper()
	if _, queued := findTest(testRecords(recs), "fetch origin/main (landing)"); queued {
		t.Error("expected the unqueued path, but saw a landing fetch")
	}
}

// #300: the named-target phase reads the sweatfile of the commit that landed,
// not a live edit made to the session worktree after the pin.
func TestPostMergeNamedTargetReadsLandedSweatfileNotLiveEdit(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	commitSweatfile(t, wtPath, kroneTarget("echo COMMITTED"))

	recs, err := runFinishWithMidEdit(t, repoDir, wtPath, "feature", false, PostMergeOptions{},
		midMergeSweatfileEdit(t, wtPath, kroneTarget("echo EDITED")))
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	assertKroneRan(t, recs, "COMMITTED", "EDITED")
}

// The legacy [hooks].post-merge string and the phase cap also come from the
// landed commit.
func TestPostMergeLegacyHookAndTimeoutReadLandedSweatfile(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	outFile := filepath.Join(t.TempDir(), "out")
	sweatfile := func(word, timeout string) string {
		return fmt.Sprintf("[hooks]\npost-merge = 'echo %s $SPINCLASS_POST_MERGE_TIMEOUT > %s'\npost-merge-timeout = %q\n",
			word, outFile, timeout)
	}
	commitSweatfile(t, wtPath, sweatfile("COMMITTED", "7m"))

	recs, err := runFinishWithMidEdit(t, repoDir, wtPath, "feature", false, PostMergeOptions{},
		midMergeSweatfileEdit(t, wtPath, sweatfile("EDITED", "3m")))
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	tests := testRecords(recs)
	tr, found := findTest(tests, "post-merge feature")
	if !found {
		t.Fatalf("no post-merge point: %v", testDescs(tests))
	}
	if !tr.OK {
		t.Errorf("post-merge point not ok: %+v", tr)
	}
	raw, readErr := os.ReadFile(outFile)
	if readErr != nil {
		t.Fatalf("hook did not run: %v", readErr)
	}
	if got := strings.TrimSpace(string(raw)); got != "COMMITTED 7m0s" {
		t.Errorf("hook wrote %q, want %q", got, "COMMITTED 7m0s")
	}
}

func TestPostMergeUnqueuedPathReadsLandedSweatfile(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeGlobalSweatfile(t, "[hooks]\ndisable-merge-queue = true\n")
	commitSweatfile(t, wtPath, kroneTarget("echo COMMITTED"))

	recs, err := runFinishWithMidEdit(t, repoDir, wtPath, "feature", false, PostMergeOptions{},
		midMergeSweatfileEdit(t, wtPath, kroneTarget("echo EDITED")))
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	assertKroneRan(t, recs, "COMMITTED", "EDITED")
	assertUnqueuedPath(t, recs)
}

// After a queue rebase the landing sha differs from the pin; the phase must
// read the landing sha's sweatfile. Both the pinned tree and the live worktree
// say BASE here, so only a landing-sha read sees RACED.
func TestPostMergeRebasedLandingReadsLandingShaSweatfile(t *testing.T) {
	repoDir := setupRepo(t)
	commitSweatfile(t, repoDir, kroneTarget("echo BASE"))
	wtPath := setupWorktree(t, repoDir, "feature-race")

	pinnedSha, _, rep, ts, buf := prepareRacedMerge(t, repoDir, wtPath, "feature-race",
		"a.txt", "a", "sweatfile", kroneTarget("echo RACED"))
	if _, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature-race", "main", pinnedSha, false, true, nil, PostMergeOptions{}); err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	ts.Finish()
	recs := decodeRecords(t, buf.Bytes())
	assertKroneRan(t, recs, "RACED", "BASE")
}

// An unparseable landed sweatfile skips the phase loudly; the merge stays
// landed and the live worktree is never consulted. The mid-merge edit puts a
// VALID krone target in the live worktree, so a fallback to it would run a
// krone node.
func TestPostMergeUnreadableLandedSweatfileWarns(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	commitSweatfile(t, wtPath, "[[post-merge]\nbroken")

	recs, err := runFinishWithMidEdit(t, repoDir, wtPath, "feature", false, PostMergeOptions{},
		midMergeSweatfileEdit(t, wtPath, kroneTarget("echo LIVE")))
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Error("merge should have landed")
	}
	tests := testRecords(recs)
	tr, found := findTest(tests, "post-merge sweatfile")
	if !found {
		t.Fatalf("no post-merge sweatfile warning: %v", testDescs(tests))
	}
	if tr.OK {
		t.Errorf("warning point should be not-ok: %+v", tr)
	}
	if sev := diagString(tr.Diagnostic, "severity"); sev != "warn" {
		t.Errorf("severity = %q, want warn", sev)
	}
	if _, ran := findNode(recs, "post-merge krone"); ran {
		t.Error("post-merge ran the live worktree's target instead of skipping")
	}

	open := map[int]bool{}
	for _, rec := range recs {
		switch r := rec.(type) {
		case ndjsoncrap.NodeStart:
			open[r.TP] = true
		case ndjsoncrap.NodeEnd:
			delete(open, r.TP)
		}
	}
	if len(open) != 0 {
		t.Errorf("node_start without node_end for tp %v", open)
	}
}
