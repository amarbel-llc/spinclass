package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/job"
	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// gateSweat activates the pre-merge attestation gate (one required skill) with
// stacking ENABLED (no disable-merge-stacking). gateSweatNoStacking is the same
// gate with stacking disabled — the path where a busy merge-async refuses.
const (
	gateSweat            = "[[pre-merge-skills]]\nname = \"eng:code-reviewer\"\nrationale = \"Mandatory.\"\n"
	gateSweatNoStacking  = "[hooks]\ndisable-merge-stacking = true\n\n" + gateSweat
	fixtureAttestedSkill = "eng:code-reviewer"
)

// gatedWorktreeFixture builds a worktree session with the attestation gate
// active (sweatBody at the repo root, HOME bound to its parent) and a fresh
// attestation buffered in session state, then chdirs into the worktree. Returns
// the canonical cwd plus the (repoPath, branch) the gate keys on. Clown is
// forced off so job.Start neither allocates a ringmaster job nor shells out.
// EvalSymlinks keeps the constructed paths aligned with git's realpath output.
func gatedWorktreeFixture(t *testing.T, sweatBody string) (cwd, repoPath, branch string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks tempdir: %v", err)
	}
	t.Setenv("HOME", base) // bound the sweatfile cascade at the repo's parent

	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feature")
	testgit.MustWorktreeAdd(t, repo, wt, "feature")
	if err := os.WriteFile(filepath.Join(repo, "sweatfile"), []byte(sweatBody), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	t.Chdir(wt)
	cwd, err = os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoPath, err = git.CommonDir(cwd)
	if err != nil {
		t.Fatalf("common dir: %v", err)
	}
	branch, err = git.BranchCurrent(cwd)
	if err != nil {
		t.Fatalf("branch: %v", err)
	}

	st := session.State{
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: filepath.Join(repoPath, ".worktrees", branch),
		Branch:       branch,
		SessionKey:   filepath.Base(repoPath) + "/" + branch,
		StartedAt:    time.Now().UTC(),
		PreMergeAttestation: &session.PreMergeAttestation{
			RecordedAt: time.Now().UTC(),
			Skills:     []session.AttestedSkill{{Name: fixtureAttestedSkill, Used: true, Reasoning: "reviewed"}},
		},
	}
	if err := session.Write(st); err != nil {
		t.Fatalf("write session state: %v", err)
	}
	return cwd, repoPath, branch
}

// startBlockingJob occupies wt's single background-job slot with a job that
// blocks until the returned channel is closed, so job.IsRunning(wt) is true
// across a handler call. The caller closes release and drains WaitDone in
// cleanup.
func startBlockingJob(t *testing.T, wt string) (release chan struct{}) {
	t.Helper()
	release = make(chan struct{})
	if _, err := job.Start(wt, job.KindMerge, false, "test-blocker", func(_ context.Context, _ io.Writer) (string, bool) {
		<-release
		return "", false
	}); err != nil {
		t.Fatalf("start blocking job: %v", err)
	}
	return release
}

// TestMergeAsyncEnqueuesWhenBusy pins spinclass#265 deliverable 1: a worktree
// merge-async issued while a job is already running ENQUEUES (rather than
// refusing), claims the attestation, and reports that the queued merge
// has no ringmaster job id.
func TestMergeAsyncEnqueuesWhenBusy(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, gateSweat)
	release := startBlockingJob(t, cwd)
	t.Cleanup(func() {
		// Clear the queued entry so it never runs against the fixture worktree,
		// then release and drain the blocking job.
		mergeQueueMu.Lock()
		delete(mergeQueue, cwd)
		mergeQueueMu.Unlock()
		close(release)
		<-job.WaitDone(cwd)
	})

	res, err := handleMergeThisSessionAsync(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("handler returned a transport error: %v", err)
	}
	if res.IsErr {
		t.Fatalf("expected enqueue success, got error result: %s", res.Text)
	}
	if !strings.Contains(res.Text, "enqueued") || !strings.Contains(res.Text, "ringmaster job id") {
		t.Errorf("enqueue result missing expected wording (enqueued / no ringmaster job id): %s", res.Text)
	}

	if !strings.Contains(res.Text, "claimed") || !strings.Contains(res.Text, "consumed only if the merge lands") {
		t.Errorf("enqueue result should describe the claim: %s", res.Text)
	}

	// The attestation is claimed (not consumed) by the queued entry.
	a := readAttestation(t, repoPath, branch)
	if a == nil {
		t.Fatal("attestation should stay buffered (claimed) on enqueue")
	}
	if a.Claim == nil || a.Claim.PID != os.Getpid() {
		t.Errorf("attestation should carry this process's claim, got %+v", a.Claim)
	}

	// A further batch needs its own attestation: the claim is live.
	res2, err := handleMergeThisSessionAsync(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("second handler transport error: %v", err)
	}
	if !res2.IsErr || !strings.Contains(res2.Text, "in-flight") {
		t.Errorf("second enqueue should be refused as in-flight, got isErr=%v: %s", res2.IsErr, res2.Text)
	}

	// Exactly one entry queued.
	mergeQueueMu.Lock()
	n := len(mergeQueue[cwd])
	mergeQueueMu.Unlock()
	if n != 1 {
		t.Errorf("queue length = %d, want 1", n)
	}
}

