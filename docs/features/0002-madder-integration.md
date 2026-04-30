---
status: exploring
date: 2026-04-30
promotion-criteria: |
  Promote to `proposed` once one of the integration angles is selected and
  fully designed (interface, data model, lifecycle hooks). Spinclass has
  agreed to start with #2 (per-worktree blob store); the others remain
  catalogued here so they aren't lost if the focus narrows further.
---

# Madder integration

## Problem Statement

Spinclass manages worktree lifecycles (create → attach → merge → close) but
has no first-class way to capture, share, or restore content-addressed
state tied to a worktree. Today, anything outside git's tracked tree —
uncommitted scratch files, build artifacts, vendored binaries fetched at
session start, intermediate captures of the worktree before a destructive
operation — is either lost on close or kept manually outside spinclass's
view. **Madder** (`github.com/amarbel-llc/madder`) is a content-addressable
blob storage CLI extracted from dodder; its **tree-capture / tree-restore**
primitives plus its CWD-relative store prefix (`.<id>` resolves under
`$PWD/.madder/local/share/blob_stores/<id>`) make it a natural backing
store for spinclass's per-worktree, content-addressed needs.

This FDR catalogues the integration ideas that came out of an initial
research pass on madder's surface area, and identifies the one we will
take forward as the first concrete design.

## Madder primitives we'd lean on

The relevant pieces of madder's CLI for any spinclass integration:

- **Blob-store-id prefixes** — `.foo` is a CWD-relative store at
  `$PWD/.madder/local/share/blob_stores/foo`; unprefixed names are XDG
  user stores; `%foo` is a purgeable cache store; etc.
- **`madder init <store-id>`** — creates a store of the appropriate type
  given the prefix.
- **`madder write <store-id> <path>`** — content-addresses a single file,
  emits the blob's markl-id on stdout.
- **`madder tree-capture <store-id> [<path>...]`** — walks a filesystem
  tree, writes every regular file as a blob, emits a single
  *receipt* (also a blob) listing every entry by path, mode, size, blob-id,
  and symlink target. Receipts are deterministic — identical input trees
  produce identical receipt blob-ids.
- **`madder tree-restore <store-id> <receipt-id> <dest>`** — rehydrates a
  tree from a receipt blob-id back to a destination directory.
- **`madder sync <src-store> <dst-store>`** — copies blobs between stores
  (e.g. from a worktree-local store up to the user XDG store).
- **`madder fsck` / `madder pack`** — health and on-disk consolidation.

See `docs/man.7/blob-store.md` and `docs/man.7/tree-capture-receipt.md`
in the madder repo for the canonical reference.

## Idea space

Four integration angles surfaced during the research pass. They are not
mutually exclusive — a few of them compose — but the FDR exists so we
don't lose the broader picture while focusing on one.

### 1. Worktree snapshot / stash before destructive ops

Treat madder as the storage layer behind a `sc stash` / `sc snapshot`
verb. Before a destructive transition (close, abandoned-cleanup, fork
that discards local state, or even just on user demand), spinclass calls
`madder tree-capture` against an XDG user store, persists the resulting
receipt blob-id into the session's tombstone, and surfaces it via
`sc list --closed` for later `madder tree-restore`. Cross-session
deduplication comes for free because madder is content-addressed.

**Why interesting.** Recovers the "what was in the worktree at close
time" question without bloating git history with WIP commits.

**Risks / unknowns.** Decision needed on which store to use (user,
cache-purgeable, or per-session); retention story for receipts that
outlive the worktree.

### 2. Per-worktree blob store (chosen first focus)

The most contained idea: every spinclass-managed worktree gets its own
CWD-relative madder store rooted at `<worktree>/.madder/local/share/
blob_stores/<id>`. Spinclass owns its lifecycle: `madder init` on
worktree create, the directory is automatically removed on
`merge-this-session` or `sc close`, and the store id can be referenced
by build tooling, hooks, or user scripts running inside the worktree.

