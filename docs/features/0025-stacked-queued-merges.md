---
status: experimental
date: 2026-08-10
promotion-criteria: |
  experimental -> testing: one real intra-session stack on a busy repo where
  (1) a second merge-this-session-async issued while a gate runs returns
  "enqueued at queue position N" and consumes the attestation, (2) the head
  merge landing dequeues the next entry, which re-prepares fresh and lands the
  un-landed commits (the already-landed batch dropped by patch-id dedup), and
  (3) a head merge FAILING drains the queue with an aborted wake naming the
  failed prior merge. Plus one observation that
  `disable-merge-stacking = true` restores the immediate ErrAlreadyRunning
  refusal (still not consuming the attestation).
  testing -> accepted: ~1 week of real stacked landings with no double-run of a
  merge, no stranded queue after a serve restart beyond the known in-process
  limitation, and no attestation lost on a refusal.
---

# Stacked / queued intra-session merges

## Problem Statement

`merge-this-session-async` allowed exactly one background merge per session
(`job.running[wt]` is a single slot). With ~30-minute pre-merge gates, a session
producing successive commit batches serialized hard: the second async attempt
hit `job.ErrAlreadyRunning`. Two costs (observed live 2026-08-10, circus repo):

1. The second batch could not make progress until the first gate finished, the
   agent noticed the wake, AND re-issued the merge — a wake + re-issue
   round-trip per batch.
2. **The refused attempt had already consumed the pre-merge attestation
   buffer**: `resolveGatedSession` → `attestation.Check` cleared it before the
   handler reached the `job.Start` refusal, forcing a full re-attestation of
   unchanged content on the retry.

This record covers both the safety fix (2, "deliverable 2") and the queue
(1, "deliverable 1"). It extends FDR 0022 (per-repo landing queue), FDR 0007
(pre-merge attestation), and FDR 0010 (async job-wakeup).

## Interface

- **A second `merge-this-session-async` while a merge gate runs ENQUEUES** the
  next batch instead of refusing. The tool returns "enqueued at queue position
  N", stating explicitly that a queued merge has **no ringmaster job id** (an
  agent cannot `job_wait` on it) and that the completion wake is the only
  signal.
- **A queued merge runs when the current gate completes**, re-preparing against
  the branch as it then stands (see Design). Its result arrives as its own
  completion wake with a freshly-allocated ringmaster job id.
- **If the running merge FAILS**, the queue is drained: one aborted wake naming
  the failed prior merge tells the agent to resolve, re-attest, and re-merge.
- **A refusal never consumes the attestation.** The paths that still refuse —
  `disable-merge-stacking = true` while busy, an implicit (main-checkout)
  session while busy, and check-this-session-async while busy — return the
  "already running" refusal with the attestation buffer intact.
- **Rollback knob:** `[hooks].disable-merge-stacking = true` restores the
  pre-#265 immediate `ErrAlreadyRunning` refusal (with the deliverable-2 fix
  still in force: the refusal does not consume).

Scope: **worktree sessions only.** Implicit sessions (push-based `MergeImplicit`,
no landing lock) keep single-job behavior; `sc check` and check-async never
stack.

## Design

### The consume ordering invariant (deliverable 2)

> Consume the attestation only once the merge is committed to being dispatched
> or enqueued — never before a refusal.

The async merge handler resolves identity without consuming, PEEKs the
attestation (refusing without consuming if absent), decides dispatch/enqueue/
refuse, and only then CONSUMEs. The attestation package split `Check` into
`Peek` (non-destructive) + `Consume` for this.

### Re-prepare at dequeue, not pin at enqueue (deliverable 1)

A queued entry records **no pin**. The naive "pin HEAD at enqueue, land it
after the current merge" is wrong when the in-flight merge is *rebased during
its own landing* (another session interleaved on the default branch): the
queued batch's recorded base — the prior batch's original commits — would then
not be an ancestor of the moved default, and FDR 0022's `rebaseLanding` (which
replays every commit not on default) would replay the prior batch a second time.