// TestMergeAsyncHookFailureKeepsAttestation: a background merge whose hook goes
// red releases its claim and leaves the attestation buffered.
func TestMergeAsyncHookFailureKeepsAttestation(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"false\"\n\n"+gateSweat)
	commitFile(t, cwd, "x.txt")

	res, err := handleMergeThisSessionAsync(context.Background(), json.RawMessage(`{"local_only":true}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsErr {
		t.Fatalf("expected the job to start, got error: %s", res.Text)
	}
	<-job.WaitDone(cwd)

	a := readAttestation(t, repoPath, branch)
	if a == nil {
		t.Fatal("attestation consumed by a background merge whose hook failed")
	}
	if a.Claim != nil {
		t.Errorf("claim not released: %+v", a.Claim)
	}
}

// TestCheckThisSessionConsumesOnlyOnGreen: a red check keeps the attestation,
// a green one consumes it.
func TestCheckThisSessionConsumesOnlyOnGreen(t *testing.T) {
	_, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"false\"\n\n"+gateSweat)

	res, err := handleCheckThisSession(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected the red check to fail, got: %s", res.Text)
	}
	a := readAttestation(t, repoPath, branch)
	if a == nil || a.Claim != nil {
		t.Fatalf("red check should leave an unclaimed attestation, got %+v", a)
	}

	writeSweatfile(t, repoPath, "[hooks]\npre-merge = \"true\"\n\n"+gateSweat)
	res, err = handleCheckThisSession(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsErr {
		t.Fatalf("green check errored: %s", res.Text)
	}
	if got := readAttestation(t, repoPath, branch); got != nil {
		t.Errorf("green check should consume the attestation, got %+v", got)
	}
}

// TestMergeAsyncLandedConsumes: a background merge that lands consumes the
// attestation.
func TestMergeAsyncLandedConsumes(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"true\"\n\n"+gateSweat)
	commitFile(t, cwd, "x.txt")

	res, err := handleMergeThisSessionAsync(context.Background(), json.RawMessage(`{"local_only":true}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsErr {
		t.Fatalf("expected the job to start, got error: %s", res.Text)
	}
	<-job.WaitDone(cwd)

	if got := readAttestation(t, repoPath, branch); got != nil {
		t.Errorf("landed merge should consume the attestation, got %+v", got)
	}
}

// TestQueuedRunSettlesByOutcome: the queued run closure consumes on a landing
// and releases on a failure.
func TestQueuedRunSettlesByOutcome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hook     string
		wantKept bool
	}{
		{"failing hook keeps", "false", true},
		{"passing hook consumes", "true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \""+tc.hook+"\"\n\n"+gateSweat)
			commitFile(t, cwd, "x.txt")
			gs, failMsg, ok, gitErr := resolveSession(cwd)
			if !ok || gitErr != nil {
				t.Fatalf("resolveSession: ok=%v msg=%q err=%v", ok, failMsg, gitErr)
			}
			hold, msg, hok := holdGate(gs, cwd)
			if !hok {
				t.Fatalf("holdGate refused: %s", msg)
			}
			defaultBranch, err := merge.ResolveDefaultBranch(repoPath)
			if err != nil {
				t.Fatal(err)
			}
			run := buildQueuedMergeRun(repoPath, cwd, branch, defaultBranch, false, merge.PostMergeOptions{Gate: merge.GateAttested}, hold)
			_, isErr := run(context.Background(), io.Discard)
			if isErr != tc.wantKept {
				t.Fatalf("run isErr = %v, want %v", isErr, tc.wantKept)
			}
			a := readAttestation(t, repoPath, branch)
			if tc.wantKept && (a == nil || a.Claim != nil) {
				t.Errorf("failed run should leave an unclaimed attestation, got %+v", a)
			}
			if !tc.wantKept && a != nil {
				t.Errorf("landed run should consume the attestation, got %+v", a)
			}
		})
	}
}

