# Stacked / queued intra-session merges (spinclass#265)

Status: APPROVED 2026-08-10 (circus/clear-walnut, operator-delegated) — all
five decisions ratified as recommended; two doc requirements folded in below
(D-response wording, cancel semantics)
Date: 2026-08-10
Extends: FDR 0022 (per-repo merge queue), FDR 0007 (pre-merge attestation),
FDR 0010 (async job-wakeup)

## Problem

`merge-this-session-async` allows exactly one background merge per session
(`job.running[wt]` is a single slot). With ~30-minute pre-merge gates, a session
that produces successive commit batches serializes hard: the second async
attempt hits `job.ErrAlreadyRunning`. Two distinct costs:

1. The second batch cannot make progress until the first gate finishes AND the
   agent notices the wake AND re-issues the merge — a wake + re-issue round-trip
   per batch.
2. **The refused attempt has already consumed the attestation buffer.**
   `resolveGatedSession` → `enforceAttestation` → `attestation.Check` clears the
   buffered attestation *before* the handler reaches the `job.Start`
   `ErrAlreadyRunning` refusal. So the retry needs a full re-attestation of
   unchanged content.

## Deliverable 2 (independent, safe): refusal must not consume attestation

The attestation is a scarce, agent-produced token. Consuming it on a path that
then refuses to do any work is a pure loss. The fix is an ordering invariant:

> **Consume the attestation only once the merge is committed to being
> dispatched or enqueued — never before a refusal.**

Concretely, in the async merge handler, the decision (dispatch-now / enqueue /
refuse) is made *before* the consume. `attestation.Check`'s consume moves to the
commit point. This is a small, self-contained reorder and lands first, as its
own commit, independent of Deliverable 1.

Note Deliverable 1 turns the specific "job already running" case from a refusal
into an enqueue, but the invariant is what protects every *other* refusal path
(bad args, not-a-session, a future queue cap) from the same silent loss.

## Deliverable 1 (the meat): enqueue the next batch

### The core mechanism — re-prepare at dequeue, not pin at enqueue

