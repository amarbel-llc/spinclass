package merge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/mergelock"
)

// prepareRacedMerge builds the common merge-queue race fixture: a worktree
// session on branch with one commit (writing sessionFile), pinned by
// PrepareMerge, followed by a racing commit landing directly on main (writing
// raceFile with raceContent) AFTER the pin. Returns the pinned sha, the racing
// main tip, and the shared reporter/stream/buffer for FinishMerge.
func prepareRacedMerge(t *testing.T, repoDir, wtPath, branch, sessionFile, sessionContent, raceFile, raceContent string) (pinnedSha, raceSha string, rep *crap.Reporter, ts *crap.TestStream, buf *bytes.Buffer) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(wtPath, sessionFile), []byte(sessionContent), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", sessionFile)
	runGit(t, wtPath, "commit", "-m", "session commit")

	buf = &bytes.Buffer{}
	rep = crap.NewReporter(buf, crap.ReporterOptions{})
	ts = rep.TestStream(0)
	var err error
	pinnedSha, err = PrepareMerge(ts, repoDir, wtPath, branch, "main", false)
	if err != nil {
		t.Fatalf("PrepareMerge: %v\n%s", err, buf.String())
	}

	// The race: a commit lands directly on main after the pin.
	if err := os.WriteFile(filepath.Join(repoDir, raceFile), []byte(raceContent), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", raceFile)
	runGit(t, repoDir, "commit", "-m", "racing commit on main")
	raceSha = runGit(t, repoDir, "rev-parse", "main")
	if raceSha == pinnedSha {
		t.Fatal("test setup: expected the racing commit to move main past the pin")
	}
	return pinnedSha, raceSha, rep, ts, buf
}

// assertNoLandWorktrees fails if any transient landing worktree dir was left
// under <repo>/.worktrees/.
func assertNoLandWorktrees(t *testing.T, repoDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoDir, ".worktrees"))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read .worktrees: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), LandWorktreePrefix) {
			t.Errorf("leftover landing worktree: %s", e.Name())
		}
	}
}

