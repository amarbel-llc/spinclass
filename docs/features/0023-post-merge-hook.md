---
status: experimental
date: 2026-07-27
promotion-criteria: |
  experimental -> testing: one real consumer wired end to end — circus's
  krone self-deploy (circus#139) triggering off `[hooks].post-merge` — with
  (1) a landed merge firing the hook exactly once against the sha that
  actually shipped, (2) an observed contended merge where a sibling session
  acquires the merge lock while this session's deploy hook is still running
  (the FDR 0022 interaction this design exists for), and (3) one deliberate
  hook failure confirming the merge still reports success with a warn point.
  testing -> accepted: ~2 weeks of real deploys with no case where a
  post-merge failure was mistaken for a merge failure (or vice versa), and
  no repo needing `disable-post-merge`.
---

# `post-merge`: a hook on the far side of a landed merge

## Problem Statement

`[hooks]` could gate a merge (`pre-merge`), repair the commit being merged
(`repair`), and repair each commit as authored (`pre-commit`) — but nothing
ran **after** a merge reached the default branch. Anything wanting to react
to "this landed" had to be triggered out of band, which admits a window
where the reaction and the merge disagree.

The motivating failure (circus, 2026-07-27, circus#139): circus's krone host
self-deploys over a forced-command SSH key. A worktree held unmerged fixes to
that self-deploy script, hand-deployed to krone ahead of merging. A separate,
already-merged trigger then ran its own `git fetch/merge` from `origin/master`
— still behind those local fixes — and silently deployed an **older** config
over the newer live one, dropping a newly added SSH key entry while reporting
"healthy" (the older config was valid, just stale). The fix direction: make
*merging* the deploy trigger, so a locally-live system can never get ahead of
what is merged. That needs something that fires reliably once a merge is
confirmed landed. See issue #244.

## Interface

```toml
[hooks]
post-merge = "deploy-krone.sh"

# Suppress an inherited command without clearing it.
disable-post-merge = true
```

- Runs **only on a fully successful merge**: the ff-only landed and, with git
  sync, the push succeeded. Every earlier failure path returns first.
- **Non-fatal.** The merge cannot be undone by the time the hook runs, so a
  nonzero exit does not fail the merge. It emits a `severity=warn` not-ok test
  point carrying the hook's output; the merge still reports success. Retrying
  the merge would find nothing to merge, so a failure here is acted on out of
  band.
- **Runs after the per-repo merge lock is released** (see Design).
- Environment, on top of the usual inherited env and `WORKTREE`:

  | Variable | Meaning |
  |---|---|
  | `SPINCLASS_MERGED_SHA` | the commit that landed — the **landing** sha on a rebased queued landing, not the original pin |
  | `SPINCLASS_MERGED_BRANCH` | the branch that was merged |
  | `SPINCLASS_DEFAULT_BRANCH` | the branch it landed on |
  | `SPINCLASS_MERGE_PUSHED` | `1` when pushed to the remote, `0` when local-only |
  | `SPINCLASS_REPO_PATH` | the main checkout |

- Working directory: the session worktree when it still exists (teardown may
  have removed it), otherwise the main checkout — which is on the merged tip
  either way. No detached build worktree.
- Applies to worktree sessions and to implicit (main-checkout) sessions.
  `sc check` / `check-this-session` never run it — nothing lands.
- Cascade: scalar override, like every other `[hooks]` string.

## Design

### Placement: outside the merge lock, inside the call

The load-bearing decision. FDR 0022 gave merges a per-repo `flock` that
`FinishMerge` acquires **before** the gate and holds through gate → land →
push. A post-merge hook is typically a deploy measured in minutes. Running it
inside that region would put every sibling session in the repo behind this
session's deploy, converting the merge queue's bounded "N × gate-time" into
"N × (gate + deploy) time" for work the lock does not protect.

So the queued path releases the lock **explicitly** once `teardownAndPush`
returns, and only then runs the hook. `mergelock.Lock.Release` is documented
idempotent, so the existing `defer` stays untouched as the error-path net.
`internal/merge/post_merge_phase_test.go` pins this with a hook that blocks
while the test acquires the same lock from a second file descriptor — flock
being per-open-file-description, that genuinely contends, and the test was
confirmed to fail when the release is removed.

The hook still runs **synchronously within the merge call**, so the tool does
not return until it finishes. That is deliberate:

- Fire-and-forget would orphan the hook's output (nothing to attach it to
  once the stream is finished) and, on the CLI path, race process exit —
  `sc merge` would exit mid-deploy.
- Callers with no `post-merge` configured pay exactly nothing: the phase
  returns before doing any work when `PostMergeActive()` is false.
- The latency is the user's own opt-in, and it no longer costs anyone else.

### Non-fatal reporting

`crap.TestStream` offers `Ok` / `NotOk` / `Skip` and no warning verdict, so a
failure is reported as `NotOk` (visibly red, tallied in the summary) with
`severity: "warn"` in the diagnostic, while `runPostMergePhase` returns no
error. Returning an error was rejected: `merge-this-session` would report
failure for a merge that landed **and pushed**, and the natural agent response
— re-run the merge — hits "nothing to merge". The point's description and
diagnostic message both say the merge already landed, so the two are not
confusable.

### Shape

Modelled on the `repair` phase (FDR 0018), not the `pre-merge` gate: a single
buffered hook run and one test point, no madder blob, no output-format
parsing, no build worktree. Output is teed to the async job log when one is
present, so a slow deploy is tailable via `session-job-status`.

`runHookInDir` grew a sibling `runHookInDirEnv` taking `extraEnv`; post-merge
is its only caller, and `runHookInDir` delegates with `nil` so every other
hook is byte-for-byte unchanged.

### Rejected

- **Backgrounding the hook** (fire-and-forget goroutine): orphaned output,
  races process exit on the CLI path, no way to report the result.
- **Reusing `inactivity-timeout`** as a post-merge watchdog: that knob is
  documented as the pre-merge gate's watchdog, and silently extending its
  scope would change the meaning of every existing config. A hook that can
  wedge should bound itself (`timeout 300 …`). Revisit if wedged deploys
  prove common.
- **Running in a detached worktree** (the FDR 0013 pattern): pointless — the
  merge has already landed, so there is nothing to isolate it from.

## Examples

Deploy only what actually shipped, and only once it is public:

```toml
[hooks]
pre-merge = "just"
post-merge = """
[ "$SPINCLASS_MERGE_PUSHED" = 1 ] || exit 0
timeout 600 ./bin/deploy.sh "$SPINCLASS_MERGED_SHA"
"""
```

Result stream on a landed merge:

    ✓ merge bright-olive
    ✓ push
    ✓ post-merge bright-olive (a1b2c3d4e5f6)

and when the deploy fails — note the merge still succeeds:

    ✓ merge bright-olive
    ✓ push
    ✗ post-merge bright-olive (a1b2c3d4e5f6)
      severity: warn
      message: post-merge hook failed: exit status 7 (the merge already
               landed — nothing was rolled back)

## Limitations

- **The merge call still blocks on the hook.** Other sessions are not
  serialized, but this session's `merge-this-session` does not return until
  the hook finishes. Use the async merge tool for long deploys.
- **No inactivity watchdog.** A hook that never exits and never writes wedges
  the merge call (not the queue). Self-bound it.
- **Fires once per landing, with no delivery guarantee.** A crash between the
  push and the hook loses the trigger; nothing records that the hook is owed.
  A consumer needing at-least-once semantics must reconcile from the merged
  sha independently.
- **`SPINCLASS_MERGED_SHA` is the landing sha**, which on a rebased queued
  landing is not any sha the session branch ever had. Consumers keying on
  session-branch history need to account for that.
- Not run by `sc check` / `check-this-session`, by a merge that fails at any
  step, or by a nothing-to-merge short-circuit.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| synchronous vs backgrounded | synchronous | keeps output attributable and survives CLI process exit; zero cost when unconfigured | deploys long enough that blocking the merge call is itself the complaint, even async |
| failure verdict | `NotOk` + `severity=warn`, no error | visible without making a landed merge look failed | agents ignoring the warn line, or conversely treating it as a merge failure |
| watchdog | none | `inactivity-timeout` means the pre-merge gate; scope creep would change existing configs | wedged post-merge hooks in practice ⇒ add a distinct `post-merge-timeout` |

## More Information

- Issue: #244. Motivating consumer: circus#139 (krone self-deploy).
- Code: `internal/merge/merge.go` (`runPostMergePhase`, and its call sites in
  `FinishMerge` / `finishMergeUnqueued` / `MergeImplicit`),
  `internal/sweatfile/sweatfile.go` (`PostMergeHookCommand`,
  `PostMergeDisabled`, `PostMergeActive`), `internal/sweatfile/apply.go`
  (`RunPostMergeHookContext`, `runHookInDirEnv`),
  `internal/merge/post_merge_phase_test.go`,
  `internal/sweatfile/postmerge_test.go`.
- Related FDRs: [FDR-0022](0022-per-repo-merge-queue.md) (the lock this hook
  must run outside of), [FDR-0018](0018-pre-merge-repair-phase.md) (the phase
  shape this mirrors), [FDR-0013](0013-isolated-build-worktree.md) (the
  PrepareMerge/FinishMerge split and the pinned-vs-landing sha distinction),
  [FDR-0014](0014-implicit-sessions.md) (the implicit-session merge path).
- `spinclass-sweatfile(5)` `[hooks]` §§ `post-merge`, `disable-post-merge`.
