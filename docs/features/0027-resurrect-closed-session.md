---
status: accepted
date: 2026-08-26
promotion-criteria:
---

# Resurrecting a closed session

## Problem Statement

`sc close` and `close-child-session` force-delete a session's worktree and
branch (`git worktree remove --force` + `git branch -D`) without ever
capturing the branch's tip commit. If that turns out to have been a mistake —
a session closed prematurely, or an agent reaping a spawned worker without
checking with the user first — the only recovery path was manual `git
reflog` archaeology in the parent repo, assuming the reflog entry hadn't
already expired. There was no spinclass-native undo for a closed session;
`sc fork`/`sc start` only build from *live* refs.

This was motivated by a real incident: an agent used `close-child-session` to
reap a spawned worker without asking, deleting its worktree and branch. The
user recovered it manually via git, then asked for this as a first-class
feature so the next occurrence doesn't require manual git surgery.

## Interface

`sc close` and `close-child-session` (both funnel through
`close.RunResolved`) now best-effort resolve the branch's tip
(`git.RevParse(wtPath, "HEAD")`) immediately before force-deleting the
worktree/branch, and record it as `State.DeletedSHA` on the tombstone
(`session.Tombstone`'s new `sha` parameter). A missing `DeletedSHA` — a
tombstone written before this field existed, or a session closed outside
spinclass entirely (a dangling index entry that never went through
`Tombstone`) — degrades to a clean refusal rather than a guess.

`sc resurrect <target> [--new-branch <name>]` (CLI and MCP tool) recreates a
closed session's worktree and branch from that captured commit:

- `target` — the worktree directory name or `<repo>/<branch>` session key,
  the same grammar `sc close`/`sc resume` accept (`sc list --closed` shows
  closed sessions and their keys).
- `--new-branch` — recreate under a different branch name instead of the
  original (e.g. if something else already created a branch with that name
  since the original was deleted). Validated the same way `sc fork`'s
  `--new-branch` is (must not contain `.`, the room-JID separator).

On success it writes fresh, `inactive` session state carrying the original
description and spawn-lineage (`SpawnedBy`) forward, so `sc list` sees it
again immediately. It does **not** attach — run `sc resume <target>`
afterward, reusing that already-hardened path unmodified.

Exposed as both a CLI subcommand and an MCP tool (`resurrect`), unlike
CLI-only `sc fork`: this is a restorative operation, and the motivating
incident was an agent's mistake via an MCP tool (`close-child-session`) — the
fix should let an agent that makes the same mistake self-correct via an MCP
call, not require the human to drop into a terminal. Unlike
`close-child-session`'s `authorizeChildReap`, there is no spawned-lineage
authorization gate: recovering a closed session is not the privileged action
tearing one down is.

Three refusal conditions, each with a message naming the fix rather than a
raw git error:

1. **Target is not a closed session** (`!st.IsTombstone()`) — nothing to
   resurrect.
2. **No captured commit** (`DeletedSHA == ""`) — predates this feature, or
   was closed outside spinclass. Points at `git reflog` in the repo as the
   manual fallback.
3. **Commit unreachable** (`!git.CommitExists(repoPath, sha)`) — most likely
   locally garbage collected. spinclass does not control git gc timing, so
   this is checked explicitly rather than surfacing `git worktree add`'s own
   opaque failure.

## Examples

    $ sc close --force feature-x
    ok 1 - close feature-x

    $ sc list --closed
    ID                    STATUS         AGE        DESCRIPTION
    repo/feature-x        ● (tombstone)  just now   working on the thing

    $ sc resurrect repo/feature-x
    Preparing worktree (new branch 'feature-x')
    HEAD is now at 48aa0b3 add marker
    ok 1 - resurrect repo/feature-x /path/to/repo/.worktrees/feature-x

    $ sc list
    ID                    STATUS   AGE        DESCRIPTION
    repo/feature-x        ●        just now   working on the thing

    $ sc resume repo/feature-x   # attach, same as any other session

Recreating under a different name, when the original branch name is taken:

    $ sc resurrect repo/feature-x --new-branch feature-x-reborn

A tombstone with no captured commit (predates this feature, or closed
outside spinclass):

    $ sc resurrect repo/legacy-session
    error: repo/legacy-session has no captured commit (closed before `sc
    resurrect` support, or closed outside spinclass); recover manually via
    `git reflog` in /path/to/repo

## Limitations

- `sc clean`'s merged-worktree removal does **not** capture a SHA. That
  content already lives in the default branch by construction (`sc clean`
  only removes worktrees whose branch has zero commits ahead), so the
  natural recovery there is re-forking from the default branch, not literal
  resurrection.
- Resurrection is only possible while the tombstone still exists — default
  30-day retention, `sc clean`'s `GCTombstones` — **and** the commit object
  hasn't been locally garbage collected. spinclass has no control over
  either window; `sc resurrect` detects and reports both failure modes
  cleanly rather than pretending to always work.
- No auto-attach. `sc resurrect` only recreates the worktree/branch and
  session state; attaching is a separate `sc resume` call.

## More Information

FDR 0001 (worktree-local session state) defines the tombstone mechanism this
builds on. FDR 0006 (spawn sibling sessions) defines the `close-child-session`
reap path this is the undo of.
