# Implicit Sessions — Design

**Issue:** [#118 — Support implicit sessions started from repo main worktrees](https://github.com/amarbel-llc/spinclass/issues/118)
**Date:** 2026-06-09
**Status:** Design approved; ready for implementation plan

## Problem

A spinclass session exists today only for worktrees spinclass created under
`.worktrees/<name>`. An agent (or human) working directly in a repo's **main
checkout** has no session identity: it is invisible to `sc list`, has no
session key, and every session-scoped MCP tool refuses to operate
(`update-this-session-description`, `merge-this-session`, chat send/receive).

Starting an agent in the root is a **deliberate** choice. spinclass should
treat that main checkout as a first-class session, not a degraded one — full
parity across the session-scoped tools.

## Goals

- A main-checkout agent gets a real session identity with zero ceremony.
- Full parity: `merge`, `check`, `update-description`, `close`, `clean`,
  `fork`, and cross-session chat all work.
- Multiple concurrent agents attached to the **same** main checkout each get a
  distinct, independently addressable session.
- No git-visible state; no disturbance to the existing (daily-driver) worktree
  session path.

## Non-goals

- Changing worktree-session storage or semantics (explicitly out of scope —
  YAGNI; avoid regressing the path used every day).
- Rebase/ff-merge semantics from a main checkout (see Merge below — merge means
  hook-then-push, there is nothing to merge).

## Model

### Identity

An **implicit session** is keyed:

```
<repo-dirname>/<default-branch>-<rand>
```

e.g. `spinclass/master-a3f9b2c1`, where `<rand> = sha256(session_id)[:8]` and
`session_id` is the Claude Code hook payload's session id.

- Globally unique → no collision with worktree session keys, and no collision
  between concurrent agents on the same main checkout (each Claude session has
  its own `session_id`).
- `session_id` is stable across a session's lifetime (same value at
  `SessionStart` and `SessionEnd`, and across `/clear`, `compact`, and
  `--resume`), so create and teardown derive the same `<rand>`.

### State storage (Approach A — parallel, minimal disturbance)

- **Worktree-local file:** `<checkout>/.spinclass/state-<rand>.json`. One file
  per session — no shared roster, so reads/writes are naturally exclusive with
  **no locking**.
- **Central index symlink:** `index/<hash>.json` where
  `<hash> = sha256(<state-file-abs-path>)[:8]`, reusing the existing
  symlink-to-worktree-local mechanism (`session.writeIndexSymlink`). `ListAll`
  already globs the whole index dir, so implicit `state-<rand>.json` siblings
  are discovered naturally.
- `.spinclass/` is already in the default git-excludes
  (`sweatfile.GetDefault`), so nothing becomes git-visible.

The existing one-file-per-worktree assumption in `ListAll` / `Remove` /
`Tombstone` is preserved for worktree sessions. Implicit sessions add
`state-<rand>.json` siblings alongside the worktree path's `state.json`.

### Schema

Reuse `session.State` (`internal/session/session.go`) plus one discriminator:

```jsonc
{
  "kind": "implicit",          // NEW; omitempty. Absent ⇒ worktree session (existing default).
  "pid": 12345,
  "state": "active",
  "repo_path": "/abs/repo",
  "worktree_path": "/abs/repo",   // the main checkout (== repo root)
  "branch": "master",             // the default branch
  "session_key": "spinclass/master-a3f9b2c1",
  "description": "...",
  "started_at": "...",
  "env": { "SPINCLASS_SESSION_ID": "spinclass/master-a3f9b2c1" }
  // ... existing fields unchanged
}
```

No new container/roster type. `sc list` renders a `main` marker for
`kind: implicit` rows so they are not mistaken for mergeable worktree sessions.

## Lifecycle

Driven by **net-new Claude Code plugin hooks** (`SessionStart`, `SessionEnd`)
added to the generated plugin manifest and the `hooks.Run` dispatcher
(`internal/hooks/`). The hooks call thin spinclass commands that own all
state-file logic; the hook is purely the lifecycle signal.

