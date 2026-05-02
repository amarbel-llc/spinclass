---
status: exploring
date: 2026-05-02
promotion-criteria: |
  Promote to `proposed` once the open questions below have decisions:
  the "healthy" gate definition for the synchronous kick-off, the
  initial-context format passed to the worker, the addressing scheme
  beyond v1's path-based form, and the visibility of spawned worker
  sessions in `sc list` / `sc resume`. The motivating cross-repo
  driver/worker pattern (see Problem Statement) must remain expressible
  end-to-end without manual terminal-juggling.
---

# Spawn sibling-repo sessions

## Problem Statement

A cross-repo driver/worker coordination pattern has emerged organically
in real use: a "driver" Claude Code session running in repo A files
cross-repo issues against repo B, sets `Monitor` watches on those
issues' state changes, and continues working on what it can while
waiting for issue closures to fire those monitors. The protocol is
durable (GitHub issues persist), survives crashes, and works across
machines — it's a good shape.

The dotted line in this otherwise-clean workflow is **starting the
worker sessions in repo B**. Today the user has to open a new terminal,
`cd` into the sibling repo, and run `sc start ...` themselves; the
driver session has no way to spawn it. As a concrete data point,
today's dodder→madder migration thread had a 7-hour driver session in
`dodder/deft-sequoia` that depended on two worker sessions
(`madder/rare-buckeye` and `madder/smart-banyan`) which the user
started manually ~1.3 hours into the driver's run. The driver filed
nine cross-repo issues and set seven monitors — automation around the
issue protocol was already excellent — but the spawn step was the only
manual action.

This feature gives the driver session a tool to spawn worker sessions
in a sibling repo, leaving everything else (issue filing, monitoring,
merge cycles) unchanged.

## Locked-in design

These pieces are settled.

### Coordination protocol — unchanged

The driver does **not** wait for the worker to complete. The worker
runs autonomously inside its own session; the driver continues with
unblocked work and reacts to GitHub issue state changes via the
existing `Monitor` mechanism. Communication between the two sessions
is exclusively through GitHub issues. The new tool spawns the worker
and returns; nothing more.

### Addressing — path-based for v1

The new tool takes the sibling repo as a path, either relative to the
current worktree (`../madder`) or absolute (`/home/sasha/eng/repos/madder`).
Path is canonicalized via `worktree.DetectRepo` before use, so a path
inside an existing worktree resolves to the parent repo.

This is brittle (laptop-vs-desktop layouts, monorepo refactors) but
sufficient for v1 — the immediate need is a single-machine workflow
where sibling repos live next to each other under a known root. A
sweatfile-driven alias scheme is one of the open questions below.

### Substrate — zmx

zmx is the multiplexer (not tmux). Spinclass already exports
`SPINCLASS_GROUP` to session entrypoints, but does not directly invoke
zmx — the user's `[session-entry].start` does the attach. For the
spawn tool, spinclass invokes zmx directly:

```
zmx run <session-id> <argv-of-sibling-session-entrypoint>
```

`zmx run` creates the session non-interactively and runs the command,
which is exactly the "start it but don't grab my terminal" semantics
we want. The driver session's TTY is not affected; the worker runs in
its own zmx session that the user can later `zmx attach` to.

### Autonomy — interactive, not one-shot

The worker session is a normal interactive Claude Code session. It
runs the sibling repo's standard `[session-entry].start` command (the
same thing `sc start` would have launched). The user can attach to
it at any time to inspect or steer; nothing about the session is
single-prompt or read-only.

This rules out, for v1, the "pass `claude -p '<prompt>'` and wait for
exit" autonomy mode. The async-via-issues protocol gives us the right
shape without forcing the worker into prompt-only mode.

### Kick-off — synchronous until healthy, then async

The driver tool blocks until the spawned worker reaches a "healthy"
state (definition TBD — see open questions), then returns. After
return the driver continues; the worker continues independently.
Failure during the synchronous window surfaces as a tool error to the
driver. Failure after handoff (worker crashes mid-task) surfaces only
through GitHub issue state — i.e. the monitor never fires, and the
user notices via the "I expected this to be done by now" instinct.

### Worker initial context

The worker session is created with the same shape `sc start-gh_issue
<N>` produces today: the issue body is fetched and stitched into the
session's initial context file. The driver tool's required input is
therefore a **sibling repo path + an issue number** (in the sibling
repo); the worker's first message is the issue context, which the
worker then attempts to address autonomously with periodic merge +
issue-close cycles.

