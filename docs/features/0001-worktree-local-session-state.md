---
status: proposed
date: 2026-04-29
promotion-criteria: |
  - Existing sessions in ~/.local/state/spinclass/sessions/ migrate cleanly to
    the new layout on first invocation after upgrade.
  - sc list, sc close, sc resume, and tab completion all operate end-to-end via
    the central symlink index.
  - At least one detach → reattach → close cycle observed where the state file
    correctly reflects running-attached, running-detached, and dead.
  - on-attach and on-detach hooks fire on every transition in that cycle.
---

# Worktree-local session state with attach/detach lifecycle

## Problem Statement

Spinclass currently tracks sessions in two disconnected places: the worktree
on disk at `<repo>/.worktrees/<name>` and a JSON state file at
`~/.local/state/spinclass/sessions/<sha256(repo+"/"+branch)[:8]>-state.json`.
The two drift out of sync — orphaned worktrees with no state file are
invisible to `sc close`, and abandoned state files outlive removed worktrees
until `sc clean` reaps them. The `active` / `inactive` distinction also
relies on `kill -0` against the spinclass-spawned PID, which collapses
"multiplexer running but user detached" into "process dead." Recent
unification work in commit 03a9265 surfaced these tensions concretely
(see issue #40, where `sc close <id>` regressed).

## Interface

### State file layout

Each session's state is owned by its worktree:

```
<repo>/.worktrees/<name>/.spinclass/state.json
```

The JSON schema matches the current `session.State` struct (PID, repo path,
worktree path, branch, session key, description, entrypoint, env, started/
exited timestamps) plus a new `attached_pid` field and a richer `state` enum
(see below).

A central index lives at:

```
$XDG_STATE_HOME/spinclass/index/<sha256(worktree-abs-path)[:8]>.json
```

Each entry is one of three things, distinguishable by a single `lstat`:

| Index entry        | Meaning                                                           |
|--------------------|-------------------------------------------------------------------|
| Symlink (resolves) | Live session — target is the worktree-local `state.json`          |
| Regular file       | Cleanly closed by spinclass — file is a tombstone of final state  |
| Symlink (dangles)  | Externally closed (worktree removed without spinclass cleanup)    |

When a session closes cleanly (via `sc close` or the attach trap exiting on
session end), spinclass reads the worktree-local `state.json`, writes its
final contents to the index path as a **regular file** (replacing the
symlink), then deletes `<worktree>/.spinclass/`. The tombstone file
carries the final `state`, `exited_at`, and any close metadata, so
`sc list --closed` can show recent history without consulting git.

The symlink filename is derived purely from the worktree's absolute path;
the human-readable repo/branch/description fields live inside the JSON.

### Session state machine

Replaces the current `active` / `inactive` / `abandoned` triple. The first
three are written into the worktree's `state.json`; the last is observed by
the type of the index entry, not stored as a field.

| State              | Source              | Meaning                                                          |
|--------------------|---------------------|------------------------------------------------------------------|
| `running-attached` | `state.json`        | Entrypoint process alive AND a user is currently attached        |
| `running-detached` | `state.json`        | Entrypoint process alive, no attached client (e.g. detached zmx) |
| `dead`             | `state.json`        | Entrypoint process gone; worktree may still exist                |
| `closed`           | Index entry shape   | Index entry is a regular file (clean) or dangling symlink (extern) |

### `sc attach <id>` wrapper subcommand

A new built-in subcommand. Invoked from the sweatfile entrypoint as the
last hop into the multiplexer:

```toml
[session]
start  = ["zmx", "-g", "spinclass", "new", "sc", "attach", "$SPINCLASS_SESSION_ID"]
resume = ["zmx", "-g", "spinclass", "sc", "attach", "$SPINCLASS_SESSION_ID"]
```

`sc attach <id>` uses **fork-and-wait**:

1. Writes `running-attached` into the worktree's `state.json`, including its
   own PID as `attached_pid`.
2. Fires `[hooks].on-attach` if defined.
3. Forks the underlying multiplexer client as a child and `wait()`s for it.
4. When the child exits (user detached, or session ended), the parent
   queries multiplexer-group liveness via the configurable
   `[session].liveness-probe` (see below) and writes one of:
   - `running-detached` if the probe reports the group is still alive —
     fires `on-detach`, then exits, leaving `state.json` and the index
     symlink in place.
   - `dead` if the probe reports the group is gone — fires `on-detach`,
     then exits.
5. Clean session-close (separate code path from `on-detach`) is what
   promotes the index symlink to a regular tombstone file and removes
   `<worktree>/.spinclass/`. That happens on `sc close` or when the
   user explicitly tears the session down.

Fork-and-wait is preferred over `syscall.Exec`-and-trap because traps
cannot run reliably after the process image has been replaced; the parent
process must outlive the child to write the post-detach state.

### Liveness probe

The fork-and-wait parent needs to distinguish "user detached but multiplexer
session is still attachable" from "session is gone." Multiplexers vary, so
the probe is sweatfile-configurable.

zmx (0.4.x verified against `amarbel-llc/zmx` source) namespaces sessions
under groups via the global `-g <group>` flag (or `$ZMX_GROUP`). spinclass
exposes a corresponding sweatfile field:

```toml
[session]
# Multiplexer group name. Exported as $SPINCLASS_GROUP into the session env.
group = "sc-dev"

# Built-in default — checks the group for the running session id.
liveness-probe = ["sh", "-c", "zmx -g \"$SPINCLASS_GROUP\" list --short | grep -qxF \"$SPINCLASS_SESSION_ID\""]

start  = ["zmx", "-g", "sc-dev", "attach", "$SPINCLASS_SESSION_ID"]
resume = ["zmx", "-g", "sc-dev", "attach", "$SPINCLASS_SESSION_ID"]
```

Contract:

- Argv list, exec'd directly. The probe inherits the session env (so
  `$SPINCLASS_SESSION_ID`, `$SPINCLASS_GROUP`, `$SPINCLASS_WORKTREE`, etc.
  are available without explicit substitution).