Both hook events are confirmed in the official Claude Code hooks reference
(<https://code.claude.com/docs/en/hooks>):

- `SessionStart` matchers: `startup`, `resume`, `clear`, `compact`. Input
  carries `session_id`, `cwd`, `source`. Can inject `additionalContext`.
- `SessionEnd` reasons: `clear`, `resume`, `logout`, `prompt_input_exit`,
  `bypass_permissions_disabled`, `other`. **No decision control** (cannot block
  termination). **Default 1.5s timeout.**

### `SessionStart` → `sc hook session-start`

1. Read `session_id`, `cwd`, `source` from hook JSON (stdin).
2. **Gate:** materialize only if `cwd` is **not** inside a `.worktrees/`
   worktree (real worktree sessions already have state) **and** `cwd` is a git
   repo checked out on its default branch. Otherwise no-op, exit 0. This gate
   is what makes the feature fire only for deliberate main-checkout agents.
3. **Orphan sweep:** glob `<cwd>/.spinclass/state-*.json`; delete any whose
   recorded PID is dead (cheap backstop for missed `SessionEnd`).
4. **Upsert** `state-<rand>.json` (`<rand> = sha256(session_id)[:8]`) + central
   symlink. Idempotent — `/clear` and `compact` re-fire `SessionStart` with the
   same `session_id`, so the upsert overwrites the same file harmlessly.
5. Optionally emit `additionalContext` advertising the session key so the agent
   knows its own chat address.

### `SessionEnd` → `sc hook session-end`

1. Read `session_id`, `cwd`; derive `<rand>`.
2. **Hard delete** `state-<rand>.json` + its central symlink (best-effort,
   tolerate not-exist). A local unlink — trivially within the 1.5s budget.

### Orphan safety net

`SessionEnd` is not guaranteed (hard crash, `kill -9`, or the 1.5s timeout can
skip it), so a leaked file would otherwise show as a phantom `active` session.
Two backstops:

1. **PID-liveness** — `sc list` / `ListAll` already compute liveness, so a
   dead-PID implicit file renders `inactive`, never a phantom `active`.
2. **`SessionStart` sweep** (step 3) — physically removes dead-PID siblings the
   next time any agent starts in that checkout.

`sc clean` also reaps dead implicit sessions (reusing existing reaping; removes
state only, never the checkout).

## Tool semantics

The `kind: "implicit"` discriminator drives the divergences. Everything else
inherits existing behavior.

- **`merge-this-session` / `sc merge`:** materially different path — **no
  rebase, no ff-merge.** From a main checkout the work is already on the
  default branch, so merge means *verify then publish*: run `[hooks].pre-merge`
  against HEAD (in the isolated build worktree, unchanged) and, if it passes,
  **push the default branch.** `merge.PrepareMerge`/`FinishMerge` branch on
  `kind`: implicit skips rebase + sha-pin + `git merge --ff-only` and
  substitutes hook→push. The pre-merge attestation gate still applies. The
  default-branch push is surfaced explicitly in TAP output — never silent
  (it is an outward-facing, hard-to-reverse action).
- **`merge-this-session-async` + job tools:** unchanged — they wrap the same
  merge function, which now has the implicit branch.
- **`check-this-session` / `sc check`:** already "run the hook against HEAD";
  works as-is.
- **`update-this-session-description`:** writes `description` into
  `state-<rand>.json`. Works once the session exists.
- **`close` / `sc close`:** for `kind: implicit`, **drop state only** (delete
  `state-<rand>.json` + symlink); **never** remove the checkout. Guarded
  explicitly so the `git worktree remove` path cannot fire on a main checkout.
- **`clean`:** reaps dead-PID implicit sessions (state-file removal only).
- **`fork`:** existing `Fork()` (branch from current worktree) already works
  from a main checkout — no change.
- **Chat (`chat-send` / `chat-read` / `chat-list-sessions`):** works for free.
  `currentSessionKey()` resolves via the implicit `state-<rand>.json` (or the
  exported `$SPINCLASS_SESSION_ID`), giving a unique send-as/receive-on key.
  This subsumes the chat-decoupling concern in #118 (and the duplicate #128,
  closed). `chat-list-sessions` shows implicit sessions as addressable peers.

