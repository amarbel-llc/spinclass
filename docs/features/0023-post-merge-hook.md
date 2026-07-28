---
status: experimental
date: 2026-07-27
promotion-criteria: |
  experimental -> testing: one real consumer wired end to end — circus's
  krone self-deploy (circus#139) triggering off `[hooks].post-merge` — with
  (1) a landed merge firing the hook exactly once against the sha that
  actually shipped, (2) an observed contended merge where a sibling session
  waits out this session's deploy hook before landing — logging the
  "merge queue: waiting behind <session>" heartbeats for the hook's duration,
  confirming a merge stays exclusive through post-merge — and (3) one
  deliberate hook failure confirming the merge still reports success with a
  warn point.
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
- **Runs under the per-repo merge lock**, as the merge's last stage — a merge
  stays exclusive end to end, so no sibling session can land (or deploy) while
  this hook is in flight. See Design for the cost.
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

### Placement: under the merge lock, as the last stage

The load-bearing decision. FDR 0022 gave merges a per-repo `flock` that
`FinishMerge` acquires **before** the gate and holds through gate → land →
push. The post-merge hook runs inside that region, as the last stage before
`FinishMerge` returns and the deferred `Release` fires.

The queue's contract is that a merge is **exclusive end to end**: while the
lock is held, no other session may perform any part of a merge. A post-merge
deploy is part of the merge. Releasing early would let a sibling session gate,
land and deploy while this session's deploy is still in flight — the two
deploys interleave, and the older one can win. That is precisely the failure
that motivated the hook (circus#139: a stale deploy silently overwriting a
newer live config), so escaping the lock to run it would reintroduce the bug
the feature exists to fix, one level up.

The hook also runs **synchronously within the merge call**, so the tool does
not return until it finishes:

- Fire-and-forget would orphan the hook's output (nothing to attach it to
  once the stream is finished) and, on the CLI path, race process exit —
  `sc merge` would exit mid-deploy. It would also drop the exclusivity
  guarantee above.
- Callers with no `post-merge` configured pay exactly nothing: the phase
  returns before doing any work when `PostMergeActive()` is false.

**The cost is real and accepted:** a slow post-merge hook extends the
exclusive region, so N racing sessions drain in N × (gate + hook) time rather
than N × gate time. This is the same burst-latency trade FDR 0022 already
made, paid unattended in background jobs with clown wakeups. A repo whose
deploys are slow enough for this to hurt should make the hook a cheap trigger
(enqueue, fire a webhook) rather than an inline deploy.

`internal/merge/post_merge_phase_test.go` pins the property with a hook that
blocks while the test tries to acquire the same lock from a second file
descriptor — flock being per-open-file-description, that genuinely contends.
The probe must time out while the hook runs, and must succeed once
`FinishMerge` returns; the test was confirmed to fail in both directions (an
early release, and a leaked lock).

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

Detaching a slow deploy so the merge lock is not held for its duration:

```toml
[hooks]
post-merge = """
[ "$SPINCLASS_MERGE_PUSHED" = 1 ] || exit 0
setsid ./bin/deploy.sh "$SPINCLASS_MERGED_SHA" </dev/null >>deploy.log 2>&1 &
"""
```

The redirects are **load-bearing**. spinclass captures hook output through a
pipe and `cmd.Wait()` blocks until every holder of its write end closes; a
detached child inheriting that pipe keeps it open, so the hook — and the lock
— blocks for the child's full duration regardless of the `&`. Measured, not
assumed: `TestRunPostMergeHookContextDetachedChildOutlivesHook` asserts the
hook returns in <500ms and the child still completes afterwards, and was
confirmed to fail (blocking the full child duration) with the redirects
removed.

`setsid` puts the child in its own session so it survives a process-group
signal; plain `&` suffices today but is not future-proof against #188.
**Detaching gives up the exclusivity guarantee** — two deploys can then
overlap — so detach only work that is safe to run concurrently or that
serializes at the far end.

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

- **The hook extends the exclusive region.** Sibling sessions queue behind it,
  so a slow deploy delays every other merge in the repo (they heartbeat
  "merge queue: waiting behind <session>" meanwhile). Deliberate — see Design
  — but it means the hook should be a cheap trigger, not a long inline deploy.
- **No inactivity watchdog.** A hook that never exits and never writes wedges
  the merge call AND holds the queue, since it runs under the lock. Self-bound
  it (`timeout 300 …`); `session-job-cancel` also kills it on the async path.
  Tracked as #246.
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
| lock scope | hook runs UNDER the landing lock | a merge is exclusive end to end; an early release lets two sessions' deploys interleave, reintroducing circus#139 one level up | queue latency from slow hooks becoming the dominant complaint, with a safe way to keep deploys ordered without the lock |
| synchronous vs backgrounded | synchronous | keeps output attributable, survives CLI process exit, and is what makes the exclusivity guarantee meaningful; zero cost when unconfigured | deploys long enough that blocking the merge call is itself the complaint, even async |
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
  runs under), [FDR-0018](0018-pre-merge-repair-phase.md) (the phase
  shape this mirrors), [FDR-0013](0013-isolated-build-worktree.md) (the
  PrepareMerge/FinishMerge split and the pinned-vs-landing sha distinction),
  [FDR-0014](0014-implicit-sessions.md) (the implicit-session merge path).
- `spinclass-sweatfile(5)` `[hooks]` §§ `post-merge`, `disable-post-merge`.
