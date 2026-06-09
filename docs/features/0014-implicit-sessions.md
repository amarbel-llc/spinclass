---
status: experimental
date: 2026-06-09
promotion-criteria: |
  experimental -> testing: a real Claude agent started in a repo's main
  checkout (a) appears in `sc list` with a `main` marker, (b) is addressable
  via cross-session chat (`chat-list-sessions` shows it; a directed `chat-send`
  reaches it), and (c) `merge-this-session` runs the pre-merge hook against HEAD
  and pushes the default branch with the push as a distinct TAP step. Plus one
  observation that `[hooks].disable-implicit-sessions = true` makes SessionStart
  a no-op.
  testing -> accepted: ~2 weeks of real main-checkout sessions across the repos
  this runs in with no orphan-leak reports (no phantom `active` rows in
  `sc list` from missed `SessionEnd`), no `<rand>` collision observed, and no
  need to set the disable knob anywhere. Remove the knob (make implicit
  always-on) as the final step.
---

# Implicit sessions (main-checkout agents)

## Problem Statement

A spinclass session existed only for worktrees spinclass created under
`.worktrees/<name>`. An agent (or human) working directly in a repo's **main
checkout** had no session identity: it was invisible to `sc list`, had no
session key, and every session-scoped MCP tool refused to operate
(`update-this-session-description`, `merge-this-session`, chat send/receive).
Starting an agent in the root is a deliberate choice; spinclass should treat
that checkout as a first-class session, not a degraded one.

## Interface

An **implicit session** is a spinclass session materialized for an agent
attached to a repo's main checkout (a git repo on its default branch, *not* a
`.worktrees/` worktree). It is created and torn down automatically by Claude
Code lifecycle hooks — no `sc` command to run.

### Identity

Session key: `<repo-dirname>/<default-branch>-<rand>`, where
`<rand> = sha256(session_id)[:8]` and `session_id` is the Claude Code hook
payload's session id (e.g. `spinclass/master-a3f9b2c1`). The `session_id` is
stable across a session's lifetime (same value at `SessionStart` and
`SessionEnd`, and across `/clear`, `compact`, and `--resume`), so create and
teardown derive the same `<rand>`. The key is globally unique, so it collides
neither with worktree session keys nor with concurrent agents in the same
checkout (each Claude session has its own `session_id`).

`session.State` carries a `Kind string` field (`json:"kind,omitempty"`);
`session.KindImplicit = "implicit"` discriminates. Absent `Kind` ⇒ a worktree
session (the existing default — worktree `state.json` files are unaffected).

### Storage

Worktree-local, one file per session:
`<checkout>/.spinclass/state-<rand>.json`, plus a central index symlink
(`index/<hash>.json`, `<hash> = sha256(<state-file-abs-path>)[:8]`) reusing the
existing symlink-to-worktree-local mechanism. `ListAll` globs the whole index
dir, so implicit siblings are discovered naturally. `.spinclass/` is already in
the default git-excludes, so nothing becomes git-visible. One file per session
means reads/writes are naturally exclusive — **no locking**. Functions:
`session.WriteImplicit(s, randID)`, `session.RemoveImplicit(checkout, randID)`,
`session.SweepDeadImplicit(checkout)` (dead-PID orphan reaper), and
`session.FindImplicitAtCwd(cwd) (*State, randID, error)` (the live-session
resolver used by merge/attestation/chat).

### Lifecycle (Claude Code plugin hooks)

Two net-new events in `hooks/hooks.json` (the hand-maintained plugin manifest),
dispatched by `hooks.Run`:

- **`SessionStart`** (timeout 10s) → `runSessionStart`. Gates on, in order:
  (1) cwd is **not** inside a `.worktrees/` worktree; (2) the
  `[hooks].disable-implicit-sessions` knob is unset/false; (3) cwd is a git repo
  whose checkout root == cwd and which is on its default branch. If any gate
  fails it is a silent no-op (exit 0 — never block session startup). Otherwise
  it sweeps dead-PID orphans (`SweepDeadImplicit`) then upserts
  `state-<rand>.json` (PID = `os.Getppid()`, best-effort). Fires on
  startup/resume/clear/compact; re-fires are idempotent upserts on the same
  `session_id`.
- **`SessionEnd`** (timeout 5s) → `runSessionEnd`. Hard-deletes the per-rand
  state file + its index symlink (best-effort, tolerates not-exist).

**Orphan safety net.** `SessionEnd` is not guaranteed (hard crash, `kill -9`,
or a timeout can skip it). Two backstops prevent a phantom `active` row:
PID-liveness — `sc list` computes liveness, so a dead-PID implicit file renders
`inactive`, never `active` — and the `SessionStart` sweep, which physically
removes dead-PID siblings the next time any agent starts in that checkout.

### Tool semantics

The `Kind == implicit` discriminator drives the few divergences; everything
else inherits existing behavior.

- **`merge-this-session` / `sc merge`** — materially different path:
  **no rebase, no ff-merge.** From a main checkout the work is already on the
  default branch, so merge means *verify then publish*: `merge.MergeImplicit`
  runs `[hooks].pre-merge` against HEAD (in the isolated build worktree) and, if
  it passes, runs `git push` of the default branch as a **distinct TAP step**
  (never silent — an outward-facing, hard-to-reverse action). Routed in
  `handleMergeThisSession`, `handleMergeThisSessionAsync`, and `merge.Run` (the
  `sc merge` CLI path) by detecting a live implicit session via
  `FindImplicitAtCwd`. The async twin and job tools wrap the same function. The
  MCP handlers enforce the implicit attestation gate; the CLI path is gate-free
  by design.
- **`update-this-session-description`** — writes `description` into
  `state-<rand>.json`; works once the session exists.
