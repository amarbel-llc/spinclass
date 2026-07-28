package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/mergelock"
)

// setupPostMergeRepo builds a repo + worktree branch with one feature commit
// ahead of main and returns (repoDir, wtPath, pinnedSha-to-be).
func setupPostMergeRepo(t *testing.T, branch string) (repoDir, wtPath string) {
	t.Helper()
	repoDir = setupRepo(t)
	wtPath = setupWorktree(t, repoDir, branch)
	if err := os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "a.txt")
	runGit(t, wtPath, "commit", "-m", "feature commit")
	return repoDir, wtPath
}

// runFinish drives PrepareMerge + FinishMerge over a buffered Reporter and
// returns the decoded test points plus FinishMerge's error. inSession=true so
// the session worktree survives teardown (the common merge-this-session case).
func runFinish(t *testing.T, repoDir, wtPath, branch string, gitSync bool) ([]ndjsoncrap.Test, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	pinnedSha, prepErr := PrepareMerge(ts, repoDir, wtPath, branch, "main", gitSync)
	if prepErr != nil {
		ts.Finish()
		t.Fatalf("PrepareMerge: %v\n%s", prepErr, buf.String())
	}
	_, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, branch, "main", pinnedSha, gitSync, true, nil)
	ts.Finish()
	return testRecords(decodeRecords(t, buf.Bytes())), err
}

