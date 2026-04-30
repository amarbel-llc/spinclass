---
status: exploring
date: 2026-04-30
promotion-criteria: |
  Promote to `proposed` once an activation model is selected — see
  "Open question: activation model" below. The remaining design
  (lifecycle, store identity, gitignore wiring, claude-allow wiring,
  manpage update) is locked in and does not block promotion.
---

# Per-worktree madder blob store

## Problem Statement

Spinclass has no first-class place for content-addressed, worktree-scoped
storage. Anything that isn't tracked in git — WIP scratch files, build
artifacts a hook produces, vendored binaries pulled in at session start,
intermediate captures of a working tree before a destructive operation —
either lives in ad-hoc paths the user maintains by hand or is lost when
the worktree is removed. The first integration with **madder** (see FDR
0002) takes the smallest viable step: spinclass ensures a per-worktree
madder blob store exists, then stays out of the way. Higher-level
integrations (auto-capture at merge time, helper verbs like
`sc snapshot` / `sc restore`, tombstone receipts) are deliberately
deferred.

## Locked-in design

These pieces apply regardless of how a user signals "I want this on."
They are settled.

### Store identity

Single store per worktree, fixed id `default`, addressed in madder
syntax as `.default` — the leading `.` is madder's CWD-relative prefix
(see madder `docs/man.7/blob-store.md`). On-disk layout:

```
<worktree>/.madder/
  local/share/blob_stores/default/
    blob_store-config            # 0444, immutable per madder ADR 0005
    ...                          # blob hash buckets, populated lazily
```

Multi-store support and alternative store types (inventory-archive,
SFTP, pointer) are out of scope. Users who need them can call
`madder init-*` themselves; spinclass will not manage extras.

### Init invocation

When the activation model decides "yes, init now," and
`<worktree>/.madder/local/share/blob_stores/default/blob_store-config`
does not yet exist, spinclass runs:

```
madder init -encryption none .default
```