Instead, when an entry dequeues, its run closure executes the **full, unchanged**
`PrepareMerge` + `FinishMerge` flow against the session branch as it then stands.
`PrepareMerge`'s rebase onto the now-advanced default lets git's **patch-id
dedup** drop the already-landed prior batch, reducing the branch to exactly the
un-landed commits atop the real landed base. The pin is computed fresh and
correctly, reusing both functions verbatim. If the prior batch landed with a
*conflict-resolved* rebase (its patches differ), dedup may not drop cleanly and
the queued batch's rebase surfaces as an integration conflict at dequeue — a
loud, actionable failure, not silent corruption.

One behavioral difference from the head-of-queue merge: a queued batch's
`PrepareMerge` runs *inside the background job* (the base it rebases onto does
not exist until the prior batch lands), so its rebase-conflict / nothing-to-merge
outcome surfaces via the completion wake rather than synchronously at the tool
call.

### Attestation semantics

The attestation covers "the diff being merged" = the queued commits atop the
prior batch assumed merged. It is consumed at enqueue and **bound** to the queue
entry; when the entry runs it does not re-check the live buffer (the gate is a
handler-level concern — `FinishMerge` has never called it). If the entry is
drained or cancelled, its bound attestation is **discarded** — the base
assumption broke, so the agent re-attests against the new reality. Nothing is
stored: "bound" means the enqueue consumed the buffer and the entry carries the
right to merge without re-check; "discarded" means that right evaporates.

### Chaining, drain, and cancel

The queue is in-process, per-worktree (`cmd/spinclass/merge_queue.go`), a
sibling of `job.running`. `job.OnJobDone` (a package-level completion hook
installed at serve start) fires `processMergeQueue` after every job's terminal
+ wake + running-slot clear:

- Completed job was a **merge that did not succeed** → **drain**: clear the
  queue, emit one aborted wake naming the failed prior merge.
- Otherwise (a succeeded merge, or any completed check — a check lands nothing,
  so the queued merges' base is intact) → **dequeue** the head and start it as a
  new job, UNDER the queue lock so a concurrent handler's busy-check never
  observes a false idle gap.

`session-job-cancel` cancels the running head; an aborted merge head is a
non-success terminal, so it drains the queue. v1 adds no per-queued-entry cancel
surface.

### Concurrency

The decision (`job.IsRunning(cwd) || len(queue) > 0`), the attestation consume,
and the enqueue append happen under one `mergeQueueMu` critical section, so a
second merge-async cannot peek the same still-present attestation and enqueue it
twice. The dequeue's `job.Start` also runs under the lock. The immediate path's
`job.Start` is backstopped by `job.Start`'s own mutex (the worst a race can do is
one spurious "already running" refusal, never a double-dispatch); MCP stdio
processes requests sequentially, so the immediate-vs-immediate race does not
arise in practice.

## Tuning Levers

- **Queue persistence: in-process only (v1).** A serve restart loses the queue —
  the same semantics the single-job path already has for the in-flight job's
  liveness wake. Change signal: restarts losing real queued work often enough to
  justify persisting to `job.json`.
- **Queue depth: unbounded (v1).** Each entry is cheap (params + a bound
  attestation). Change signal: a runaway agent loop enqueueing pathologically
  deep — then add a cap that refuses (without consuming) beyond N.
- **Failure cascade: any non-success merge drains.** Simplest correct semantics.
  Change signal: transient push failures common enough that a "retry the head,
  keep the queue" policy earns its complexity.

## Rollback

- Deliverable 2 (consume ordering) is a pure reorder; rollback is a revert.
- Deliverable 1: `[hooks].disable-merge-stacking = true` restores today's
  `ErrAlreadyRunning` refusal (the deliverable-2 fix still holds — the refusal
  does not consume). Single-config rollback, no revert.

## Alternatives Considered

- **Pin HEAD at enqueue** — rejected: corrupts when the prior merge is rebased
  during its own landing (see Design).
- **Defer PrepareMerge for the head merge too** (unify immediate + queued) —
  rejected: regresses the common single-merge UX, where rebase conflicts
  currently surface synchronously at the tool call.
- **Persist the queue in `job.json`** — deferred to a possible v2; in-process
  matches the existing model and is enough for the observed friction.