// TestPostMergeRunsUnderTheMergeLock is the load-bearing property of FDR
// 0023: the per-repo landing lock (FDR 0022) makes a merge exclusive END TO
// END, and the post-merge hook is part of the merge. While the hook runs, no
// sibling session may perform any part of a merge — otherwise two sessions'
// deploys could interleave and the older one could win, which is the failure
// the queue exists to prevent.
//
// The hook blocks until the test releases it. While it is blocked, the test
// tries to acquire the same per-repo lock from a second file descriptor;
// flock is per-open-file-description, so this genuinely contends and MUST
// time out. After the merge returns, the lock must be free again.
func TestPostMergeRunsUnderTheMergeLock(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")

	signals := t.TempDir()
	started := filepath.Join(signals, "hook-started")
	release := filepath.Join(signals, "hook-release")
	writeRepoSweatfile(t, repoDir, fmt.Sprintf(
		"[hooks]\npost-merge = \"touch %s; while [ ! -e %s ]; do sleep 0.05; done\"\n",
		started, release,
	))

	type result struct {
		tests []ndjsoncrap.Test
		err   error
	}
	done := make(chan result, 1)
	go func() {
		tests, err := runFinish(t, repoDir, wtPath, "feature", false)
		done <- result{tests, err}
	}()

	// Wait for the hook to be running.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("post-merge hook never started within 30s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A sibling session must NOT be able to start a merge right now: the hook
	// is running and the landing lock is still held.
	lockDir, err := git.CommonGitDir(repoDir)
	if err != nil {
		t.Fatalf("CommonGitDir: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lock, acqErr := mergelock.Acquire(ctx, lockDir, "sibling-session", nil)

	// Let the hook finish regardless of the assertion outcome, so the
	// goroutine never leaks and the failure message is the useful one.
	if lock != nil {
		if relErr := lock.Release(); relErr != nil {
			t.Errorf("releasing probe lock: %v", relErr)
		}
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if acqErr == nil {
		t.Fatal("a sibling session acquired the merge lock while the post-merge " +
			"hook was still running — the merge must stay exclusive end to end, " +
			"or two sessions' deploys can interleave")
	}
	if !errors.Is(acqErr, context.DeadlineExceeded) {
		t.Fatalf("expected the probe to time out contending for a held lock, got %v", acqErr)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("FinishMerge: %v", r.err)
		}
		tr, ok := findTest(r.tests, "post-merge feature")
		if !ok {
			t.Fatalf("no post-merge test point in %v", testDescs(r.tests))
		}
		if !tr.OK {
			t.Errorf("post-merge point not ok: %+v", tr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("FinishMerge did not complete within 30s after releasing the hook")
	}

	// ...and once the merge returns, the lock must be free again — held for
	// the whole merge is the contract, held forever is a wedged queue.
	afterCtx, afterCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer afterCancel()
	after, afterErr := mergelock.Acquire(afterCtx, lockDir, "sibling-session", nil)
	if afterErr != nil {
		t.Fatalf("merge lock not released after FinishMerge returned: %v", afterErr)
	}
	if relErr := after.Release(); relErr != nil {
		t.Errorf("releasing probe lock: %v", relErr)
	}
}

// The hook must be told the sha that actually landed, the branches involved,
// and whether the landing was pushed. Without a remote, gitSync=false, so
// SPINCLASS_MERGE_PUSHED is 0 — the hook can tell "landed locally" from
// "landed and published".
func TestPostMergeReceivesLandedFactsEnv(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")

	envFile := filepath.Join(t.TempDir(), "env")
	writeRepoSweatfile(t, repoDir, fmt.Sprintf(
		"[hooks]\npost-merge = \"env | grep '^SPINCLASS_' | sort > %s\"\n", envFile,
	))

	tests, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if tr, ok := findTest(tests, "post-merge feature"); !ok || !tr.OK {
		t.Fatalf("expected ok post-merge point, got %+v (all: %v)", tr, testDescs(tests))
	}

	raw, readErr := os.ReadFile(envFile)
	if readErr != nil {
		t.Fatalf("post-merge hook did not write its env: %v", readErr)
	}
	got := string(raw)

	// The landed sha must be what main actually points at.
	mainSha := runGit(t, repoDir, "rev-parse", "main")
	for _, want := range []string{
		"SPINCLASS_MERGED_SHA=" + mainSha,
		"SPINCLASS_MERGED_BRANCH=feature",
		"SPINCLASS_DEFAULT_BRANCH=main",
		"SPINCLASS_MERGE_PUSHED=0",
		"SPINCLASS_REPO_PATH=" + repoDir,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in post-merge env:\n%s", want, got)
		}
	}
}

// With a real remote and gitSync, the hook sees SPINCLASS_MERGE_PUSHED=1 —
// the "confirmed pushed to origin" signal the deploy trigger keys on.
func TestPostMergeReportsPushed(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")

	remote := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, repoDir, "init", "--bare", remote)
	runGit(t, repoDir, "remote", "add", "origin", remote)
	runGit(t, repoDir, "push", "-u", "origin", "main")

	envFile := filepath.Join(t.TempDir(), "env")
	writeRepoSweatfile(t, repoDir, fmt.Sprintf(
		"[hooks]\npost-merge = \"env | grep '^SPINCLASS_MERGE_PUSHED' > %s\"\n", envFile,
	))

	if _, err := runFinish(t, repoDir, wtPath, "feature", true); err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}

	raw, readErr := os.ReadFile(envFile)
	if readErr != nil {
		t.Fatalf("post-merge hook did not run: %v", readErr)
	}
	if got := strings.TrimSpace(string(raw)); got != "SPINCLASS_MERGE_PUSHED=1" {
		t.Errorf("got %q, want SPINCLASS_MERGE_PUSHED=1", got)
	}
	// And the hook really did run after the push landed on the remote.
	if local, remoteSha := runGit(t, repoDir, "rev-parse", "main"),
		runGit(t, repoDir, "rev-parse", "origin/main"); local != remoteSha {
		t.Errorf("main %s not pushed to origin/main %s", local, remoteSha)
	}
}

// A failing post-merge hook must NOT fail the merge: the merge already landed,
// so there is nothing to roll back and nothing to retry. The failure is
// surfaced as a not-ok point carrying severity=warn plus the hook's output.
func TestPostMergeFailureIsNonFatal(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, "[hooks]\npost-merge = \"echo deploy-broke >&2; exit 7\"\n")

	tests, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err != nil {
		t.Fatalf("post-merge failure must not fail the merge, got %v", err)
	}

	// The merge itself still landed.
	if got := strings.TrimSpace(runGit(t, repoDir, "show", "main:a.txt")); got != "a" {
		t.Errorf("merge did not land: main:a.txt = %q", got)
	}

	tr, ok := findTest(tests, "post-merge feature")
	if !ok {
		t.Fatalf("no post-merge test point in %v", testDescs(tests))
	}
	if tr.OK {
		t.Errorf("post-merge point should be not-ok on failure: %+v", tr)
	}
	if sev := fmt.Sprintf("%v", tr.Diagnostic["severity"]); sev != "warn" {
		t.Errorf("severity = %q, want warn (a landed merge is not a failure)", sev)
	}
	if out := fmt.Sprintf("%v", tr.Diagnostic["output"]); !strings.Contains(out, "deploy-broke") {
		t.Errorf("hook output not surfaced in diagnostic: %v", tr.Diagnostic)
	}
}

