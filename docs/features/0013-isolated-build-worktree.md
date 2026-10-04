---
status: testing
date: 2026-06-08
promotion-criteria: |
  experimental -> testing: PROMOTED 2026-09-28 on operator confirmation. The
  feature has been default-on since 1520353 (2026-06-08) and no fleet
  sweatfile opts out. A real `merge-this-session-async` run on a
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

> **Amended 2026-10-04 (#188):** cancellation.

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
- **Cancellation reaches the hook only from some sources (#188).** The build
  worktree is removed only after the hook returns. A cancel reaches the hook
  from an async job (`session-job-cancel`, the inactivity watchdog), a signal
  to `serve`, or a signalled `sc merge|check|run`. A hard kill of spinclass
  (SIGKILL, or a second Ctrl-C) still orphans the hook and leaves `.merge-*`
  for `sc clean`; whether that removal is safe with live processes inside is
  spinclass#350. **Rejecting or interrupting one sync MCP call does not cancel
  it**: go-mcp treats `notifications/cancelled` as a no-op and runs every
  handler under one server-wide ctx, so this waits on purse-first#200
  (per-request cancel). #188 is therefore only partially addressed.
- **Scope setup failure is a warn-and-fallback (transitional).** See
  "Cancellation and scoping" below.

## Cancellation and scoping (2026-10-04, #188)

Every synchronous gate (`merge-this-session`, `check-this-session`, `sc merge`,
`sc check`, `sc run`) now passes a real ctx to the pre-merge hook, so a cancel
kills the hook's process tree before `defer cleanup()` removes the build
worktree. `cmd.Cancel` is SIGTERM, escalating to SIGKILL after `cancelGrace`
(10s); there is still no `Setpgid` (FDR 0023).

**Scoping.** The pre-merge hook runs in a transient systemd scope
(`systemd-run --user --scope`; #25). Its id comes from
`clown.ScopeIDFromContext`: the async job id, or, for a synchronous gate, a
local `sync-<kind>-<pid>-<nanos>-<seq>` id attached by `clown.WithLocalScope`.
That is a ctx key separate from `WithJobID`, so a synthetic id is never taken
for a ringmaster job and is never passed to ringmaster. A cancel calls
`clown.ScopeStop` to SIGKILL the whole cgroup. The post-merge hook is never
scoped (FDR 0023). A host with no scope tier at all (no systemd user bus,
macOS, `RINGMASTER_DISABLE_SCOPE`) simply runs the hook bare. The real scope
path is dogfooded, not covered by CI (no user bus in the sandbox).

**Cancellation sources.**

- `sc merge|check|run`: SIGINT, SIGTERM or SIGHUP cancels (`gateSignalContext`).
  The first signal starts the teardown; a second force-exits. A signal that
  was already ignored when `sc` started (`nohup`, a non-job-control `&`) stays
  ignored and never cancels. `sc run` installs the gate only once the session
  exists, so Ctrl-C during its stdin read and attach keeps the default
  behaviour.
- `serve`: SIGINT or SIGTERM cancels in-flight tool calls. SIGHUP is
  discarded (a drained `signal.Notify` channel, not `signal.Ignore`, which
  children would inherit across exec).
- Stdin EOF does **not** cancel. Merges are session-durable: closing the
  client session leaves an in-flight sync merge running, because go-mcp
  drains in-flight handlers before closing the transport (purse-first
  `server/server.go:63-68,100-105`). Only a signal to `serve`, or later
  purse-first#200, aborts one, and then the hook tree is torn down rather than
  orphaned. `serve` discards SIGHUP for the same reason: a dropped terminal
  must neither kill it mid-merge nor cancel it; the EOF that follows drains.

**Cancel after landing.** The ctx also reaches the post-merge phase, so a
SIGTERM to `serve` or `sc` during a deploy SIGTERMs the post-merge command on
the sync and CLI paths, exactly as async `session-job-cancel` does. Detached
children survive (FDR 0023). Accepted.

**Scope setup failure.** If the scope cannot be set up, the hook falls back
ONCE to the bare hook, writing `[spinclass] hook scope unavailable (...);
running unscoped`. `[hooks].require-hook-scope = true` makes that a hard
failure instead. The knob governs setup failure only. Detection is a start
marker written inside the scope immediately before `exec` of the hook: a hook
that started is never re-run, and a cancel before it starts is never a
fallback. The behavior is uniform across sync, CLI and async (async previously
had no fallback). Trajectory: opt-in now, later the default (flipped to an
opt-out), eventually the fallback is removed.

**Residual.** With no scope, a hook whose top process swallows SIGTERM is
SIGKILLed alone after `cancelGrace` and its descendants may survive; a warn
line in the hook output says so.

## More Information

- Issue: #106.
- `internal/merge/merge.go` (`PrepareMerge`/`FinishMerge`),
  `internal/check/check.go` (`resolveHookDir`, `hookSha` threading),
  `internal/git/git.go` (`WorktreeAddDetached`/`WorktreePrune`/`RevParse`),
  `cmd/spinclass/commands_mcp_only.go` (`handleMergeThisSessionAsync`).
- `spinclass-sweatfile(5)` `[hooks]` § `pre-merge` and
  `disable-merge-build-worktree`.
