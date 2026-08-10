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
// refusing), consumes+binds the attestation, and reports that the queued merge
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

	// The attestation is consumed and bound to the queued entry.
	st, rerr := session.Read(repoPath, branch)
	if rerr != nil {
		t.Fatalf("read session state: %v", rerr)
	}
	if st.PreMergeAttestation != nil {
		t.Error("attestation should be consumed (bound) on enqueue, still present")
	}

	// Exactly one entry queued.
	mergeQueueMu.Lock()
	n := len(mergeQueue[cwd])
	mergeQueueMu.Unlock()
	if n != 1 {
		t.Errorf("queue length = %d, want 1", n)
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

	processMergeQueue(wt, job.KindMerge, job.StatusSucceeded, "prior-merge")

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("dequeued merge did not run within 5s")
	}
	<-job.WaitDone(wt)

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
