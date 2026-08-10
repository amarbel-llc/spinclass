package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/executor"
	"code.linenisgreat.com/spinclass/internal/job"
	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/present"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// The intra-session merge queue (spinclass#265, FDR 0025). A second
// merge-this-session-async issued while a merge gate is already running does
// not refuse — it ENQUEUES the next batch, which runs when the current gate
// completes. The queue is in-process and per-worktree, a sibling of
// job.running: a serve restart loses it (exactly as it already loses the
// in-flight job's liveness wake). Worktree sessions only; the queue holds
// merges (populated only by the worktree merge-async path).
//
// A queued entry records NO pin. When it dequeues, its run closure executes the
// full PrepareMerge + FinishMerge flow FRESH against the session branch as it
// then stands: PrepareMerge's rebase onto the (now-advanced) default lets git's
// patch-id dedup drop the already-landed prior batch, reducing the branch to
// exactly the un-landed commits atop the real landed base. This sidesteps the
// naive "pin at enqueue" corruption when the prior merge is rebased during its
// own landing (see the design doc / FDR 0025).
var (
	mergeQueueMu sync.Mutex
	mergeQueue   = map[string][]queuedMerge{} // worktree abs path -> FIFO of pending merges
)

// queuedMerge is one enqueued batch. run does the deferred PrepareMerge +
// FinishMerge at dequeue; gitSync mirrors the tool's push default. The
// pre-merge attestation was consumed and BOUND at enqueue, so run never
// re-checks the gate — FinishMerge has never called it, and re-checking a live
// buffer that has moved on would be wrong.
type queuedMerge struct {
	gitSync bool
	run     func(ctx context.Context, w io.Writer) (text string, isErr bool)
}

// wireMergeQueue installs the job-completion hook that drives the queue. Called
// once at serve startup (the only place async jobs run). Without it the queue
// is inert — enqueue is never reached because registration of the async tools
// and this wiring share the clown gate.
func wireMergeQueue() {
	job.OnJobDone = processMergeQueue
}

// processMergeQueue is job.OnJobDone: it runs in the completed job's goroutine
// after its terminal record, wake, and running-slot clear. It either dequeues
// the next merge or drains the queue.
//
//   - Drain iff the completed job was a MERGE that did not succeed: a failed or
//     aborted (incl. cancelled) landing broke the base every queued merge
//     assumed. A completed check (any status) or a succeeded merge leaves that
//     base intact, so the next merge dequeues and re-prepares fresh.
//   - The dequeue Start runs UNDER the queue lock so a concurrent handler's
//     busy-check sees a consistent state (entry popped AND the new job running),
//     never a false idle gap.
func processMergeQueue(wt, kind, status, id string) {
	mergeQueueMu.Lock()
	q := mergeQueue[wt]
	if len(q) == 0 {
		mergeQueueMu.Unlock()
		return
	}

	if kind == job.KindMerge && status != job.StatusSucceeded {
		drainedCount := len(q)
		mergeQueue[wt] = nil
		mergeQueueMu.Unlock()
		emitDrainWake(drainedCount, id, status)
		return
	}

	next := q[0]
	mergeQueue[wt] = q[1:]
	nextID := fmt.Sprintf("%s-%d", job.KindMerge, time.Now().UnixNano())
	_, startErr := job.Start(wt, job.KindMerge, next.gitSync, nextID, next.run)
	mergeQueueMu.Unlock()

	if startErr != nil {
		// The dequeued merge could not be dispatched (clown allocation failed,
		// or a racing Start already claimed the slot). Its OnJobDone will never
		// fire, so the chain is broken: report it and drain whatever is left,
		// counting the entry that failed to start alongside the remainder.
		servelog.Errorf("merge queue: dequeue Start failed for %s: %v", wt, startErr)
		mergeQueueMu.Lock()
		drainedCount := len(mergeQueue[wt])
		mergeQueue[wt] = nil
		mergeQueueMu.Unlock()
		emitDrainWake(drainedCount+1, nextID, "failed to dispatch")
	}
}

