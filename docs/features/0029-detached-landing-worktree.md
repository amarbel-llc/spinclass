---
status: experimental
date: 2026-09-02
promotion-criteria: A full fleet pass of gitSync merges lands without ever advancing a root checkout's local default ref; at least one refused push (stale tip or dropped credential) is observed to leave the session branch, its worktree, and the root ref untouched and to succeed on a plain re-merge; the close/clean/auto-close paths correctly classify origin-landed branches as integrated throughout.
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

## Interface

No new configuration. For a **gitSync worktree-session merge** on the default
(queued, FDR 0022) path, `merge.FinishMerge` now lands like this, under the
landing lock:

1. Re-pull the default branch in the root (unchanged — the self-healing
   catch-up), then the ancestry check.
2. **Always** create the disposable detached landing worktree
   (`.land-<branch>-<shortsha>-<pid>` under `.worktrees/`, checked out at the
   pinned sha), rebasing there onto the moved tip only when the ancestry check
   says the branch lost the race (unchanged conflict handling).
3. Gate on the landing sha (unchanged).
4. **Land by pushing**: `git push <remote> <landingSha>:refs/heads/<default>`
   run **from the landing worktree**. The fast-forward check is the remote's
   own (no `--force`), so a stale or unauthenticated push exits nonzero having
   moved nothing — locally or remotely. The root checkout's local default ref
   is **never advanced** by the merge; the next `PrepareMerge` pull (or
   base-branch freshening) catches it up. The ladder shows this as the
   `merge <branch>` point; there is no separate `push` point any more.
5. Teardown (unchanged pin-contract gating). Branch deletion is forced on this
   path: the local default ref was never advanced, so `git branch -d`'s
   "merged into HEAD" check cannot see the landing — safe because
   `tipMatchesPin` means the tip IS what just landed.
6. The post-merge phase (FDR 0023/0026) runs **in the landing worktree** — the
   exact tree that landed — rather than the session worktree or the root.

`gitSync=false` (local-only) merges still ff-only into the root: there, the
local ref IS the landing. Implicit main-checkout merges (FDR 0014) and the
`[hooks].disable-merge-queue` rollback path are unchanged.

**Consumers that read the local default ref** were fixed to count a commit as
integrated when it is reachable from the local default branch OR its
remote-tracking ref (`git.CommitsUnintegrated`), since a push landing advances
only the latter and a local-only landing only the former:
`close.RunResolved`'s unintegrated guard, `clean.scanWorktrees`'s merged
classification, and the session exit handler's auto-close gate
(`shop.closeShop`).

## Examples

    ✓ pull master
    ✓ rebase fast-aspen
    ✓ pull master (landing)
    ✓ pre-merge hook for fast-aspen: `just`
    ✓ merge fast-aspen
    ✓ remove worktree fast-aspen
    ✓ delete branch fast-aspen

After this, `git -C <repo> rev-parse master` still reports the pre-merge tip
(until the next pull), while `origin/master` carries the landing.

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
- **The root's local default ref lags** until the next pull. Anything that
  reads it without consulting the tracking ref sees a stale tip; the known
  consumers are fixed (above), and `git.CommitsUnintegrated` is the helper to
  reach for in any new one.
- **The re-pull in the root remains.** `git pull` in the main checkout is
  still the merge's catch-up mechanism, so the root is not fully untouched —
  it is only never advanced past origin by automation.

## Tuning Levers

None: the landing shape is not configurable beyond the existing
`[hooks].disable-merge-queue` rollback.

## More Information

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