- Exit 0 → session alive → state becomes `running-detached`.
- Non-zero exit → session gone → state becomes `dead`.
- A 2-second timeout applies; on timeout the parent writes `dead` and logs
  the event to the session's diagnostic stream.

zmx 0.4.x subcommands relevant to probing: `zmx [-g <group>] list
[--short|--json]` and `zmx groups`. `zmx run` is **not** safe as a
liveness probe because it creates the session if missing. The README at
amarbel-llc/zmx is out of date and does not mention `-g`/groups even
though they exist in the source — see amarbel-llc/zmx#2 for the manpages
request.

Users on tmux or zellij override:

```toml
[session]
liveness-probe = ["sh", "-c", "tmux has-session -t \"$SPINCLASS_SESSION_ID\" 2>/dev/null"]
```

### Hooks

Two new sweatfile hookpoints, parallel to the existing `create` / `stop` /
`pre-merge`:

```toml
[hooks]
on-attach = "echo attached >> /tmp/sc.log"
on-detach = "echo detached >> /tmp/sc.log"
```

Hook scalars override per-level (existing semantic). Both run with the same
env as the entrypoint (`SPINCLASS_SESSION_ID`, `SPINCLASS_WORKTREE`, etc.).

### Index walks

Operations that previously read `~/.local/state/spinclass/sessions/*` now
walk `$XDG_STATE_HOME/spinclass/index/`:

- `sc list` — `readdir` the index, `lstat` each entry. Symlinks that
  resolve are live; regular files are clean tombstones; dangling symlinks
  are externally-closed sessions. `--closed` includes the latter two.
- `sc resume` / `sc close` picker — same source, filter to live entries
  (resolved symlinks) and to the current repo via the target file's
  `repo_path`.
- Garbage collection (folded into `sc clean`):
  - `find $XDG_STATE_HOME/spinclass/index -xtype l -delete` reaps dangling
    symlinks.
  - Tombstone files older than a configurable retention (default: 30 days)
    are removed.

### Migration

One-shot, automatic, idempotent. On any `sc` invocation, before reading
state:

1. If `~/.local/state/spinclass/sessions/` exists and contains state files:
   for each, read the JSON, locate the worktree at `WorktreePath`, write
   `<worktree>/.spinclass/state.json` if absent, create the central symlink,
   delete the old file.