This reuses an existing, working code path. New context shapes (e.g.
"a custom prompt instead of an issue", "multiple issues stitched
together") are deferred.

## Sketch — interface

```
sc spawn-sibling <path> <issue-number> [--description "<text>"]
```

- `<path>`: relative or absolute path to the sibling repo (NOT a
  worktree path; the tool resolves to the repo root).
- `<issue-number>`: integer; the issue's body becomes the worker's
  initial context, exactly as `sc start-gh_issue` does.
- `--description`: optional override for the worker's session
  description; defaults to a derivation from the issue title.

The corresponding MCP tool is `spawn-sibling-session` so a driver
Claude Code session can call it directly without shelling out.

The tool's effects:

1. Resolve `<path>` to a repo root. Error if not a git repo, not on
   disk, or pointing at a worktree-of-a-worktree (forbidden — siblings
   are repos, not nested worktrees).
2. Run the same sibling-repo logic `sc start-gh_issue` would, ending
   with a worktree on disk and the issue context written.
3. Spawn the worker via `zmx run <session-id> <sibling-entrypoint>`.
4. Synchronously poll until "healthy" (see open questions); error out
   if the deadline elapses.
5. Return `{session_id, worktree_path, zmx_session, issue_url}` to
   the driver.

## Open questions

### "Healthy" definition for the sync gate

Candidates for what "healthy" means:

- **zmx session exists.** Cheapest probe: `zmx list` lists the new
  session name. Confirms the daemon picked up the spawn but says
  nothing about the worker actually running anything.
- **Entrypoint command's PID is alive.** Slightly deeper: confirm the
  session has a live process. Still doesn't confirm Claude Code is
  responding.
- **Claude Code reports ready.** Deepest: tail the session's output
  for the Claude prompt-ready marker, or use a Claude Code MCP-side
  health endpoint (does one exist?). Most accurate but most coupled to
  Claude Code internals.

The lean is the middle option (PID alive) for v1: it's a real signal
without requiring Claude Code internals, and the zmx-only check is too
shallow for a "healthy" claim.

### Addressing scheme beyond v1

After path-based works, candidates:

- **Sweatfile aliases.**
  ```toml
  [siblings]
  madder = "/home/sasha/eng/repos/madder"
  dodder = "/home/sasha/eng/repos/dodder"
  ```
  Cascade-merged like the rest of the sweatfile, easy to override.
- **Project-name resolution.** `sc spawn-sibling madder #106` resolves
  via a registry under `~/eng/repos/<name>` or similar.
- **Existing `worktree.ResolvePath` overload.** Re-use the path
  resolver that `sc start` already uses, just with the "must point at
  a different repo" constraint.

Lean: sweatfile aliases when the path-based v1 starts hurting. Project
resolution feels too magical.

### Initial-context format

`sc start-gh_issue` only handles a single issue body. Real cross-repo
work often needs more context: a thread of related issues, a design
doc URL, a snippet from the driver's own current state. Rich-context
extensions:

- **Multiple issue numbers.** `sc spawn-sibling <path> --issues
  106,111,113`.
- **Custom prompt blob.** A driver-supplied initial message, no GH
  involvement.
- **Reference to a driver-side scratch file.** The driver writes a
  context file in its own worktree, the spawned worker session
  receives a pointer to it (probably copied into the worker's context
  area).

Lean: single-issue v1 (which is what `sc start-gh_issue` already
supports), revisit when a real shape becomes load-bearing.

### Visibility of spawned workers

`sc list` already enumerates active spinclass sessions across all
known projects. A spawned worker shows up in `sc list` automatically
since it's a real spinclass session. Open questions:

- **Should `sc list` highlight the parent driver session?** Useful for
  remembering "why does this worker exist?". Could be a `parent_pid`
  or `spawned_by_session_id` field in the worker's session state.
- **Does `sc list --tree` make sense?** Driver → worker trees might be
  useful as the spawn pattern matures. Out of scope for v1.

Lean: nothing special for v1 — the worker is just a session, no
parent-tracking metadata. Add tree-style display in a follow-up if
the pattern proves load-bearing.

### Failure modes after handoff

The "synchronous until healthy, then async" model means once the tool
returns, all worker failures are invisible to the driver until they
manifest as "the issue I'm watching never closes." Open questions:

- Should the worker session emit a heartbeat into a known location so
  the driver can notice silent deaths? Probably no — it's complexity
  for a problem that already has a coarse signal (GitHub issue state).
- Should the driver tool record its spawn somewhere durable so a
  later command can ask "what workers did I spawn?". Maybe — a
  small spawn-log per driver session would be cheap and useful for
  forensics. Worth considering but out of scope for v1.

## Limitations

- **Path-based addressing only.** A driver on a machine where the
  sibling repo lives in a non-conventional location must give the
  full path. Sweatfile aliases or registry-based resolution is a
  follow-up.
- **zmx-required.** v1 hard-codes zmx. Users without zmx installed (or
  using tmux instead) get an error at the spawn step. A multiplexer
  abstraction layer is a possible v2.
- **No cross-machine spawning.** The new tool only spawns workers on
  the same host as the driver. Spawning a worker on a remote dev box
  via SSH is out of scope (though zmx's first-class SSH workflow
  makes it a plausible v2).
- **No worker-status feedback to driver.** Once the sync window
  closes, the only signal back to the driver is GitHub issue state.
  A worker that silently crashes 30 seconds in is invisible until the
  user notices "the issue I expected to close never did."
- **No spawn cleanup on driver close.** If the driver session is
  closed mid-flight, spawned workers continue running. They're real
  sessions; the user manages them via `sc list` / `sc close` like any
  other session.
- **Single-issue context only.** The worker is initialized with one
  GitHub issue body, the same surface `sc start-gh_issue` already
  exposes. Multi-issue or custom-prompt initial contexts are
  deferred.

## More Information

- Origin observation: today's dodder→madder migration thread —
  driver session
  `f94e6853-4ad1-4ec4-9bb3-cfea1e47af1c`
  (`dodder/deft-sequoia`, ~7h, 2333 messages); workers
  `3b5c4231-f85c-4f57-8e9e-3b4efc078896`
  (`madder/rare-buckeye`) and
  `2c1feec9-5669-4b7f-8276-54d0d11d3995`
  (`madder/smart-banyan`).
- FDR 0004 (`docs/features/0004-direnv-template-plugins.md`) — same
  "spinclass exposes a plugin point" pattern; this FDR is closer in
  shape to a built-in tool that wraps an existing
  (`sc start-gh_issue`) code path.
- `internal/sweatfile/sweatfile.go` `defaultStartCommands()` — the
  baked-in `gh_issue` start command this design layers on top of.
- zmx README (https://zmx.sh / `~/eng/repos/zmx/README.md`) — the
  multiplexer's interface; `zmx run <name> <cmd>` is the
  non-interactive spawn primitive this design relies on.
- Today's `Monitor` usage in the dodder driver session (issues #105,
  #106, #114) — the existing async-via-issues coordination protocol
  this feature does **not** change.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