This is the right starting point because:

- It introduces madder to spinclass without coupling either tool's data
  model — spinclass just makes sure the directory exists and is cleaned
  up.
- It composes with ideas 1, 3, and 4: a worktree-local store is a
  natural staging area before syncing to a longer-lived XDG store.
- Ownership is unambiguous: the store dies with the worktree.
- The `.madder/` directory pattern is already what madder expects for
  CWD-relative stores; we're not inventing layout.

A full design (interface, sweatfile knobs, hook integration, gitignore
handling, cleanup ordering relative to existing hooks) is intentionally
deferred to a follow-up FDR or a re-promotion of this one to `proposed`.

### 3. Pre-merge build artifact capture

`merge-this-session` already runs the pre-merge hook, which typically
shells out to `just` (build + tests). Capturing the build outputs
(`result/`, `dist/`, etc.) into a madder store before the merge would
let post-merge analysis or diff-vs-base inspection reach back to the
exact artifacts that were validated. The receipt blob-id could be
recorded in the merge commit trailer.

**Why interesting.** Pairs cleanly with content-addressed CI caches and
gives a stable handle on "what got merged" beyond the source tree.

**Risks / unknowns.** Captures get large fast; need a retention or
cache-store-prefix story so we don't fill XDG_DATA_HOME with dead
build outputs.

### 4. Tombstone enrichment for forensic recovery

When a session closes (cleanly or via abandonment), capture the final
state of the worktree (or just a curated subtree like `.spinclass/`,
`.envrc`, scratch dirs that aren't gitignored) into a long-lived madder
store and stash the receipt blob-id in the tombstone JSON written by
the existing tombstone GC machinery. Later, `sc forensic <id>` can
`tree-restore` that state into a temp dir for triage.

**Why interesting.** Makes "what was in that worktree we just nuked"
recoverable without redirecting users to filesystem snapshots.

**Risks / unknowns.** Privacy / disk-usage implications of capturing
arbitrary worktree contents on every close; may need an opt-in
sweatfile knob or an explicit subset selector.

## Chosen first focus

**Idea 2 — per-worktree blob store.** It is the smallest viable surface
that brings madder into spinclass and unlocks the others without
committing to their semantics. Once a worktree-local store exists and
is reliably created/destroyed alongside the worktree, ideas 1, 3, and 4
become incremental additions: each is "use the existing store, write a
receipt, save the id somewhere."

The detailed interface — sweatfile config (opt-in vs default-on, store
id, handling of nested `.madder/`), gitignore wiring, claude-allow
implications, hook ordering on create / merge / close, and the
`sc validate` checks — will be written up in a separate FDR (or a
re-promotion of this one) once we agree on the shape.

## Limitations

- **Speculative scope.** This FDR is exploratory; none of the four ideas
  has a working implementation. Limitations specific to each idea will
  move into the per-idea FDR when one is promoted to `proposed`.
- **Madder is new to spinclass.** Spinclass currently has zero madder
  references. Any integration introduces a new external tool dependency
  (binary on PATH or pinned via the flake) and a new permission story
  (`madder` invocations under `claude-allow`).
- **No format guarantees crossed.** None of the ideas commit spinclass
  to madder's wire formats (markl-ids, hyphence). The integration is
  always at the CLI boundary.

## More Information

- Madder repo: <https://github.com/amarbel-llc/madder>
- Madder `CLAUDE.md` (extraction history and dodder cross-references):
  <https://github.com/amarbel-llc/madder/blob/master/CLAUDE.md>
- Madder man pages used during research:
  - `docs/man.7/blob-store.md` — store-id grammar, store types, XDG paths
  - `docs/man.7/tree-capture-receipt.md` — receipt format
- Spinclass FDR 0001 (worktree-local session state) — establishes the
  pattern of putting per-worktree spinclass state at
  `<worktree>/.spinclass/`. A per-worktree madder store at
  `<worktree>/.madder/` is a deliberate sibling of that pattern.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