// TestFinishMergeQueueRebasesLandingAndGatesOnLandingSha is the core #235
// scenario: main moves (non-conflictingly) after PrepareMerge pins, and
// FinishMerge — instead of failing the ff-only merge — rebases the pinned
// commits onto the moved tip in a transient landing worktree, runs the
// pre-merge gate against that LANDING sha (not pinnedSha), and lands it. The
// hook records the sha it actually gated so the test can prove the gate ran on
// the landed tip.
func TestFinishMergeQueueRebasesLandingAndGatesOnLandingSha(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-race")
	root := filepath.Dir(repoDir)

	// Pre-merge hook records the HEAD of the build worktree it runs in — the
	// hook sha FinishMerge gates on.
	hookShaFile := filepath.Join(root, "hook-sha")
	sweatfileBody := "[hooks]\npre-merge = \"git rev-parse HEAD > " + hookShaFile + "\"\n"
	if err := os.WriteFile(filepath.Join(wtPath, "sweatfile"), []byte(sweatfileBody), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	pinnedSha, _, rep, ts, buf := prepareRacedMerge(t, repoDir, wtPath, "feature-race",
		"a.txt", "a", "racing.txt", "race")

	_, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature-race", "main", pinnedSha, false, true, nil)
	ts.Finish()
	if err != nil {
		t.Fatalf("FinishMerge: %v\n%s", err, buf.String())
	}

	// main contains both the racing commit and the session commit, at a
	// rebased tip distinct from the pin.
	mainTip := runGit(t, repoDir, "rev-parse", "main")
	if mainTip == pinnedSha {
		t.Errorf("main = pinned sha %s; expected a rebased landing past the racing commit", pinnedSha)
	}
	mainLog := runGit(t, repoDir, "log", "--oneline", "main")
	if !strings.Contains(mainLog, "session commit") || !strings.Contains(mainLog, "racing commit on main") {
		t.Errorf("expected both commits on main, got:\n%s", mainLog)
	}

	// The gate ran on the LANDING sha (== the landed tip), not pinnedSha.
	gated, readErr := os.ReadFile(hookShaFile)
	if readErr != nil {
		t.Fatalf("hook did not record its sha: %v\n%s", readErr, buf.String())
	}
	gatedSha := strings.TrimSpace(string(gated))
	if gatedSha == pinnedSha {
		t.Errorf("gate ran on pinnedSha %s; want the landing sha", pinnedSha)
	}
	if gatedSha != mainTip {
		t.Errorf("gate ran on %s, want landed tip %s", gatedSha, mainTip)
	}

	// The merge test point carries the distinct rebased-landing label, and no
	// transient landing worktree is left behind.
	tests := testRecords(decodeRecords(t, buf.Bytes()))
	found := false
	for _, tr := range tests {
		if tr.Description == "merge feature-race (rebased onto moved main)" && tr.OK {
			found = true
		}
	}
	if !found {
		t.Errorf("expected passing 'merge feature-race (rebased onto moved main)' record, got: %+v", tests)
	}
	assertNoLandWorktrees(t, repoDir)
}

// TestFinishMergeQueueIntegrationConflict: the racing main commit conflicts
// with the session commit. FinishMerge must fail with ErrIntegrationConflict,
// leave main unmoved beyond the racing commit, leave no transient landing
// worktrees behind, and release the landing lock.
func TestFinishMergeQueueIntegrationConflict(t *testing.T) {
	repoDir := setupRepo(t) // main: file.txt="initial"
	wtPath := setupWorktree(t, repoDir, "feature-conflict-race")

	// Both sides edit file.txt → the landing rebase conflicts.
	pinnedSha, raceSha, rep, ts, buf := prepareRacedMerge(t, repoDir, wtPath, "feature-conflict-race",
		"file.txt", "feature", "file.txt", "racing")

	_, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature-conflict-race", "main", pinnedSha, false, true, nil)
	ts.Finish()
	if err == nil {
		t.Fatalf("expected FinishMerge to fail on the integration conflict\n%s", buf.String())
	}
	if !errors.Is(err, ErrIntegrationConflict) {
		t.Errorf("errors.Is(err, ErrIntegrationConflict) = false; err = %v", err)
	}

	// main is unmoved beyond the racing commit.
	if mainTip := runGit(t, repoDir, "rev-parse", "main"); mainTip != raceSha {
		t.Errorf("main = %s, want the racing tip %s (nothing should have landed)", mainTip, raceSha)
	}

	// The failure surfaced as a failing 'land <branch>' test point.
	tests := testRecords(decodeRecords(t, buf.Bytes()))
	found := false
	for _, tr := range tests {
		if tr.Description == "land feature-conflict-race" && !tr.OK {
			found = true
		}
	}
	if !found {
		t.Errorf("expected failing 'land feature-conflict-race' record, got: %+v", tests)
	}

	assertNoLandWorktrees(t, repoDir)

	// The lock was released: a fresh Acquire on the same lock dir succeeds
	// (flock is per open-file-description, so a leaked lock in this same
	// process would block this).
	lockDir, dirErr := git.CommonGitDir(repoDir)
	if dirErr != nil {
		t.Fatalf("CommonGitDir: %v", dirErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lock, lockErr := mergelock.Acquire(ctx, lockDir, "test/probe", nil)
	if lockErr != nil {
		t.Fatalf("landing lock was not released: %v", lockErr)
	}
	_ = lock.Release()
}

// TestFinishMergeQueueDisabledKnobFailsAtFF: with disable-merge-queue = true
// and a moved default branch, FinishMerge runs the pre-#235 path and fails at
// the ff-only merge — no lock, no landing rebase, no ErrIntegrationConflict.
func TestFinishMergeQueueDisabledKnobFailsAtFF(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-knob")

	if err := os.WriteFile(filepath.Join(wtPath, "sweatfile"), []byte("[hooks]\ndisable-merge-queue = true\n"), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	pinnedSha, raceSha, rep, ts, buf := prepareRacedMerge(t, repoDir, wtPath, "feature-knob",
		"a.txt", "a", "racing.txt", "race")

	_, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature-knob", "main", pinnedSha, false, true, nil)
	ts.Finish()
	if err == nil {
		t.Fatalf("expected ff-only failure with the queue disabled\n%s", buf.String())
	}
	if errors.Is(err, ErrIntegrationConflict) {
		t.Errorf("queue-disabled path must not produce ErrIntegrationConflict, got %v", err)
	}

	// Today's failure shape: a failing 'merge <branch>' point, no landing.
	tests := testRecords(decodeRecords(t, buf.Bytes()))
	found := false
	for _, tr := range tests {
		if strings.HasPrefix(tr.Description, "land ") {
			t.Errorf("queue-disabled path attempted a landing rebase: %+v", tr)
		}
		if tr.Description == "merge feature-knob" && !tr.OK {
			found = true
		}
	}
	if !found {
		t.Errorf("expected failing 'merge feature-knob' record, got: %+v", tests)
	}

	// Nothing landed; main is still the racing tip.
	if mainTip := runGit(t, repoDir, "rev-parse", "main"); mainTip != raceSha {
		t.Errorf("main = %s, want the racing tip %s", mainTip, raceSha)
	}
	assertNoLandWorktrees(t, repoDir)
}

// TestFinishMergeQueueRebasedLandingTeardown: a non-inSession merge with a
// rebased landing must still tear down — the session branch tip (pinnedSha) is
// no longer an ancestor of main after the landing rebase, so the teardown
// exercises the branch force-delete path.
func TestFinishMergeQueueRebasedLandingTeardown(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-teardown")

	pinnedSha, _, rep, ts, buf := prepareRacedMerge(t, repoDir, wtPath, "feature-teardown",
		"a.txt", "a", "racing.txt", "race")

	_, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature-teardown", "main", pinnedSha, false, false, nil)
	ts.Finish()
	if err != nil {
		t.Fatalf("FinishMerge: %v\n%s", err, buf.String())
	}

	// Worktree removed, branch force-deleted, both commits on main.
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Error("expected the session worktree to be removed")
	}
	if git.BranchExists(repoDir, "feature-teardown") {
		t.Error("expected branch feature-teardown to be deleted (force-delete path)")
	}
	mainLog := runGit(t, repoDir, "log", "--oneline", "main")
	if !strings.Contains(mainLog, "session commit") || !strings.Contains(mainLog, "racing commit on main") {
		t.Errorf("expected both commits on main, got:\n%s", mainLog)
	}

	tests := testRecords(decodeRecords(t, buf.Bytes()))
	assertOk := func(desc string) {
		t.Helper()
		for _, tr := range tests {
			if tr.Description == desc && tr.OK {
				return
			}
		}
		t.Errorf("expected passing %q record, got: %+v", desc, tests)
	}
	assertOk("merge feature-teardown (rebased onto moved main)")
	assertOk("remove worktree feature-teardown")
	assertOk("delete branch feature-teardown")
	assertNoLandWorktrees(t, repoDir)
}

// TestFinishMergeQueuePostPinCommitsSurviveRebasedTeardown: the pin contract
// allows commits to land on the session branch after PrepareMerge pins ("left
// for a later merge"). When the landing was ALSO rebased (default branch moved
// after the pin), the old unconditional `branch -D` would force-delete the
// advanced ref after `worktree remove` — stranding the post-pin commits
// unreachable. The fix must instead keep both the worktree and the branch:
// the pinned commit lands, the post-pin commit stays reachable, and a "keep
// worktree" test point records why teardown was skipped.
func TestFinishMergeQueuePostPinCommitsSurviveRebasedTeardown(t *testing.T) {
	repoDir := setupRepo(t)
	wtPath := setupWorktree(t, repoDir, "feature-postpin")

	pinnedSha, _, rep, ts, buf := prepareRacedMerge(t, repoDir, wtPath, "feature-postpin",
		"a.txt", "a", "racing.txt", "race")

	// The other half of the race: a commit lands on the SESSION branch after
	// the pin (the pin contract's "left for a later merge" window).
	if err := os.WriteFile(filepath.Join(wtPath, "later.txt"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "later.txt")
	runGit(t, wtPath, "commit", "-m", "post-pin commit")
	postPinSha := runGit(t, wtPath, "rev-parse", "HEAD")

	_, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature-postpin", "main", pinnedSha, false, false, nil)
	ts.Finish()
	if err != nil {
		t.Fatalf("FinishMerge: %v\n%s", err, buf.String())
	}

	// The pinned session commit and the racing commit both landed on main; the
	// post-pin commit did NOT.
	mainLog := runGit(t, repoDir, "log", "--oneline", "main")
	if !strings.Contains(mainLog, "session commit") || !strings.Contains(mainLog, "racing commit on main") {
		t.Errorf("expected pinned + racing commits on main, got:\n%s", mainLog)
	}
	if strings.Contains(mainLog, "post-pin commit") {
		t.Errorf("post-pin commit must not land in this merge, got:\n%s", mainLog)
	}

	// The branch ref survives at the post-pin tip (the commit stays reachable)
	// and the worktree directory is still on disk.
	if !git.BranchExists(repoDir, "feature-postpin") {
		t.Fatal("expected branch feature-postpin to survive (post-pin commits would be unreachable)")
	}
	if tip := runGit(t, repoDir, "rev-parse", "refs/heads/feature-postpin"); tip != postPinSha {
		t.Errorf("branch tip = %s, want the post-pin commit %s", tip, postPinSha)
	}
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Errorf("expected the session worktree to survive: %v", statErr)
	}

	// Teardown was skipped via the "keep worktree" point — no remove/delete.
	tests := testRecords(decodeRecords(t, buf.Bytes()))
	foundKeep := false
	for _, tr := range tests {
		switch tr.Description {
		case "keep worktree feature-postpin (commits added since pin; left for a later merge)":
			if tr.OK {
				foundKeep = true
			}
		case "remove worktree feature-postpin", "delete branch feature-postpin":
			t.Errorf("teardown ran despite post-pin commits: %+v", tr)
		}
	}
	if !foundKeep {
		t.Errorf("expected passing 'keep worktree feature-postpin (commits added since pin; left for a later merge)' record, got: %+v", tests)
	}
	assertNoLandWorktrees(t, repoDir)
}