The naive design ("pin HEAD at enqueue, land it after the current merge") is
**wrong** when the in-flight merge is rebased during its own landing (another
session interleaved on the default branch). The queued batch's recorded base
(the prior batch's *original* commits) would then not be an ancestor of the
moved default, and the existing `rebaseLanding` — which replays *every* commit
not on default — would replay the prior batch a second time.

Instead, a queued entry records **no pin**. When it dequeues (after the current
merge lands), it runs the **full, unchanged** `PrepareMerge` + `FinishMerge`
flow against the session branch as it stands *now*:

- `PrepareMerge` pulls the (now-advanced) default and `git rebase`s the session
  branch onto it. Git's patch-id dedup drops the already-landed prior batch's
  commits, so the rebase reduces the branch to exactly the un-landed batch atop
  the real landed base. The pin is computed fresh and correctly.
- `FinishMerge` gates and lands that batch through the existing per-repo queue.

This reuses both functions verbatim — no new "rebase --onto" landing path. The
well-defined failure mode: if the prior batch landed with *conflict-resolved*
rebase (its patches differ), dedup may not drop cleanly and the queued batch's
rebase surfaces as an integration conflict at dequeue — a loud, actionable
failure, not silent corruption.

The one behavioral difference from a head-of-queue merge: a queued batch's
`PrepareMerge` runs *inside the background job* (the base it rebases onto does
not exist until the prior batch lands), so its rebase-conflict / nothing-to-merge
outcome surfaces via the completion wake rather than synchronously at the tool
call. This is unavoidable and acceptable.

### Attestation semantics (the design meat, per the issue)

The attestation covers "the diff being merged" = the queued commits atop the
prior batch assumed merged. At enqueue of batch N, the agent attests to batch N's
content assuming batches 1..N-1 land. So:

- The attestation is **consumed at enqueue** and **bound to the queue entry**
  (not left in the live buffer).
- When the entry dequeues and runs, it does **not** re-check the live buffer —
  its attestation was satisfied at enqueue. (The gate is a handler-level concern;
  `FinishMerge` itself has never called it.)
- If the entry is **aborted** because the prior merge failed (below), its bound
  attestation is **discarded** — the base assumption broke, so the agent must
  re-attest against the new reality.

### Dispatch decision (async merge handler)

1. Resolve session identity (no consume).
2. Validate a fresh attestation exists (peek; refuse cleanly without consuming
   if absent).
3. Branch on `job.IsRunning(wt)`:
   - **Idle** → consume attestation; run `PrepareMerge` synchronously; `job.Start`
     the `FinishMerge` job. (Today's behavior, unchanged.)
   - **Busy** → consume attestation; enqueue a deferred entry carrying
     `{defaultBranch, gitSync, boundAttestation}` plus a prepare-closure. Return
     "enqueued at position N behind <current job>."

Peek+consume is made atomic per worktree under the queue mutex to avoid a TOCTOU
between two concurrent tool calls in the same serve.

### Chaining and the failure cascade

The queue lives in-process, per worktree, alongside `job.running` (v1 — see
Decision A). On a job's terminal:

- **Succeeded** → pop the head of the queue; run its prepare-closure
  (`PrepareMerge`) then `job.Start` its `FinishMerge` as a *new* ringmaster job
  (its own id, its own wake, allocated at dequeue).
- **Any non-success** (hook fail, integration conflict, push fail, aborted) →
  **drain** the queue: each queued entry terminalizes with an `aborted` wake
  that **names the failed prior merge** — "prior merge `<failed-job-id>` failed
  (`<first-failure-line>`); this queued merge did not run; its base assumption
  broke — resolve the failure, re-attest, and re-merge." The `<failed-job-id>`
  is the head job's ringmaster id, known at drain time, so the queued agent's
  "re-attest + re-merge" has a concrete cause to inspect. Bound attestations are
  discarded.

Any non-success drains (no transient/permanent distinction in v1): the session
branch keeps all un-landed commits, so the agent fixes the prior failure,
re-attests to the whole remaining stack, and re-merges.

### Cancellation semantics (ratified)

v1 adds **no per-queued-entry cancel** surface. `session-job-cancel` cancels the
**running head** (`job.Cancel(wt)` fires the in-flight job's context), which
terminalizes it `aborted` — and an aborted head is a non-success terminal, so it
**drains the queue** exactly like any other failure. Every drained entry's bound
attestation is **discarded** (same rule as the failure cascade): the attestation
covered a merge that never ran, and re-attesting is cheap, so silently restoring
it to the live buffer would invite staleness. Thus "cancel a queued-not-running
entry" reduces in v1 to "cancel the head, which drains everything." If a future
version adds per-entry cancel, the same discard rule applies.

### Teardown interaction (already handled)

The existing pin contract + `tipMatchesPin` logic already does the right thing
for stacking: batch 1's `FinishMerge` sees commits (batch 2) added since its pin,
emits `keep worktree (commits added since pin; left for a later merge)`, and does
**not** delete the session branch. The MCP async path also passes `inSession=true`,
so worktree removal is skipped regardless. No teardown changes needed.

## Scope boundaries

- **Worktree sessions only.** Implicit (main-checkout) sessions merge via
  `MergeImplicit` (push-based, no `PrepareMerge` prefix, no landing lock);
  stacking there is out of scope for v1. They keep single-job behavior but still
  get Deliverable 2's no-consume-on-refuse fix. (Decision C.)
- **`sc check` / check-async** are unaffected — no landing, nothing to stack.
- **`disable-merge-queue`** repos: the intra-session queue is independent of the
  per-repo landing lock, so it still functions; the head-of-queue merge just
  lands via the unqueued path. (Confirm during implementation.)

## Rollback

- Deliverable 2 is a pure reorder; rollback is a revert.
- Deliverable 1: gate the enqueue behind a sweatfile knob
  `[hooks].disable-merge-stacking` (default off = stacking on). Setting it true
  restores today's `ErrAlreadyRunning` refusal (with the Deliverable-2 fix
  intact: the refusal still won't consume). Single-config rollback, no revert.

## Tuning levers

- **Queue depth: unbounded (v1).** Rationale: an agent producing batches faster
  than depth × gate-time is unlikely, and each entry is cheap (params + a bound
  attestation). Change signal: a session accumulating a pathological queue
  (runaway loop) — then add a cap that refuses (without consuming) beyond N.
- **Failure cascade: any non-success drains.** Rationale: simplest correct
  semantics; the base assumption broke either way. Change signal: transient
  push failures proving common enough that a "retry the head, keep the queue"
  policy earns its complexity.

## Decisions for operator ratification

- **A. Queue persistence: in-process only (v1).** A serve restart loses the queue
  (and, already today, the in-flight job's liveness wake). Matches the existing
  `job.running` model. Persisted-in-job.json is a possible v2. *Recommend
  in-process.*
- **B. Queue depth: unbounded (v1).** *Recommend unbounded, revisit against
  real usage.*
- **C. Scope: worktree sessions only; implicit sessions keep single-job + get
  Deliverable 2.** *Recommend this boundary.*
- **D. Dequeue-time ringmaster job allocation.** The enqueue returns a
  spinclass-local queue position; the real ringmaster job (with wake) is
  allocated when the entry dequeues and runs. The agent learns the result via
  the self-describing completion wake. *Recommend allocate-at-dequeue.*
  **Response-wording requirement (ratified):** the enqueue tool result must
  state explicitly that a queued merge returns **no ringmaster job id** — so an
  agent cannot `job_wait` on it — and that the completion wake is the only
  signal. This must be discoverable from the enqueue response text itself, not
  only the FDR. (Matches the "end turn, get woken" guidance.)
- **E. FDR.** This extends FDR 0022 + 0007 + 0010. Write a new FDR
  (0025) at merge time, or fold into an existing one? *Recommend a new FDR.*

## Implementation order

1. Deliverable 2 — attestation no-consume-on-refuse (standalone commit).
2. Deliverable 1 — in-process per-worktree merge queue + chaining + drain.
3. Docs: FDR + sweatfile(5) knob + tool description updates.
4. (Separate task, per operator) spinclass#258 — remove the `issue` arg from
   spawn/fork-session.
