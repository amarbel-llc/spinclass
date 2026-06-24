---
status: experimental
date: 2026-06-08
promotion-criteria: |
  experimental -> testing: a real `merge-this-session-async` run on a
  nontrivial repo (e.g. a `nix build` + test-lane hook) where the agent
  edits/commits new work in the session worktree while the hook runs, and
  (1) the merge lands exactly the pinned sha, (2) the concurrent edit
  survives as new uncommitted/committed work, (3) the `.merge-*` build
  worktree is gone afterward. Plus one observation that `disable-merge-
  build-worktree = true` restores in-place behavior.
  testing -> accepted: ~1 week of real merges across repos under the
  default (build-worktree-on) path with no orphaned `.merge-*` worktrees,
  no hook breakage attributable to detached HEAD or the relocated cwd,
  and no need to set the opt-out anywhere.
---

# Isolated build worktree for pre-merge hooks

## Problem Statement

The `[hooks].pre-merge` command (the merge gate / agent-CI lane) historically
ran with `cmd.Dir` set to the live session worktree. For a nontrivial hook
(`nix build` + test lanes, minutes long) this froze the worktree: any edit the
agent made while the hook ran would race the build/test and dirty the tree
mid-merge. The `-async` merge/check tools detached the hook from the MCP request
timeout but bought no actual concurrency — the agent still could not safely edit.
See issue #106.

## Interface

By default the pre-merge hook now runs in a transient **detached build worktree**
pinned to the exact committed sha being merged:

- A hidden `.merge-<branch>-<shortsha>-<pid>` worktree is created as a sibling
  under `.worktrees/` (`git worktree add --detach`), the hook runs there, and it
  is removed (`git worktree remove --force`) when the hook finishes.
- The merge fast-forwards the **pinned sha** (`git merge --ff-only <sha>`), not
  the branch tip, so a commit landing on the branch while the hook runs is left
  for a later merge instead of leaking in.
- `[hooks].disable-merge-build-worktree = true` (sweatfile cascade, scalar
  override) reverts to running the hook in place in the session worktree.

Covers `merge`/`merge-this-session`(`-async`) and `check`/`check-this-session`
(`-async`) and `sc check`, since both funnel through `check.RunWithWriterContext`.

## Design

The merge flow is split (in `internal/merge/merge.go`) into:

- `PrepareMerge` — the fast, session-worktree-touching prefix: disable-merge
  gate → optional pull → rebase the branch onto the default → nothing-to-merge
  short-circuit → **pin** the post-rebase `HEAD` sha.
- `FinishMerge` — the slow, committing suffix against the pinned sha: pre-merge
  hook (in the build worktree) → `merge --ff-only <pinnedSha>` → worktree/branch
  teardown → push.

`ResolvedContext` (sync) runs both inline. `merge-this-session-async` runs
`PrepareMerge` **synchronously before returning the job id** — sharing one
`crap.Reporter`+buffer so the prefix's records are appended to the backgrounded
`FinishMerge`'s output as one stream (originally a `tap.Writer` via
`merge.NewMergeWriter`, deleted when merge moved to ndjson-crap; see FDR 0015)
— then backgrounds only `FinishMerge`. This is what makes async genuinely concurrent: the rebase (the one
step that mutates `wtPath`) completes before the agent is told the job started,
so it cannot race the agent's next edits, and rebase conflicts / nothing-to-merge
surface immediately instead of as an orphan job.

The build-worktree lifecycle lives at the shared hook chokepoint
(`check.resolveHookDir` + `git.WorktreeAddDetached`/`WorktreeForceRemove`); madder
still targets `wtPath` (the session worktree's blob store) — only the hook's
working directory relocates.

### Why rebase the real branch + pin, not rebase in the isolated worktree

The alternative — never touch `wtPath`, rebase+hook+merge entirely in the build
worktree — gives stronger concurrency (zero `wtPath` mutation) but leaves the
session branch and the default branch **diverged** between merges, relying on
`git rebase` patch-id dedup on the next merge and breaking ancestry-based "is
this branch merged" checks (`sc clean`, `CommitsAhead`, ff-only). Rebasing the
real branch keeps history strictly linear and preserves every existing invariant;
only the (expensive) hook relocates. The rebase is fast and inherent to the merge
anyway, so the freeze window shrinks from "the whole hook" to "the rebase."

## Examples

```toml
# Default: hook runs in a pinned detached build worktree.
[hooks]
pre-merge = "just"

# Opt out: run the hook in place in the session worktree (legacy).
[hooks]
pre-merge = "just"
disable-merge-build-worktree = true
```

## Limitations

- **Detached HEAD.** The branch stays checked out in the session worktree, so the
  build worktree is detached; a hook that reads the current branch name (`git
  rev-parse --abbrev-ref HEAD`) sees `HEAD`. Opt out if a hook needs the branch
  ref.
- **Devshell loaded from the session worktree.** When the session worktree has a
  `.envrc`, the hook runs under `direnv exec <session-worktree> sh -c …` so a
  devShell-provided hook command resolves regardless of the ambient PATH
  (spinclass#198). The devshell is loaded from the session worktree (which has the
  allowed `.envrc`), not the build worktree (which is checked out from the tracked
  tree only and never has the git-excluded `.envrc` nor a `direnv allow` record).
  Without an `.envrc`, the hook runs as a bare `sh -c` inheriting the `serve`/CLI
  process environment (legacy behavior).
- **`$WORKTREE` is the session worktree, `$PWD` is the build worktree.** As a
  consequence of the above, the hook's `WORKTREE` env var points at the session
  worktree (the logical session location) while its working directory (`$PWD`) is
  the build worktree pinned to the committed sha. A hook that `cd "$WORKTREE"`
  would land in the session worktree and verify *uncommitted* state instead of the
  pinned tree — read the tree from `$PWD`, not `$WORKTREE`.
- **Origin moved during hook.** If `origin/<default>` advances while the hook
  runs, the final `merge --ff-only` into the local default can still fail —
  pre-existing behavior, not introduced here.
- **Crash orphans.** The deferred remove covers normal completion and most
  failures; a hard `serve` crash mid-hook leaves a `.merge-*` directory, reaped
  by `git worktree prune` (run before each add) and by `sc clean`. A dedicated
  startup sweep is a possible follow-up.

## More Information

- Issue: #106.
- `internal/merge/merge.go` (`PrepareMerge`/`FinishMerge`),
  `internal/check/check.go` (`resolveHookDir`, `hookSha` threading),
  `internal/git/git.go` (`WorktreeAddDetached`/`WorktreePrune`/`RevParse`),
  `cmd/spinclass/commands_mcp_only.go` (`handleMergeThisSessionAsync`).
- `spinclass-sweatfile(5)` `[hooks]` § `pre-merge` and
  `disable-merge-build-worktree`.