## Error handling

- `SessionStart` gate fails (not a repo, detached HEAD, inside `.worktrees/`)
  → silent no-op, exit 0. Never block session startup over a non-applicable
  cwd.
- `state-<rand>.json` write fails → log to stderr, exit 0. Degrades to today's
  behavior (session-scoped tools error "not inside a worktree session") — no
  worse than status quo.
- `SessionEnd` delete fails / skipped → orphan reaped by PID-liveness + next
  `SessionStart` sweep. Designed-for, not exceptional.
- `merge` push fails (no upstream, rejected, network) → TAP `not ok` with git
  stderr; the hook already passed, so re-running push is safe/idempotent.
- Concurrent agents, same checkout → distinct `<rand>` files, zero shared
  mutable state, no lock.

## Rollback

- **Dual-architecture:** implicit sessions are purely additive; worktree
  sessions (the daily driver) are untouched.
- **Rollback procedure:** sweatfile knob `[hooks].disable-implicit-sessions =
  true` (cascades like other `[hooks]` flags) makes `sc hook session-start` a
  no-op. Single config change, no revert. Plugin hooks stay registered but do
  nothing.
- **Promotion criteria:** remove the knob (make implicit always-on) after ~2
  weeks of no orphan-leak reports and no `sc list` phantom-active complaints
  across the repos it runs in.

## Testing

- **Unit:** `<rand>` derivation stable across SessionStart/SessionEnd for one
  `session_id`; gate logic (worktree vs main-checkout vs non-repo); orphan
  sweep deletes dead-PID only; `kind` discriminator drives close/merge
  branches.
- **bats integration:** `sc hook session-start` (fake hook JSON on stdin)
  creates file + symlink; `session-end` deletes it; dead-PID file gets swept;
  `merge` from a main checkout runs hook + push (against a local bare upstream)
  without rebase; `close` on implicit never touches the checkout;
  `chat-list-sessions` shows the implicit peer.

## Tuning levers

Decisions correct for now whose right value depends on real usage:

- **`<rand>` width — `sha256(session_id)[:8]` (8 bytes):** collision-safe for
  realistic concurrent counts. Widen only if a collision is observed.
- **Orphan-sweep trigger — every `SessionStart`:** cheap (one glob + PID
  checks). If a checkout accumulates many `.spinclass/` files and sweep cost
  shows up, gate to "sweep only if N+ state files present."
- **`disable-implicit-sessions` default — off (feature on):** the rollback
  lever. Flip to disabled-by-default if early usage is rocky.
- **`SessionEnd` timeout headroom — rely on default 1.5s:** the delete is a
  local unlink, well within budget. Raise via per-hook `timeout` only if slow
  filesystems cause missed deletes.

## Relationship to other issues

- **#110** (recipient validation for directed `chat-send`): if non-spinclass /
  implicit participants become addressable, #110's validation predicate must
  account for them. Co-design.
- **#128** (decouple chat from session-key resolution): closed as duplicate;
  implicit sessions give main-checkout agents a real unique chat key, which is
  what #128 asked for.

## Key file touch-points (orientation, not a plan)

- `internal/session/session.go` — `State.Kind` field; `ListAll` rendering of
  implicit rows; `Remove` close-state-only guard.
- `internal/hooks/hooks.go` + `cmd/spinclass/commands_hooks.go` —
  `SessionStart`/`SessionEnd` dispatch; `sc hook session-start|session-end`.
- plugin manifest generation (`generate-artifacts`) — register the two new
  hook events.
- `internal/merge/` — `kind`-conditional hook-then-push path.
- `internal/sweatfile/` — `[hooks].disable-implicit-sessions`.
- `cmd/spinclass/commands_mcp_only.go` — `currentSessionKey` already resolves
  via state file once it exists; verify implicit path.