2. Worktrees whose `WorktreePath` no longer exists are skipped (their old
   state files are deleted; nothing to migrate).
3. Once the old directory is empty, remove it.

A separate `sc migrate-state --dry-run` is provided for inspection.

## Examples

### Layout for a single active session

```
~/eng/repos/myproj/.worktrees/feat-x/.spinclass/state.json
~/.local/state/spinclass/index/3f8a91c4d5b7e102.json -> ../../../../eng/repos/myproj/.worktrees/feat-x/.spinclass/state.json
```

(symlink target is shown relative for clarity; implementation may use absolute.)

### Detach/reattach cycle

```
$ sc start "fix login bug"
# sc attach writes running-attached, exec's into zmx
# user works, then hits zmx detach key
# attach-trap writes running-detached, on-detach hook runs

$ sc list
ID         STATE              BRANCH      DESCRIPTION
feat-x     running-detached   feat-x      fix login bug

$ sc resume feat-x
# sc attach writes running-attached again, exec's into zmx
```

### Cleanly closed session (sc close)

```
$ sc close feat-x
# spinclass reads worktree state, writes final snapshot to
# index/3f8a91c4d5b7e102.json as a regular file, removes
# <worktree>/.spinclass/, then proceeds with merge/cleanup.

$ sc list --closed
ID         STATE    BRANCH   CLOSED AT             DESCRIPTION
feat-x     closed   feat-x   2026-04-29T10:42:00Z  fix login bug
```

### Externally closed session (worktree removed without sc)

```
$ git worktree remove .worktrees/feat-x
$ sc list --closed
# index/3f8a91c4d5b7e102.json is now a dangling symlink — shown as
# closed with no tombstone metadata
$ sc clean
# dangling symlink removed from index
```

## Limitations

- **Single-attach assumption.** The state machine treats a session as either
  attached or detached. Multiple simultaneous attaches to the same multiplexer
  group (e.g. two zmx clients on the same group) are not modeled — the most
  recent `sc attach` wins for `attached_pid`.
- **SIGKILL gap on the attach process.** Fork-and-wait protects against the
  multiplexer client crashing — the parent still runs and updates state.
  But if the `sc attach` parent itself is `kill -9`'d, no post-detach write
  happens; the file is left in `running-attached` until something
  stale-checks `attached_pid`. Stale detection uses `kill -0` as a fallback
  during reads.
- **Multiplexer-agnostic.** `sc attach` does not know what zmx, tmux, or
  zellij are doing internally. It only sees its own process boundary. We
  cannot detect "user is attached but the inner shell crashed," for example.
- **Path-keyed symlinks are stable but not portable.** Moving a worktree
  directory invalidates its index entry. The migration command can be re-run
  to repair, but in-place renames are not auto-detected.
- **No backwards compatibility for the old layout.** After migration the old
  directory is removed; downgrading to a previous spinclass requires manual
  state reconstruction.

## More Information

- Tracked under milestone `v0.1.0 — Worktree-local session state +
  clean/completion triage`.
- Implementation slices (sequential):
  - [#41](https://github.com/amarbel-llc/spinclass/issues/41) — slice 1:
    worktree-local `state.json` + central symlink index (storage
    relocation, no behaviour change).
  - [#42](https://github.com/amarbel-llc/spinclass/issues/42) — slice 2:
    `sc attach <id>` lifecycle wrapper, fork-and-wait, new state enum,
    liveness probe, `on-attach`/`on-detach` hooks.
  - [#43](https://github.com/amarbel-llc/spinclass/issues/43) — slice 3:
    `sc list --closed` + tombstone GC retention.
- Issue [#40](https://github.com/amarbel-llc/spinclass/issues/40) — the
  `sc close <id>` regression that motivated revisiting this area;
  expected to be resolved by slice 1.
- Existing sweatfile hooks (`create` / `stop` / `pre-merge`) documented in
  `cmd/spinclass/doc/spinclass-sweatfile.5` — `on-attach` / `on-detach`
  follow the same merging and override semantics.
- Current state struct at `internal/session/session.go:21-33`.
- Current executor lifecycle at `internal/executor/session.go:21-102`
  (`Detach()` is presently a no-op stub at line 104).

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
