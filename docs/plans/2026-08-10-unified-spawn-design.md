# Unify spawn/fork into one spawn tool; drop fork-at-HEAD (spinclass#262)

Status: APPROVED 2026-08-11 (Sasha, interactive decision walkthrough)
Date: 2026-08-10 (finalized 2026-08-11)
Depends on: #258 (issue-arg removal) — landed.

## Problem

`spawn-session` and `fork-session` are two tools for one operation — start a
detached, harness-booted worker — split by target: spawn requires a DIFFERENT
repo (refuses the current one); fork is same-repo-only, forking at current HEAD.
The caller must know the taxonomy up front, and the seam shows in practice
(reach for spawn on the current repo → refused → re-issue as fork).

## Decision (interactive, 2026-08-11)

The fork-at-HEAD semantics are **dropped entirely**, not merged: a worker always
starts fresh off the target repo's default branch, and a session can reposition
its own branch to any commit on its own (the commit is reachable in the shared
repo for a same-repo worker). This collapses the "which starting state" axis to
a single behavior, so there is no `inherit`/`fork`/`base` flag at all.

Decisions:
- **state-source**: no flag — always fresh off default. (fork-at-HEAD dropped.)
- **Tool name**: keep `spawn-session` (MCP) / `sc spawn` (CLI); just relax
  `repo` to optional. No new name, no alias churn.
- **fork-session / `sc fork --brief`**: hard-remove. Its HEAD-inheritance no
  longer exists, so it can't alias to anything with the same behavior; a silent
  behavior-changing alias would be worse than a clean removal.
- **create-only `sc fork [branch]`**: kept, unchanged — a local worktree
  convenience (`shop.Fork`), independent of the spawn path (verified: shares no
  launch code with spawn-session; only `resolveForkSource`, which stays).
- **FDR**: extend FDR 0006 in place.

## Interface (after)

`spawn-session` MCP tool / `sc spawn [repo] --brief`:

| param | |
|---|---|
| `repo` | **optional**. Omitted or naming the current repo → this repo (fresh worktree off its default branch). A sibling dirname/path → that repo. `""` + not inside a repo → error. |
| `brief` | required; the worker's only context. |
| `description`, `model`, `hello-timeout` | as today. |

Worker always: fresh worktree off the target repo's default branch, spawned_by
lineage, blocking hello gate, `close-child-session` reaping — all unchanged.

`sc fork [branch]` (create-only, non-detached): unchanged. `sc fork --brief`:
rejected with "detached fork removed; use `sc spawn` (repo now optional)".

## Implementation

- `internal/spawn/resolve.go`: drop `rejectDriverRepo` (the same-repo refusal).
  Keep the git-repo / main-checkout validation. Its one caller is spawn.
- `cmd/spinclass/spawn_cmd.go`: `Repo` optional. runSpawn: `repo==""` →
  `driverRepoPath()` (error if that is empty — not in a repo); else
  `spawn.ResolveRepo`. Then `spawn.Launch` as today. `spawnParamList` marks
  `repo` not-required and documents the current-repo default.
- Remove the detached-fork surface:
  - `cmd/spinclass/fork_cmd.go`: delete `runForkDetached`, `handleForkSession`,
    `forkSessionParamList`, `forkDetachedParams`. Keep `resolveForkSource`
    (used by create-only `sc fork`).
  - `cmd/spinclass/commands_mcp_only.go`: unregister the `fork-session` tool.
  - `cmd/spinclass/commands_query.go`: `sc fork` drops the `--brief`/detached
    branch; `--brief` present → error pointing at `sc spawn`. Create-only path
    (`shop.Fork`) stays.
  - `internal/spawn/spawn.go`: delete `LaunchExisting` (only the detached fork
    used it). Keep `launchRendered` (shared with `Launch`).
  - Tests: drop `fork_cmd_test.go`'s detached-fork cases + LaunchExisting cases;
    keep create-only fork tests. spawn tests gain a current-repo case.
- Docs: FDR 0006 (interface + a note that fork-at-HEAD was dropped in #262),
  AGENTS.md spawn subsystem bullet, `sc spawn` CLI description, README CLI table
  (`sc fork` loses `--brief`).

## Rollback

Pure removal + a param relaxation; rollback is a revert. No dual-architecture
needed (nothing replaced with a new mechanism — a whole surface is deleted and
an existing one relaxed). If the fork-at-HEAD convenience is later missed, it is
re-addable as a spawn option, but the decision here is that self-repositioning
covers it.

## Ordering

After #258 (landed) and before #266 (async spawn): unify first so there is ONE
tool to make async.
