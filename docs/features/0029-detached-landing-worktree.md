---
status: experimental
date: 2026-09-02
promotion-criteria: One week of fleet gitSync merges with `spinclass.merge.local_advance.skip` ≤ 15% of `spinclass.merge.landed` and `local_advance.ok + local_advance.skip == landed` (counters from spinclass#314); at least one `push_refused` observed to leave the session branch, its worktree, and the root ref untouched and to succeed on a plain re-merge; the close/clean/auto-close paths correctly classify origin-landed branches as integrated throughout.
revised: 2026-09-23 — local default branch advanced opportunistically after the push (spinclass#295); merges and session creation target the remote's default branch, never local (spinclass#315)
---

# Detached landing worktree (Alt B)

## Problem Statement

Before this record, `merge-this-session`'s landing ran with CWD = the repo's
**main checkout**: `git merge --ff-only <landingSha>` advanced the checked-out
default branch there, then `git push` published it, authenticating off the
`spinclass serve` process environment. Three consequences (spinclass#284):

1. The operator's root checkout was mutated by automation — its default branch
   advanced on every merge — defeating "the root checkout is the operator's".
2. Failure asymmetry: the local ff happened BEFORE the push, so a refused push
   (a dropped forwarded ssh-agent killed two multi-hour fleet passes on
   2026-09-01) left local `master` AHEAD of origin — a divergence to repair,
   not a no-op to retry.
3. Per-session push credentials could not be scoped: the push ran outside any
   session worktree, where worktree-scoped git config cannot reach it.

The invariant "landings happen in the root" was only half real: for gitSync
worktree merges the landing commit was already produced off-root (the rebase
case in a transient `.land-*` worktree, the pure-ff case as the branch tip);
the root's only roles were "hold checked-out master for the ff" and "be the
push CWD".

## Revised (2026-09-23): the local default branch follows the landing

The original record held that the root's local default ref is **never
advanced** by a merge. That rule was misguided, and live agent sessions
disproved it (spinclass#295): agents and humans read local `master` as "what
merged", saw it trail `origin/master` (by exactly one merge — the next merge's
pull dragged it forward), and spent effort doubting landings that had
succeeded. It was also never really held: the pre-merge pull already moved the
root's ref on every merge, one merge late.

It is replaced by one principle (spinclass#315, `internal/landing`):

- **A merge targets the default remote's default branch.** Rebase, ancestry
  check, landing rebase, the nothing-to-merge check, and session creation's
  cut point all read the remote-tracking ref (`landing.Target.Ref()`), never
  the local branch. Only the fetch is fatal.
- **The local default branch is advanced for ergonomics, not correctness** —
  once, right after a successful push, under the landing lock, before the
  post-merge phase, with git's `merge --ff-only` semantics. When that is not
  possible (dirty overlapping checkout, diverged, ahead) the step is a
  `# SKIP` that opens with `merge LANDED on <remote>/<branch> at <sha>`, names
  the reconcile command, and cites `spinclass-local-default-ref(7)`. The merge
  still succeeds, and the async completion wake carries the skip line.
- **A local-only merge is a merge whose remote is self**: its target is the
  local branch, fetching is a no-op, and landing IS the (fatal) fast-forward.

The failure-symmetry property below is unchanged: the advance runs only after
the push succeeds, so a refused push still moves nothing.

## Interface

No new configuration. For a **gitSync worktree-session merge** on the default
(queued, FDR 0022) path, `merge.FinishMerge` now lands like this, under the
landing lock:

1. Re-fetch the landing target (`fetch <remote>/<branch> (landing)`), then the
   ancestry check against the tracking ref.
2. **Always** create the disposable detached landing worktree
   (`.land-<branch>-<shortsha>-<pid>` under `.worktrees/`, checked out at the
   pinned sha), rebasing there onto the moved tip only when the ancestry check
   says the branch lost the race (unchanged conflict handling).
3. Gate on the landing sha (unchanged).
4. **Land by pushing**: `git push <remote> <landingSha>:refs/heads/<default>`
   run **from the landing worktree**. The fast-forward check is the remote's
   own (no `--force`), so a stale or unauthenticated push exits nonzero having
   moved nothing — locally or remotely. The ladder shows this as the
   `merge <branch>` point; there is no separate `push` point any more.
5. **Advance local** (revised): fast-forward the root's local default branch to
   the landing sha — an `advance local <branch> to <sha>` point, or a skip (see
   above). Never fatal.
6. Teardown (unchanged pin-contract gating). Branch deletion is forced on this
   path: the local advance may have been skipped, so `git branch -d`'s
   "merged into HEAD" check cannot rely on seeing the landing — safe because
   `tipMatchesPin` means the tip IS what just landed.
7. The post-merge phase (FDR 0023/0026) runs **in the landing worktree** — the
   exact tree that landed — rather than the session worktree or the root.

`gitSync=false` (local-only) merges fast-forward the local default branch
(through whichever worktree has it checked out): there, the local ref IS the
landing. Implicit main-checkout merges (FDR 0014) and the
`[hooks].disable-merge-queue` rollback path are unchanged (unifying them is
spinclass#299).

**Consumers that read the local default ref** were fixed to count a commit as
integrated when it is reachable from the local default branch OR its
remote-tracking ref (`git.CommitsUnintegrated`), since a push landing advances
only the latter and a local-only landing only the former:
`close.RunResolved`'s unintegrated guard, `clean.scanWorktrees`'s merged
classification, and the session exit handler's auto-close gate
(`shop.closeShop`).

## Examples

    ✓ fetch origin/master
    ✓ rebase fast-aspen
    ✓ fetch origin/master (landing)
    ✓ pre-merge hook for fast-aspen: `just`
    ✓ merge fast-aspen
    ✓ advance local master to 3928513a1b2c
    ✓ remove worktree fast-aspen
    ✓ delete branch fast-aspen

With the operator's overlapping edit in the root checkout, the advance skips
and the merge still succeeds:

    ✓ merge fast-aspen
    ↷ advance local master # SKIP merge LANDED on origin/master at 3928513a1b2c;
      only local master was not advanced: uncommitted changes in <root> block
      the fast-forward of master; reconcile: commit or stash them, then git -C
      <root> merge --ff-only <sha> (see spinclass-local-default-ref(7))

A refused push:

    ✗ merge fast-aspen
      git push origin <sha>:refs/heads/master: exit status 128
      ...Permission denied (publickey)...

leaves `origin/master`, the root's `master`, the session branch, and its
worktree exactly as before; a re-merge is a plain retry.

## Limitations

- **Worktree-session gitSync merges only.** Local-only merges and implicit
  main-checkout sessions keep landing in the root by design; the
  `disable-merge-queue` rollback path keeps the pre-#235 ff-then-push shape
  verbatim (it is a rollback knob, not a second maintained landing path).
- **The root's local default ref can still lag** when the post-push advance
  skips. Anything that reads it without consulting the tracking ref may see a
  stale tip; the known consumers are fixed (above), and
  `git.CommitsUnintegrated` is the helper to reach for in any new one.
- **The root is not untouched.** The local advance moves the root's default
  branch (or, with it checked out, its working tree) — deliberately, and only
  ever forward to what origin already holds.

## Tuning Levers

None: the landing shape is not configurable beyond the existing
`[hooks].disable-merge-queue` rollback.

## Metrics (spinclass#314)

The promotion criterion reads statsd counters (`internal/statsd`, emitted from
`merge.FinishMerge`'s queued remote landing only): `spinclass.merge.landed`,
`.push_refused`, `.local_advance.ok`, `.local_advance.skip`, and
`.local_advance.skip_reason.<dirty_overlap|diverged|ahead|error>`. Dimensions
live in the name (the graphite backend drops tags); `ok + skip == landed` by
construction. Emission is opt-in (`STATSD_HOST`/`STATSD_PORT` present) and
killed by `SPINCLASS_DISABLE_STATSD=1`, which `testgit.SetHermeticEnv` sets so
fixture merges never reach the fleet counters.

## More Information

- spinclass#295 / #315 — the revision: advance local opportunistically; target
  the remote, never local. `spinclass-local-default-ref(7)` is the reader-facing
  explanation the skip cites. spinclass#314 — the counters the promotion
  criteria read.
- spinclass#284 — the investigation, POC (git 2.55), dependency inventory,
  and the confirmed non-issues (mergelock keyed to the common git dir, the
  `SPINCLASS_MERGED_*` env, the pre-merge build worktree, `sc list`, base
  freshening).
- FDR 0028 — per-session forge push credentials: the consumer this landing
  shape exists to enable (worktree-scoped `credential.helper` +
  `pushInsteadOf` on the landing worktree reach the merge push).
- FDR 0013 (the detached-worktree pattern), FDR 0022 (the merge queue whose
  landing this record reshapes), FDR 0023/0026 (the post-merge phase now
  running in the landing worktree).
