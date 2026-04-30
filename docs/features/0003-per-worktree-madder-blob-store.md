---
status: proposed
date: 2026-04-30
promotion-criteria: |
  Promote to `experimental` once:
  - The `[madder] enabled = true` sweatfile path creates a working store
    on `sc start` and the store survives at least one full
    `sc merge-this-session` cycle on the same worktree.
  - `sc validate` reports a clear error when `[madder] enabled = true`
    but the configured binary is missing.
  - `madder` invocations work inside Claude sessions without a
    permission prompt (claude-allow wired up).
  - `.madder/` is auto-added to git-excludes for the worktree alongside
    `.spinclass/` and `.envrc`.
  - `cmd/spinclass/doc/spinclass-sweatfile.5` has a `.SS [madder]`
    section covering both fields and the isolation behaviour.
  Promote to `accepted` after at least two distinct repos run sessions
  with `[madder] enabled = true` end-to-end with no manual cleanup.
---

# Per-worktree madder blob store

## Problem Statement

Spinclass has no first-class place for content-addressed, worktree-scoped
storage. Anything that isn't tracked in git — WIP scratch files, build
artifacts a hook produces, vendored binaries pulled in at session start,
intermediate captures of a working tree before a destructive operation —
either lives in ad-hoc paths the user maintains by hand or is lost when
the worktree is removed. The first integration with **madder** (see FDR
0002) takes the smallest viable step: spinclass guarantees a per-worktree
madder blob store exists when the user opts in, then stays out of the
way. Higher-level integrations (auto-capture at merge time, helper
verbs like `sc snapshot` / `sc restore`, tombstone receipts) are
deliberately deferred.

## Interface

### Sweatfile

A new `[madder]` section, hierarchically merged the same way other
sweatfile sections are (global → parent → repo).

```toml
[madder]
# Off by default. When true, spinclass ensures a per-worktree CWD-relative
# blob store exists at `<worktree>/.madder/local/share/blob_stores/default`.
enabled = false

# Path or name of the madder binary. Resolved against PATH when not
# absolute. Defaults to "madder".
binary = "madder"
```

Both fields are optional; defaults are as shown. `[madder] enabled = true`
at any level of the hierarchy turns the feature on; lower levels can
override (`enabled = false` in a worktree's repo-level sweatfile shuts
off something inherited from global).

### Store identity

Single store per worktree, with the fixed id `default`, addressed in
madder syntax as `.default` — the leading `.` is madder's CWD-relative
prefix (see madder `docs/man.7/blob-store.md`). The on-disk layout
spinclass produces is therefore:

```
<worktree>/.madder/
  local/share/blob_stores/default/
    blob_store-config            # 0444, immutable per madder ADR 0005
    ...                          # blob hash buckets, populated lazily
```

Multi-store support and alternative store types (inventory-archive,
SFTP, pointer) are not in this FDR; users who need them can call
`madder init-*` themselves against the worktree, but spinclass won't
manage them.

### Lifecycle

| Event                          | Spinclass behavior                                                                            |
|--------------------------------|-----------------------------------------------------------------------------------------------|
| `sc start` (and friends)       | After sweatfile load, if `[madder] enabled` is true and the store-config does not yet exist, run `<binary> init -encryption none .default` from the worktree. |
| `sc resume`                    | No-op. The store already exists from `sc start`.                                              |
| `merge-this-session`           | No-op. The store survives. Spinclass worktrees are long-lived workers; the store accumulates blobs across many merge cycles. |
| `sc close` / `sc clean`        | No-op on the store directory itself; it lives inside the worktree and is removed only when the worktree is removed (manual `git worktree remove`, eventual sc-driven cleanup). |
| `sc validate`                  | When `[madder] enabled = true`, check that the configured binary is resolvable. Missing binary is an error.                                              |

The init invocation matches the form madder's own bats suite uses
(`run_madder init -encryption none .default` in
`zz-tests_bats/init.bats`). `-encryption none` is required by madder;
spinclass will not set encryption on its own.

### Idempotency

`madder init` is **not** idempotent — re-running it on an existing store
fails (per madder's `init_idempotent_fails` bats test). Spinclass guards
against this by checking
`<worktree>/.madder/local/share/blob_stores/default/blob_store-config`
before invoking init. Existence of that file is the marker that the
store is already initialised.

### Auto-actions: none

This is the smallest possible first cut. Spinclass does **not** read,
write, capture, or restore anything against the store on its own. Users
or hooks invoking `madder` from inside the worktree do all the work.

### git-excludes

`.madder/` is added to the worktree's git-excludes (same mechanism that
already manages `.spinclass/` and `.envrc`) when `[madder] enabled` is
true. Idempotent — re-running `sc start` does not duplicate the entry.

### claude-allow

When `[madder] enabled` is true, spinclass appends `Bash(madder:*)` to
the worktree's claude-allow rules so Claude Code sessions can call
madder without a permission prompt.

### Missing-binary handling

- `sc validate` reports a clear error if `[madder] enabled = true` but
  the binary is unresolvable.