// TestCheckAsyncConsumesOnlyOnGreen mirrors the sync check test for the async
// twin.
func TestCheckAsyncConsumesOnlyOnGreen(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"false\"\n\n"+gateSweat)

	run := func() {
		t.Helper()
		res, err := handleCheckThisSessionAsync(context.Background(), json.RawMessage(`{}`), nil)
		if err != nil || res.IsErr {
			t.Fatalf("expected the check job to start: err=%v res=%+v", err, res)
		}
		<-job.WaitDone(cwd)
	}
	run()
	if a := readAttestation(t, repoPath, branch); a == nil || a.Claim != nil {
		t.Fatalf("red check should leave an unclaimed attestation, got %+v", a)
	}

	writeSweatfile(t, repoPath, "[hooks]\npre-merge = \"true\"\n\n"+gateSweat)
	run()
	if got := readAttestation(t, repoPath, branch); got != nil {
		t.Errorf("green check should consume the attestation, got %+v", got)
	}
}

// TestProcessMergeQueueDrainReleasesHolds: draining a failed merge's queue
// releases every drained entry's hold.
func TestProcessMergeQueueDrainReleasesHolds(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	wt := t.TempDir()
	released := 0
	mk := func() queuedMerge {
		return queuedMerge{
			run:     func(_ context.Context, _ io.Writer) (string, bool) { return "", false },
			release: func() { released++ },
		}
	}
	mergeQueueMu.Lock()
	mergeQueue[wt] = []queuedMerge{mk(), mk()}
	mergeQueueMu.Unlock()
	t.Cleanup(func() {
		mergeQueueMu.Lock()
		delete(mergeQueue, wt)
		mergeQueueMu.Unlock()
	})

	processMergeQueue(wt, job.KindMerge, job.StatusFailed, "p")

	if released != 2 {
		t.Errorf("released = %d, want 2", released)
	}
}

// TestStartSessionJobCallsOnNotStartedWhenBusy: a refused start releases the
// caller's hold via onNotStarted, and starts nothing.
func TestStartSessionJobCallsOnNotStartedWhenBusy(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	wt := t.TempDir()
	release := startBlockingJob(t, wt)
	t.Cleanup(func() {
		close(release)
		<-job.WaitDone(wt)
	})

	fnRan, notStarted := 0, 0
	res := startSessionJob(wt, job.KindMerge, false,
		func(_ context.Context, _ io.Writer) (string, bool) { fnRan++; return "", false },
		func() { notStarted++ })

	if !res.IsErr || !strings.Contains(res.Text, "already running") {
		t.Errorf("expected the already-running error, got isErr=%v: %s", res.IsErr, res.Text)
	}
	if notStarted != 1 {
		t.Errorf("onNotStarted ran %d times, want 1", notStarted)
	}
	if fnRan != 0 {
		t.Errorf("fn ran %d times, want 0", fnRan)
	}
}

// TestProcessMergeQueueDequeuesOnSuccess: when a merge completes successfully,
// the queue's head is dequeued and started.
func TestProcessMergeQueueDequeuesOnSuccess(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	wt := t.TempDir()
	ran := make(chan struct{}, 1)
	mergeQueueMu.Lock()
	released := 0
	mergeQueue[wt] = []queuedMerge{{
		run: func(_ context.Context, _ io.Writer) (string, bool) {
			ran <- struct{}{}
			return "✓ queued merge", false
		},
		release: func() { released++ },
	}}
	mergeQueueMu.Unlock()
	t.Cleanup(func() {
		mergeQueueMu.Lock()
		delete(mergeQueue, wt)
		mergeQueueMu.Unlock()
	})

	processMergeQueue(wt, job.KindMerge, job.StatusSucceeded, "prior-merge")

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("dequeued merge did not run within 5s")
	}
	<-job.WaitDone(wt)
	if released != 0 {
		t.Errorf("a dequeued entry's hold is settled by its run closure; release called %d times", released)
	}

	mergeQueueMu.Lock()
	n := len(mergeQueue[wt])
	mergeQueueMu.Unlock()
	if n != 0 {
		t.Errorf("queue length = %d, want 0 after dequeue", n)
	}
}

// TestProcessMergeQueueDrainsOnFailure: when a merge fails, every queued entry
// is drained (removed) and NONE run — their base assumption broke.
func TestProcessMergeQueueDrainsOnFailure(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	wt := t.TempDir()
	var mu sync.Mutex
	ranCount := 0
	mkEntry := func() queuedMerge {
		return queuedMerge{run: func(_ context.Context, _ io.Writer) (string, bool) {
			mu.Lock()
			ranCount++
			mu.Unlock()
			return "", false
		}}
	}
	mergeQueueMu.Lock()
	mergeQueue[wt] = []queuedMerge{mkEntry(), mkEntry()}
	mergeQueueMu.Unlock()
	t.Cleanup(func() {
		mergeQueueMu.Lock()
		delete(mergeQueue, wt)
		mergeQueueMu.Unlock()
	})

	processMergeQueue(wt, job.KindMerge, job.StatusFailed, "prior-merge")

	mergeQueueMu.Lock()
	n := len(mergeQueue[wt])
	mergeQueueMu.Unlock()
	if n != 0 {
		t.Errorf("queue length = %d, want 0 after drain", n)
	}
	// No entry should have been started.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	rc := ranCount
	mu.Unlock()
	if rc != 0 {
		t.Errorf("drained entries ran %d times, want 0", rc)
	}
}