// emitDrainWake fires one clown wake telling the agent that count queued
// merge(s) did not run because the prior merge failed, naming that merge so the
// "re-attest + re-merge" has a concrete cause to inspect. The bound
// attestations are discarded (consumed at enqueue, never re-satisfied) — the
// agent re-attests against the new reality. Without clown there is no wake
// channel, so this is a no-op (a queued merge has no ringmaster id to inspect
// either); the branch it drains is still cleared.
func emitDrainWake(count int, priorJobID, priorStatus string) {
	if !clown.Enabled() {
		return
	}
	ctx := context.Background()
	newID, err := clown.StartJob(ctx, job.KindMerge, clown.Source)
	if err != nil {
		servelog.Errorf("merge queue: drain wake StartJob failed: %v", err)
		return
	}
	noun := "merge"
	if count != 1 {
		noun = "merges"
	}
	msg := fmt.Sprintf(
		"%d queued %s did not run: prior merge %s %s — its base assumption broke. Resolve the failure, re-attest with nothing-but-the-truth, and re-merge the remaining commits.",
		count, noun, priorJobID, priorStatus,
	)
	if cerr := clown.FinishJob(ctx, newID, job.StatusAborted, msg, ""); cerr != nil {
		servelog.Errorf("merge queue: drain wake FinishJob failed: %v", cerr)
	}
}

// enqueuedMergeResult renders the tool result for a merge that was queued
// behind the currently-running gate. It states explicitly that a queued merge
// has NO ringmaster job id (so an agent cannot job_wait on it) and that the
// completion wake is the only signal — the FDR 0025 ratified response contract,
// discoverable from the response itself, not just the FDR.
func enqueuedMergeResult(position int) *command.Result {
	return command.TextResult(fmt.Sprintf(
		"enqueued this merge at queue position %d behind the running gate (spinclass#265 stacked merges). "+
			"It runs automatically when the current merge completes, re-preparing against the branch as it then "+
			"stands. NOTE: a queued merge has NO ringmaster job id yet — you cannot job_wait on it; the completion "+
			"wake is the only signal, so end your turn and let it arrive. If the running merge FAILS, this queued "+
			"merge is aborted (its base assumption broke) and you are woken to resolve, re-attest, and re-merge. "+
			"The pre-merge attestation was consumed and bound to this queued merge now.",
		position,
	))
}

// buildQueuedMergeRun builds the deferred run closure for an enqueued worktree
// merge: the full PrepareMerge + FinishMerge, run at dequeue against the session
// branch's then-current state. It mirrors the immediate async merge path but
// fuses the prefix (which the immediate path runs synchronously before
// backgrounding) into the job, since the base it rebases onto does not exist
// until the prior batch lands. inSession is passed true, matching the immediate
// MCP path (worktree removal skipped).
func buildQueuedMergeRun(repoPath, wtPath, branch, defaultBranch string, gitSync bool) func(context.Context, io.Writer) (string, bool) {
	return func(ctx context.Context, w io.Writer) (string, bool) {
		var buf bytes.Buffer
		rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "merge " + branch, Source: "spinclass"})
		ts := rep.TestStream(0)
		pinnedSha, prepErr := merge.PrepareMerge(ts, repoPath, wtPath, branch, defaultBranch, gitSync)
		if prepErr != nil {
			ts.Finish()
			text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
			if text == "" {
				text = prepErr.Error()
			}
			return text, true
		}
		_, mergeErr := merge.FinishMerge(
			ctx, executor.ShellExecutor{}, rep, ts,
			repoPath, wtPath, branch, defaultBranch, pinnedSha, gitSync, true, w,
		)
		ts.Finish()
		text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
		if mergeErr != nil && text == "" {
			text = mergeErr.Error()
		}
		text = appendNotPushedNote(text, gitSync, mergeErr)
		return text, mergeErr != nil
	}
}