The form matches madder's own bats suite (`run_madder init -encryption
none .default` in `zz-tests_bats/init.bats`). `-encryption none` is
required by madder's CLI; spinclass will not set encryption.

### Idempotency guard

`madder init` is **not** idempotent — re-running it on an existing
store fails (per madder's `init_idempotent_fails` test). Spinclass
guards by checking the `blob_store-config` file before invoking init.

### Isolation from ancestors

Spinclass sets `MADDER_CEILING_DIRECTORIES=<worktree>` on the init
invocation so madder will not walk up into a parent repo's `.madder/`
during store discovery. The variable is **not** exported into the
broader session environment — only the init invocation gets it.

### Persistence

The store survives `merge-this-session` and any subsequent merge
cycles. Spinclass worktrees are long-lived workers; the store
accumulates blobs across many merges and is removed only when the
worktree itself is removed (manual `git worktree remove` or eventual
sc-driven cleanup). `sc resume`, `sc close`, and `sc clean` perform
no operations against the store directory itself.

### Auto-actions: none

Spinclass does **not** read, write, capture, or restore anything
against the store on its own. Hooks, user scripts, or Claude tools
running inside the worktree do all the work. This is the bright line
that keeps idea 2 separable from ideas 1, 3, and 4 in FDR 0002.

### git-excludes

When the store is active for a worktree, `.madder/` is added to that
worktree's git-excludes (same mechanism that already manages
`.spinclass/` and `.envrc`). Idempotent.

### claude-allow

When the store is active for a worktree, spinclass appends
`Bash(madder:*)` to the worktree's claude-allow rules so Claude Code
sessions can call madder without a permission prompt.

### Manpage

`cmd/spinclass/doc/spinclass-sweatfile.5` is updated to document
whichever activation model is chosen. If the activation model
introduces a new sweatfile section, it gets its own `.SS [...]`
entry alongside the existing `[claude]`, `[git]`, `[direnv]`,
`[hooks]`, etc. If the activation model is purely auto-detection, the
manpage instead grows a section explaining the auto-detection
behaviour and pointers to madder(1) / blob-store(7).

The manpage is the user-facing source of truth; the FDR documents
intent. Both stay in sync.

## Open question: activation model

How does spinclass decide whether to manage a `.madder/` store for a
given worktree? This is the only undecided piece. The candidates
below are not exhaustive; combining them is also possible (e.g. C
plus D). Each candidate is followed by what it implies for the
"binary discovery" and "missing-binary handling" sub-questions, since
those answers cascade from the activation choice.

### Candidate A — No config; auto-on when `madder` is on PATH

Spinclass checks for `madder` on PATH at `sc start`. If found, init
the store. If not, silently skip — no store, no error, no friction.

- **Pro.** Zero config burden. Users with madder installed get the
  feature for free; users without it never know it exists.
- **Pro.** Failure mode is "no store" (which is the same as today),
  not "session refuses to start."
- **Con.** A user who has madder installed but doesn't want a
  `.madder/` directory in this worktree has no per-repo escape hatch.
  Workarounds: shadow `madder` with a stub on PATH, or remove the
  directory after `sc start`.
- **Con.** Surprising-by-default for users who installed madder for
  unrelated reasons and don't expect spinclass to start using it.
- **Binary discovery.** PATH only. No override.
- **Missing-binary handling.** No-op; not an error.

### Candidate B — No config; always-on, madder is a hard dependency

Every spinclass worktree gets a `.madder/` store; missing madder is
a hard error at `sc start` (or the existing devshell guarantees it).

- **Pro.** Maximum simplicity; a single behaviour.
- **Con.** Forces a runtime dependency on every spinclass user, even
  those who will never use the store.
- **Con.** Breaks any user who runs spinclass outside a devshell where
  madder isn't installed.
- **Binary discovery.** PATH only.
- **Missing-binary handling.** Hard error.

### Candidate C — Sweatfile opt-in flag

A new `[madder]` section in the sweatfile with an `enabled` boolean
(default `false`). Was the proposed model in the previous draft of
this FDR; recorded here as a candidate, not a decision.

```toml
[madder]
enabled = true
binary  = "madder"   # optional override
```

- **Pro.** Explicit, per-repo, per-user, hierarchically merged the
  same way every other sweatfile section already is. Users who never
  declare `[madder]` are unaffected.
- **Pro.** Natural place for the `binary` override and any future
  per-repo madder knobs.
- **Con.** New config surface to learn and maintain. Most users will
  never touch it, so it pays for itself only if a non-trivial
  fraction of users want per-repo control.
- **Con.** Sweatfile drift: enabling the flag, disabling it, and
  expecting the directory to disappear are not symmetric (see
  Limitations).
- **Binary discovery.** PATH by default; sweatfile `binary` overrides.
- **Missing-binary handling.** `sc validate` errors when enabled but
  unresolvable; `sc start` hard-errors before creating the worktree.

### Candidate D — Filesystem signal

Activate the store when a marker is present — e.g. an existing
`.madder/` directory in the parent repo, a top-level `.madder-keep`
file, or any sibling worktree that already has a store.

- **Pro.** Zero config; activation is "you have a `.madder/` here, so
  spinclass keeps it consistent."
- **Pro.** Works well if users naturally create `.madder/` at the
  repo level for unrelated reasons.
- **Con.** Magic. Unrelated `.madder/` directories (left by other
  tools) accidentally activate spinclass behaviour.
- **Con.** Doesn't answer the "I want this in a fresh worktree where
  no signal exists yet" case.
- **Binary discovery.** PATH only (no place to put an override).
- **Missing-binary handling.** Probably warn and skip; hard-erroring
  on a passive filesystem signal seems aggressive.

### Candidate E — CLI flag per session

`sc start --madder "fix login bug"` opts that single session in.

- **Pro.** Per-session, no persistent config. Easy to remember; easy
  to forget.
- **Con.** Does not survive `sc resume` unless re-flagged or a marker
  is written somewhere. The "spinclass worktrees are long-lived
  workers" property means this gets rediscovered on every cycle.
- **Con.** No global default mechanism.
- **Binary discovery.** PATH only.
- **Missing-binary handling.** Hard error when the flag is passed
  but the binary is missing.

### What we want from the choice

- **No friction for users who don't care.** Most users should
  experience zero behavioural change.
- **No surprising filesystem state.** A `.madder/` directory should
  only appear when the user has plausibly asked for one.
- **Compositional.** The chosen model should not foreclose adding the
  ideas 1/3/4 from FDR 0002 (snapshot, pre-merge capture, tombstone
  enrichment) on top.
- **Aligned with the rest of spinclass.** Sweatfile is the existing
  per-repo configuration mechanism; if the answer is "config", it
  goes there. If the answer is "no config", that is also fine — the
  bar is whether config earns its keep.

A working hypothesis (not a decision): start with **Candidate A** as
the simplest possible thing, and only graduate to **Candidate C** if
real usage surfaces a need for per-repo escape hatches or binary
overrides. Candidates B, D, and E feel less likely.

## Limitations

These apply once an activation model is chosen, regardless of which.

- **Single store, fixed id.** Multi-store support, alternative store
  types (inventory-archive, SFTP, pointer), and configurable store
  ids are deferred.
- **No auto-capture or auto-restore.** Spinclass does not invoke
  `tree-capture` or `tree-restore` on its own.
- **No encryption.** Spinclass always passes `-encryption none` to
  madder. Users who need encryption can init the store manually
  before running spinclass; the existence check on `blob_store-config`
  will then skip spinclass's own init.
- **No retention or fsck automation.** Spinclass does not run
  `madder pack` or `madder fsck` on the store. Users who care about
  consolidation run them manually.
- **Asymmetric activation/deactivation.** If activation is
  configurable (Candidates C, D, E) and the user later turns it off,
  spinclass does not delete an existing `.madder/` directory. The
  user is responsible for cleanup.
- **No interaction with the main repo's `.madder/`.** The worktree
  store is fully isolated via `MADDER_CEILING_DIRECTORIES`. Sharing
  blobs with the parent repo (or with sibling worktrees) requires an
  explicit `madder sync` from the user.

## More Information

- FDR 0002 (`docs/features/0002-madder-integration.md`) — the
  exploratory parent that catalogues the broader idea space and
  identifies this feature as the chosen first focus.
- Madder docs:
  - `docs/man.7/blob-store.md` — store-id grammar, store types, XDG
    paths.
  - `docs/man.7/tree-capture-receipt.md` — receipt format (relevant
    to follow-up FDRs that add capture/restore on top of this store).
- Madder behaviours this design relies on:
  - `init_idempotent_fails` (`zz-tests_bats/init.bats:26`) —
    confirms spinclass must guard `madder init` with an existence
    check.
  - `init_default_config_is_read_only` (same file, line 12) —
    confirms the `blob_store-config` file is a stable existence
    marker.
  - `MADDER_CEILING_DIRECTORIES` (`go/internal/india/commands/main.go`,
    env-var description) — the isolation knob spinclass uses during
    init.
- Implementation tracking issue:
  [#53](https://github.com/amarbel-llc/spinclass/issues/53),
  scoped to milestone v0.2.0.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