- **`close` / `sc close`** — for an implicit session, **drops state only**
  (`RemoveImplicit`): deletes `state-<rand>.json` + symlink, **never** removes
  the checkout, and runs **no nix gc**.
- **`clean`** — reaps dead-PID implicit sessions (state-file removal only).
- **`sc list`** — marks live implicit sessions `main` in text output;
  `session.ListRow` gained an omitempty `Kind` field so the marker survives
  `--format json` and the remote wire format.
- **Chat (`chat-send` / `chat-read` / `chat-list-sessions`)** —
  `currentSessionKey` falls back to `FindImplicitAtCwd` when not in a worktree
  and `$SPINCLASS_SESSION_ID` is unset, so chat works from a main checkout with
  a unique send-as/receive-on key.

### Attestation

`attestation.RecordImplicit` / `CheckImplicit` operate on the per-rand state
file (via `FindImplicitAtCwd` + `WriteImplicit`), so the pre-merge
skill-attestation gate applies to implicit sessions. The MCP merge handlers call
`enforceAttestationImplicit` → `CheckImplicit`; `handleNothingButTheTruth`
admits a live implicit session when recording the attestation.

### Rollback lever

`[hooks].disable-implicit-sessions = true` (sweatfile cascade, scalar override,
default false) makes `runSessionStart` a no-op. Single config change, no revert;
the plugin hooks stay registered but do nothing. The feature is otherwise purely
additive — worktree sessions (the daily driver) are untouched.

## Examples

A Claude agent starts in a repo's main checkout; the `SessionStart` hook
materializes the session, which then shows up in `sc list`:

    $ sc list
    KEY                          STATE     ...
    spinclass/master-a3f9b2c1    active    main
    spinclass/sleek-locust       active

Merge from the main checkout — hook then push, no rebase/ff:

    # (invoked as the merge-this-session MCP tool, or `sc merge`)
    ok 1 - pre-merge hook
    ok 2 - push master

Disable the feature for a repo (rollback):

    [hooks]
    disable-implicit-sessions = true

## Limitations

- **Build-worktree placement (#130).** The isolated pre-merge build worktree is
  created at `filepath.Dir(checkout)`. For a worktree session that is inside
  `.worktrees/`; for an implicit session `checkout` == the repo root, so the
  build worktree lands in the repo's **parent** dir (e.g. a repo at
  `~/eng/repos/myrepo` gets a build worktree at
  `~/eng/repos/.merge-master-<sha>-<pid>`). Functional but an out-of-repo
  placement quirk; tracked as a followup.
- **`check` parity (#132).** `merge` routes implicit sessions to the
  hook-then-push path, but `check-this-session` / `sc check` do not yet detect
  an implicit session — a follow-up.
- **Best-effort PID.** The recorded PID is `os.Getppid()` (the parent of the
  short-lived hook subprocess). It is *not* empirically verified to be the
  stable Claude process versus a transient shell wrapper, so PID-liveness is a
  backstop reaper only — the `SessionEnd` delete and the `SessionStart` sweep are
  the primary reapers.
- **Chat resolution with shared checkouts.** Multiple agents sharing one
  checkout each get a distinct `state-<rand>.json`, but `currentSessionKey`
  resolves via `FindImplicitAtCwd`, which returns the **first live** implicit key
  (the `serve` process cannot know its own `session_id`). Broadcasts are
  unaffected; a directed `chat-send` from such a checkout may resolve to a
  sibling agent's key.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| `<rand>` width | `sha256(session_id)[:8]` (8 hex chars) | collision-safe for realistic concurrent counts | a `<rand>` collision is observed |
| orphan-sweep trigger | every `SessionStart` | cheap (one glob + PID checks) | a checkout accumulates many `.spinclass/` files and sweep cost shows up in profiling — then gate to "sweep only if N+ state files present" |
| `disable-implicit-sessions` default | off (feature on) | the rollback lever | flip to disabled-by-default if early usage is rocky |
| `SessionEnd` timeout | 5s (manifest), Claude default budget 1.5s | the delete is a local unlink, well within budget | slow filesystems cause missed deletes — raise per-hook |

## More Information

- Issue: [#118](https://github.com/amarbel-llc/spinclass/issues/118).
  Followups: [#130](https://github.com/amarbel-llc/spinclass/issues/130)
  (build-worktree placement),
  [#132](https://github.com/amarbel-llc/spinclass/issues/132) (check parity).
  [#128](https://github.com/amarbel-llc/spinclass/issues/128) (decouple chat
  from session-key resolution) was closed as a duplicate — implicit sessions
  give main-checkout agents the unique chat key it asked for.
- Design doc: `docs/plans/2026-06-09-implicit-sessions-design.md`;
  implementation plan: `docs/plans/2026-06-09-implicit-sessions-plan.md`.
- FDR 0013 — isolated build worktree for pre-merge hooks (the hook isolation
  `MergeImplicit` reuses; #130 is a placement quirk of that mechanism for
  implicit sessions).
- `internal/session/session.go` (`Kind`, `WriteImplicit`/`RemoveImplicit`/
  `SweepDeadImplicit`/`FindImplicitAtCwd`), `internal/hooks/hooks.go`
  (`runSessionStart`/`runSessionEnd`), `internal/merge/merge.go`
  (`MergeImplicit`), `internal/attestation/attestation.go`
  (`RecordImplicit`/`CheckImplicit`), `internal/close/close.go`,
  `cmd/spinclass/commands_mcp_only.go` (`currentSessionKey` fallback, merge
  routing), `cmd/spinclass/commands_query.go` (`main` marker), `hooks/hooks.json`
  (the two event blocks).
- `spinclass-sweatfile(5)` `[hooks]` § `disable-implicit-sessions`.