// disable-post-merge suppresses the hook even with a (failing) command set.
func TestPostMergeDisabledEmitsNoPoint(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir,
		"[hooks]\npost-merge = \"exit 1\"\ndisable-post-merge = true\n")

	tests, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if tr, ok := findTest(tests, "post-merge"); ok {
		t.Errorf("disabled post-merge should emit no point, got %+v", tr)
	}
}

// post-merge is strictly post-LANDING: a merge that never lands (here, a
// failing pre-merge gate) must not fire it.
func TestPostMergeNotRunWhenGateFails(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir,
		"[hooks]\npre-merge = \"exit 1\"\npost-merge = \"echo should-not-run\"\n")

	tests, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err == nil {
		t.Fatal("expected the failing pre-merge gate to fail the merge")
	}
	if tr, ok := findTest(tests, "post-merge"); ok {
		t.Errorf("post-merge must not run when the merge never landed, got %+v", tr)
	}
}

// Out-of-session merges tear the worktree down before the hook would run, so
// the hierarchy load and the hook's working directory must both survive a
// wtPath that no longer exists — otherwise post-merge silently never fires on
// the `sc merge <target>` path. Falls back to the main checkout, which is on
// the merged tip.
func TestPostMergeRunsAfterWorktreeTeardown(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")

	pwdFile := filepath.Join(t.TempDir(), "pwd")
	writeRepoSweatfile(t, repoDir, fmt.Sprintf(
		"[hooks]\npost-merge = \"pwd > %s\"\n", pwdFile,
	))

	// inSession=false ⇒ teardownAndPush removes the worktree and branch.
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	pinnedSha, prepErr := PrepareMerge(ts, repoDir, wtPath, "feature", "main", false)
	if prepErr != nil {
		t.Fatalf("PrepareMerge: %v", prepErr)
	}
	_, err := FinishMerge(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature", "main", pinnedSha, false, false, nil)
	ts.Finish()
	if err != nil {
		t.Fatalf("FinishMerge: %v\n%s", err, buf.String())
	}
	tests := testRecords(decodeRecords(t, buf.Bytes()))

	// Precondition: the worktree really is gone by the time the hook runs.
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected worktree removed, stat err = %v", statErr)
	}

	tr, ok := findTest(tests, "post-merge feature")
	if !ok {
		t.Fatalf("post-merge did not run after teardown: %v", testDescs(tests))
	}
	if !tr.OK {
		t.Errorf("post-merge point not ok: %+v", tr)
	}

	raw, readErr := os.ReadFile(pwdFile)
	if readErr != nil {
		t.Fatalf("hook did not run: %v", readErr)
	}
	// EvalSymlinks: the hook's $PWD is the resolved path, while repoDir may
	// carry an unresolved /tmp -> /private/tmp style prefix.
	wantDir, _ := filepath.EvalSymlinks(repoDir)
	if got := strings.TrimSpace(string(raw)); got != wantDir {
		t.Errorf("hook ran in %q, want the main checkout %q", got, wantDir)
	}
}

// The [hooks].disable-merge-queue rollback path (no lock at all) still runs
// post-merge — the hook is a property of a landed merge, not of the queue.
func TestPostMergeRunsOnUnqueuedPath(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir,
		"[hooks]\ndisable-merge-queue = true\npost-merge = \"true\"\n")

	tests, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	tr, ok := findTest(tests, "post-merge feature")
	if !ok {
		t.Fatalf("no post-merge point on the unqueued path: %v", testDescs(tests))
	}
	if !tr.OK {
		t.Errorf("post-merge point not ok: %+v", tr)
	}
	// Sanity: the queue really was disabled (no wait/landing-pull points).
	if _, queued := findTest(tests, "pull main (landing)"); queued {
		t.Error("expected the unqueued path, but saw a landing pull")
	}
}