// readAttestation returns the buffered attestation for (repoPath, branch).
func readAttestation(t *testing.T, repoPath, branch string) *session.PreMergeAttestation {
	t.Helper()
	st, err := session.Read(repoPath, branch)
	if err != nil {
		t.Fatalf("read session state: %v", err)
	}
	return st.PreMergeAttestation
}

// commitFile writes and commits name in dir.
func commitFile(t *testing.T, dir, name string) {
	t.Helper()
	testgit.MustWriteFile(t, dir, name, name+"\n")
	testgit.MustGit(t, dir, "add", name)
	testgit.MustGit(t, dir, "commit", "-q", "-m", "add "+name)
}

func writeSweatfile(t *testing.T, repoPath, body string) {
	t.Helper()
	testgit.MustWriteFile(t, filepath.Dir(repoPath), "repo/sweatfile", body)
}

// TestMergeSyncHookFailureKeepsAttestation is the #219 repro: a red pre-merge
// hook must leave the attestation buffered (unclaimed) for the retry, and the
// retry that lands consumes it.
func TestMergeSyncHookFailureKeepsAttestation(t *testing.T) {
	cwd, repoPath, branch := gatedWorktreeFixture(t, "[hooks]\npre-merge = \"false\"\n\n"+gateSweat)
	commitFile(t, cwd, "x.txt")
	before := readAttestation(t, repoPath, branch)

	res, err := handleMergeThisSession(context.Background(), json.RawMessage(`{"local_only":true}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected the red hook to fail the merge, got: %s", res.Text)
	}
	after := readAttestation(t, repoPath, branch)
	if after == nil {
		t.Fatal("attestation was consumed by a merge that landed nothing (#219)")
	}
	if after.Claim != nil {
		t.Errorf("claim not released after failure: %+v", after.Claim)
	}
	if !after.RecordedAt.Equal(before.RecordedAt) {
		t.Error("RecordedAt changed across a failed attempt")
	}

	writeSweatfile(t, repoPath, "[hooks]\npre-merge = \"true\"\n\n"+gateSweat)
	res, err = handleMergeThisSession(context.Background(), json.RawMessage(`{"local_only":true}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsErr {
		t.Fatalf("retry should land, got error: %s", res.Text)
	}
	if got := readAttestation(t, repoPath, branch); got != nil {
		t.Errorf("attestation should be consumed once the merge landed, got %+v", got)
	}
}

// TestMergeAsyncPrepareFailureKeepsAttestation is the #303 repro: a
// PrepareMerge refusal (here nothing-to-merge) must not burn the attestation.
func TestMergeAsyncPrepareFailureKeepsAttestation(t *testing.T) {
	_, repoPath, branch := gatedWorktreeFixture(t, gateSweat)

	res, err := handleMergeThisSessionAsync(context.Background(), json.RawMessage(`{"local_only":true}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected nothing-to-merge to fail synchronously, got: %s", res.Text)
	}
	a := readAttestation(t, repoPath, branch)
	if a == nil {
		t.Fatal("attestation burned by a prepare failure (#303)")
	}
	if a.Claim != nil {
		t.Errorf("claim not released: %+v", a.Claim)
	}
}

// TestProcessMergeQueueDequeuesAfterCheck: a completed check — even a failed
// one — must NOT drain queued merges (a check lands nothing, so the merges'
// base is intact); it dequeues the next merge.
func TestProcessMergeQueueDequeuesAfterCheck(t *testing.T) {
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	wt := t.TempDir()
	ran := make(chan struct{}, 1)
	mergeQueueMu.Lock()
	mergeQueue[wt] = []queuedMerge{{run: func(_ context.Context, _ io.Writer) (string, bool) {
		ran <- struct{}{}
		return "✓ queued merge", false
	}}}
	mergeQueueMu.Unlock()
	t.Cleanup(func() {
		mergeQueueMu.Lock()
		delete(mergeQueue, wt)
		mergeQueueMu.Unlock()
	})

	processMergeQueue(wt, job.KindCheck, job.StatusFailed, "prior-check")

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("queued merge did not dequeue after a failed check within 5s")
	}
	<-job.WaitDone(wt)
}