- `sc start` with the flag enabled and the binary missing: hard error
  before the worktree is created, with a message pointing the user at
  either installing madder or unsetting `[madder] enabled`.

### Manpage

A new `.SS [madder]` subsection in
`cmd/spinclass/doc/spinclass-sweatfile.5`, alongside the existing
`[claude]`, `[git]`, `[direnv]`, `[hooks]`, `[session-entry]`,
`[[start-commands]]`, `allowed-mcps`, and `[[mcps]]` entries. It
documents both fields (`enabled`, `binary`), names the on-disk location
of the resulting store, calls out the `MADDER_CEILING_DIRECTORIES`
isolation behaviour, and points at madder(1) and blob-store(7) as the
upstream reference. Merge semantics follow the existing **scalar
override** pattern documented in the manpage's `MERGE SEMANTICS`
section.

The manpage is the user-facing source of truth for sweatfile syntax;
the FDR documents intent. Both must stay in sync — adding the
sweatfile fields without updating the manpage is not a complete
implementation.

### MADDER_CEILING_DIRECTORIES

To keep the worktree store isolated from any ancestor `.madder/`
directory, spinclass sets `MADDER_CEILING_DIRECTORIES` (mirroring
`GIT_CEILING_DIRECTORIES`) to the worktree path when invoking init.
This prevents madder from walking up into the parent repo's `.madder/`
if one exists. Spinclass does **not** export the variable into the
session environment — only the init invocation gets it.

## Examples

### Opting in via sweatfile

A repo's sweatfile turns the feature on:

```toml
# <repo>/sweatfile
[madder]
enabled = true
```

After `sc start "fix login bug"`, the new worktree contains:

```
~/eng/repos/myproj/.worktrees/fix-login-bug/
  .madder/local/share/blob_stores/default/blob_store-config
  .spinclass/state.json
  .envrc
  ...
```

`git status` inside the worktree shows nothing new because `.madder/`,
`.spinclass/`, and `.envrc` are all in git-excludes.

### Persistence across merges

```
$ cd ~/eng/repos/myproj/.worktrees/fix-login-bug
$ printf hello | madder write -format json - | jq -r '.id'
blake3-x256-sha2-x256:7d8e...

$ sc merge-this-session
# rebase + ff-merge + push, all green

$ ls .madder/local/share/blob_stores/default
blob_store-config  ab/  ...

# blob is still there; the store outlives the merge cycle.
$ madder cat blake3-x256-sha2-x256:7d8e...
hello
```

### Opting out per-repo

A repo overrides a global default:

```toml
# ~/.config/spinclass/sweatfile
[madder]
enabled = true

# myproj/sweatfile (this repo doesn't want it)
[madder]
enabled = false
```

`sc start` in `myproj` skips madder init and leaves no `.madder/`
directory.

## Limitations

- **Single store, fixed id.** Multi-store support, alternative store
  types (inventory-archive, SFTP, pointer), and configurable store
  ids are deferred. Users who need them call madder directly.
- **No auto-capture or auto-restore.** Spinclass does not invoke
  `tree-capture` or `tree-restore` on its own. The other ideas in
  FDR 0002 (snapshot/stash, pre-merge artifact capture, tombstone
  enrichment) compose on top of this FDR; they are not part of it.
- **No encryption.** Spinclass always passes `-encryption none` to
  match madder's default test posture. Users who need encryption can
  init the store manually before running spinclass; spinclass's
  existence check on `blob_store-config` will then skip its own init.
- **Not idempotent across config drift.** If `[madder] enabled` flips
  from true to false, spinclass will not delete the existing
  `.madder/` directory. The user is responsible for cleanup.
- **No interaction with the main repo's `.madder/`.** The worktree
  store is fully isolated via `MADDER_CEILING_DIRECTORIES`. Sharing
  blobs with the parent repo (or with sibling worktrees) requires
  explicit `madder sync` from the user.
- **No retention or fsck automation.** Spinclass does not run
  `madder pack` or `madder fsck` on the store. Users who care about
  consolidation run them manually.
- **Madder dependency is runtime, not compile-time.** Spinclass calls
  `madder` via PATH (or the configured override), so missing-binary
  errors only surface at the moments listed under "Missing-binary
  handling" above.

## More Information

- FDR 0002 (`docs/features/0002-madder-integration.md`) — the
  exploratory parent that catalogues the broader idea space and
  identifies this feature as the chosen first focus.
- Madder docs:
  - `docs/man.7/blob-store.md` — store-id grammar, store types, XDG
    paths.
  - `docs/man.7/tree-capture-receipt.md` — receipt format (relevant to
    follow-up FDRs that add capture/restore on top of this store).
- Madder behaviors this design relies on:
  - `init_idempotent_fails` (`zz-tests_bats/init.bats:26`) — confirms
    spinclass must guard `madder init` with an existence check.
  - `init_default_config_is_read_only` (same file, line 12) — confirms
    the `blob_store-config` file is a stable existence marker.
  - `MADDER_CEILING_DIRECTORIES` (`go/internal/india/commands/main.go`,
    env-var description) — the isolation knob spinclass uses during
    init.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
