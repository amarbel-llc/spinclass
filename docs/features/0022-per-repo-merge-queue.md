---
status: experimental
date: 2026-07-19
promotion-criteria: |
  experimental -> testing: one real contended landing on a busy repo (e.g.
  cutting-garden under parallel session activity) where (1) two concurrent
  merges serialize — the loser logs "merge queue: waiting behind <session>"
  heartbeats and emits the "merge queue wait" test point, (2) a landing whose
  default branch moved during the gate rebases in a `.land-*` worktree, is
  re-gated on the LANDING sha, and ff-lands with the "(rebased onto moved
  <default>)" label, and (3) the `.land-*` worktree is gone afterward. Plus
  one observation that `disable-merge-queue = true` restores the fail-on-race
  path.
  testing -> accepted: ~1 week of real merges on contended repos with zero
  green-gate-then-ff-fail job failures (the #235 failure mode), no orphaned
  `.land-*` worktrees, no lock wedges needing manual intervention, and no
  repo needing the opt-out.
---

# Per-repo merge queue: serialized landings, gate under the lock

## Problem Statement

The final `git merge --ff-only <pinnedSha>` raced concurrent sessions'
merges: when the default branch moved during the ~12-minute pre-merge gate,
the ff-only refused and the WHOLE job failed — costing a full re-gate plus a
fresh pre-merge skill attestation per retry, for content that was already
validated. On 2026-07-18 cutting-garden hit four green-gate-then-ff-fail
occurrences across three concurrent sessions in one working day; landing a
2-file doc-only merge required hand-coordinating a merge hold over chat.
See issue #235.

## Interface

By default, merges for a repo **serialize on a per-repo landing lock**, and
the pre-merge gate runs **under the lock against the exact sha that lands**:

- The lock is an advisory `flock(2)` on `spinclass-merge.lock` inside the
  repo's shared git common dir (`internal/mergelock`), so every worktree of
  the repo contends on the same lock. It self-releases on process death (no
  stale-lock reaping) and the file is never unlinked.
- `merge.FinishMerge` acquires the lock **before the gate**. Under the lock:
  re-pull the default branch (gitSync only) → ancestry check → if the
  default tip moved past the pin, rebase the pinned commits onto it in a
  transient `.land-<branch>-<shortsha>-<pid>` worktree → run the pre-merge
  gate on the **landing sha** → `merge --ff-only <landingSha>` → teardown
  (`branch -D` when rebased) → push → release.
- A landing-rebase conflict is the queue's **only hard failure class**
  (`merge.ErrIntegrationConflict`, with the conflicting paths when known).
  Recovery is a plain re-merge: the fresh `PrepareMerge` rebases the session
  worktree onto the moved tip so the conflict is resolved there.
- While queued, the waiter heartbeats to the async job log ("merge queue:
  waiting behind <session>", every 30s) — visible via ringmaster's
  `status --tail` (the job log is teed into its spool, #251) — and, once
  acquired, emits a single
  "merge queue wait <elapsed> (behind <session>)" test point. The holder's
  session key is written into the lock file so waiters can name it.
- The `[hooks].inactivity-timeout` watchdog wraps only the hook subprocess,
  so time spent queued is exempt from it.
- Async split is unchanged: `PrepareMerge` still runs synchronously before
  the job id returns; the whole queue wait happens inside the background
  `FinishMerge` job.
- `[hooks].disable-merge-queue = true` (sweatfile cascade, scalar override)
  restores the pre-#235 path verbatim: hook on pinnedSha → ff-only →
  teardown → push, no lock, no re-pull, no landing rebase — a moved default
  branch fails the ff-only exactly as before.

**Confidence property:** every commit landing on the default branch was
gate-tested against the exact tree it lands on. **Cost:** burst latency — N
racing sessions drain in N × gate-time, paid unattended in background jobs
with clown wakeups. With no contention, behavior is byte-for-byte the
pre-queue flow (plus the re-pull/ancestry check).

## Design

### Rejected alternatives (issue #235 thread)

- **Bounded retry loop** (re-enter pull → rebase → gate → ff on ff failure):
  still pays a full re-gate per retry and generates retry traffic under
  contention.
- **Optimistic landing, "gate is spent"** (initially proposed, superseded):
  serialize and rebase the loser onto the moved tip but skip the re-gate,
  accepting the integration-risk window since both sides passed their own
  gates. Judged not worth the confidence cost — GitHub's merge queue
  re-gates the speculative merged result, and we chose the same confidence
  property.
- **Speculative parallel gating GitHub-style** (gate optimistic merge
  results concurrently): complexity not warranted at this scale; queued
  entries wait instead of building optimistically.

### Lock mechanics

`mergelock.Acquire` polls `LOCK_EX|LOCK_NB` at 500ms rather than issuing a
blocking `LOCK_EX`: a blocking flock parks the thread in the kernel with no
way to abandon the wait on context cancellation, and interruption semantics
differ across platforms — the poll keeps acquisition ctx-cancellable and
darwin-portable at negligible latency against merge durations. The lock file
is deliberately never unlinked: unlinking on release while a waiter holds an
fd to the old inode would let a later acquirer lock a fresh inode at the
same path, and two processes would each "hold" the lock (the two-inode
hazard). File contents (holder identity) carry no locking semantics.

The lock file lives in `git.CommonGitDir` (the shared `.git` dir), not the
main-checkout root, so it never appears in worktree status.

### Landing rebase

The rebase runs in a transient detached worktree (mirroring the FDR 0013
`.merge-*` pattern), NOT the session worktree — whose HEAD may legitimately
have advanced past the pin (that is the pin contract). Until the ff-only
completes, the landing commit's only ref is that worktree's HEAD, so cleanup
runs only after the merge (with a deferred idempotent safety net for error
paths). After a rebased landing the session branch tip is no longer an
ancestor of the default branch, so teardown uses `branch -D`; force is safe
because the patch-identical content just landed via the rebased sha.

## Examples

```toml
# Default: merges serialize; the gate runs under the lock on the landing sha.
[hooks]
pre-merge = "just"

# Rollback: restore the pre-#235 fail-on-race path.
[hooks]
pre-merge = "just"
disable-merge-queue = true
```

Contended async merge, seen from the losing session's job log:

    merge queue: waiting behind cutting-garden/sharp-hazel (30s)
    merge queue: waiting behind cutting-garden/sharp-hazel (1m0s)
    ...

and in the result stream:

    ✓ merge queue wait 11m32s (behind cutting-garden/sharp-hazel)
    ✓ pull master (landing)
    ✓ land bright-olive
    ✓ pre-merge hook
    ✓ merge bright-olive (rebased onto moved master)

## Limitations

- **Implicit-session merges are out of scope.** `MergeImplicit` (FDR 0014)
  does not queue — its race surfaces as a push rejection on origin, a
  different failure shape.
- **Host-local only.** flock does not serialize across hosts (and is not
  NFS-safe); a cross-host origin race still fails at push, the legible
  pre-existing error.
- **Orphaned `.land-*` worktrees** from a crash between worktree add and
  cleanup are reaped by `sc clean` alongside `.merge-*` build worktrees
  (dead-pid detection via the trailing `-<pid>`; issue #237, resolved). A
  stale dir from an interrupted run at the same name is also cleared on the
  next attempt.
- **With `[hooks].disable-merge-build-worktree`** the gate runs in the
  session worktree (pre-existing `resolveHookDir` behavior), so a rebased
  landing's gate verifies whatever that worktree has checked out — not the
  landing sha.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| lock poll interval | 500ms | negligible against minutes-long gates; keeps acquisition ctx-cancellable | measurable idle-handoff latency mattering in practice |
| wait heartbeat interval | 30s | legible job-log cadence without noise | logs too chatty on long queues, or agents wanting finer progress |

## More Information

- Issue: #235 (design thread; the "gate is spent" framing and its
  supersession live there). Wasted-attestation adjacency: #219.
- Commits: 4839de5 (`internal/mergelock`), e9a7221
  (`[hooks].disable-merge-queue`), 1750f25 (`FinishMerge` restructure).
- Code: `internal/mergelock/mergelock.go`, `internal/merge/merge.go`
  (`FinishMerge`, `finishMergeUnqueued`, `rebaseLanding`,
  `ErrIntegrationConflict`, `LandWorktreePrefix`),
  `internal/merge/queue_test.go`.
- Related FDRs: [FDR-0013](0013-isolated-build-worktree.md) (the
  PrepareMerge/FinishMerge split and pinned-sha contract this builds on),
  [FDR-0014](0014-implicit-sessions.md) (the excluded implicit-merge path),
  [FDR-0010](0010-clown-job-wakeup-producer.md) (the wakeups that make
  queue latency payable unattended).
- `spinclass-sweatfile(5)` `[hooks]` § `disable-merge-queue`.
