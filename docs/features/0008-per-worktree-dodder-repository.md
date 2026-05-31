---
status: exploring
date: 2026-05-30
promotion-criteria: |
  Promote to `proposed` once these have decisions: (1) the plugin-abstraction
  shape — bespoke third pin vs. a shared participant abstraction that spans the
  three injection sites madder already occupies (create, merge-capture,
  mcp-resource); (2) signing-key acquisition — when the markl.Id is resolved
  from the agent and what happens at `sc start` if the agent is locked; (3)
  whether the dodder MCP server is auto-registered when the dodder pin is
  active or left to a user `[[mcps]]` entry. The store model (dodder over the
  CWD-local `.default` madder store) is already verified — see "Verified
  ground" — and must remain expressible end-to-end with the chosen design.
---

# Per-worktree dodder repository (over the madder store)

## Problem Statement

FDR 0003 gave each spinclass worktree a per-worktree **madder** blob store at
`<worktree>/.madder/`, created at `sc start` when the binary was built with a
non-null `madder` input to `lib.mkSpinclass`. **Dodder** —
`github.com/amarbel-llc/dodder`, the distributed zettelkasten and
content-addressable store that madder was extracted from — is the natural next
pin: it layers a signed, queryable object index (zettels, tags, types) over a
madder blob store, and ships its own MCP server. The motivating want is a
per-worktree dodder repository the agent can write notes/objects into during a
session, content-addressed and sharing the worktree's existing madder blobs,
created and isolated the same way the madder store already is.

This FDR records the design. The store model the user chose — **dodder layered
on the existing per-worktree madder store, not a second independent store** —
turned out to be the load-bearing unknown, so it was verified empirically
before writing anything else (below). What remains genuinely open is *not* the
storage mechanics but the **abstraction**: madder is already wired into
spinclass at three distinct injection sites, and adding dodder as a fourth
copy-pasted `if embeds.XBin() != ""` block would triple that surface. The
user's framing — "madder is kinda dependency-injected for us, especially with
merge-this-session output" — is exactly the tension this FDR has to resolve.

## Verified ground

Everything in this section is **VERIFIED** against the binary pair this
worktree actually pins (`madder 0.3.30+9cbbd1b`, the spinclass shim at
`.git/spinclass/bin/madder`; `dodder 0.1.19+dacf90c` on PATH), via three
`[group('explore')]` justfile recipes added alongside this FDR. Re-run them
against any other pair with `DODDER_BIN` / `MADDER_BIN`.

- **`just explore-dodder-init-plain`** — a plain `dodder init -encryption none
  -repo_id . <id>` creates both a `.dodder/` repo tree *and* a `.madder/`
  default blob store via dodder's *embedded* madder. The pinned standalone
  `madder list` reports that store with `"id":".default"` — i.e. dodder's
  embedded madder and the separately-pinned standalone madder agree on the
  CWD-relative store identity and on-disk format. **No version skew at the
  store-read boundary.**

- **`just explore-dodder-reuse-cwd`** — the chosen model. `madder init
  -encryption none .default` (what spinclass's pinned madder already does at
  `sc start`), then `dodder init -encryption none -repo_id . -blob_store-id
  .default <id>`. **Succeeds.** The resulting tree has exactly one
  `.madder/local/share/blob_stores/default/` — dodder *adopted* the existing
  store rather than creating a second one — alongside the new `.dodder/` repo
  tree.

- **`just explore-dodder-reuse-xdg`** — the fallback. A plain-named madder
  store in an XDG-scoped dir, reused via `-blob_store-id <name>`. Also
  succeeds; written objects land in the shared store. Kept as a documented
  alternative; **not needed** given the CWD path works.

This contradicts an earlier standalone research pass that reported `blob store
not found: ".default"`. The **theory** (not asserted as fact — the earlier
steps were not re-run exactly) is that `.default` is a *CWD-relative* prefix
that only resolves when `dodder init` runs in the directory containing
`.madder/`; the earlier attempt likely ran the two commands from different
working directories. For spinclass this is a non-issue: `sc start` runs both in
the worktree root.

### Settled mechanics (from FDR 0003 + dodder verification)

- **Store identity.** Reuse the FDR-0003 madder store `.default`; do not create
  a dodder-owned blob store. dodder repo tree lives at `<worktree>/.dodder/`,
  a deliberate sibling of `.madder/` and `.spinclass/`.
- **Init invocation.** When the dodder pin is non-empty and
  `<worktree>/.dodder/local/share/config-seed` does not exist:
  `$dodderBin init -encryption none -repo_id . -blob_store-id .default <repo-id>`,
  run with cwd = worktree.
- **Idempotency guard.** dodder's stable marker is
  `.dodder/local/share/config-seed` (the analog of madder's
  `blob_store-config`). Re-running `dodder init` over an existing repo *fails*
  (collides on `inventory_lists_log`), so spinclass stats the marker and skips
  if present — same shape as `madder.StoreReady`.
- **Isolation.** Set **both** `DODDER_CEILING_DIRECTORIES=<worktree>` and
  `MADDER_CEILING_DIRECTORIES=<worktree>` on the init invocation (dodder spans
  two XDG utility scopes). Not exported into the session.
- **Encryption.** Off by default; pass `-encryption none` to match the madder
  invocation style. Spinclass does not configure encryption.
- **git-excludes / claude-allow.** Add `.dodder/` to the worktree's
  git-excludes and `Bash(dodder:*)` to claude-allow, mirroring `.madder/` /
  `Bash(madder:*)`.
- **Persistence.** Like the madder store, the dodder repo survives merges and
  dies with the worktree. Spinclass performs no reads/writes/queries against it
  on its own.

## Signing identity: reuse the pivy-agent key

`dodder init` generates a fresh ed25519 signing key per repo unless given
`-private_key <markl.Id>`. Per-worktree throwaway keys would sprawl signing
identities across every worktree, so spinclass passes the user's **existing
pivy-agent key**:

- `dodder info-ssh_agent` (or `info-pivy_agent`) lists agent keys; the markl.Id
  is the first whitespace-token per line (or column 3 with `-verbose`).
  (**VERIFIED** from `dodder-init(1)`, `dodder-info-ssh_agent(1)`, and dodder's
  `init_ecdsa_p256.bats` end-to-end test; **not yet exercised** by a spinclass
  recipe — a `explore-dodder-reuse-cwd-signed` variant is the obvious next
  recipe.)
- Spinclass resolves one markl.Id and passes it as `dodder init -private_key
  <id>` so every worktree repo signs with the same agent-served key.

This couples to the user's pivy-agent stack (see `eng-ssh(7)`). The open
question is **timing and failure**: if the agent is locked at `sc start` (a
common state — see the project rule that gpg-signing failures mean "ask the
user to unlock pivy-agent"), does spinclass hard-fail the worktree create, skip
dodder init with a TAP `not ok`, or fall back to a generated key? Lean: **skip
with a diagnostic**, never silently generate a divergent identity. Recorded as
an open question.

## Two facets of "load dodder as a plugin"

Research surfaced that "plugin" is overloaded here, and dodder presents two
*independent* facets that map to two *existing* spinclass mechanisms:

1. **dodder-as-CLI-binary + per-worktree repo init** — the FDR-0003 pattern: a
   `dodderBin` link-time pin, an `embeds.DodderBin()` accessor, and
   worktree-lifecycle participation (init the repo, link into the shim bin dir,
   git-excludes + claude-allow). This is where the copy-paste pressure lives.

2. **dodder-as-MCP-server** — dodder ships an MCP server (`dodder mcp`, plus a
   `dodder install-mcp` helper) and already appears as the `dodder` child in
   the user's moxy proxy. Spinclass *already* supports MCP registration via
   `[[mcps]]` sweatfile entries (`worktree.go` builds `.mcp.json` from
   `ActiveMCPs()`), so this facet needs **zero new code** — a user can register
   dodder's MCP server today. The only design choice is whether an active
   dodder *pin* should auto-add the `[[mcps]]` entry (so the binary pin and the
   agent-facing tools travel together) or leave it fully manual.

Scope for this work is **both** facets. Facet 2's plumbing exists; the decision
is auto-register vs. manual.

## Open question: the plugin abstraction

This is the real design question, and the reason this FDR is `exploring`.

### Why a flat interface doesn't obviously fit

spinclass has, today, **two** build-time pins (`madder`, `direnv`) threaded as
separate ldflags + separate `embeds` accessors, and madder's integration alone
touches **three** injection sites:

| Site | madder | dodder (proposed) | direnv |
| --- | --- | --- | --- |
| worktree create | init `.default` store, shim-link, excludes/allow (`worktree.go`) | init `.dodder` over `.default`, shim-link, excludes/allow | — (PATH/pin for `.envrc` render) |
| merge/check output | write pre-merge hook output as a blob, surface `madder://` link (FDR 0005; `merge.go`, `check.go`) | — | — |
| MCP resource provider | `madder://blobs/<digest>` reader (`commands_mcp.go`, `madder_provider.go`) | — (ships its own MCP *server* instead) | — |

The four pins have **heterogeneous roles**: madder occupies all three sites,
dodder occupies create + (server-style) MCP, direnv occupies none of them
(it's a binary the `.envrc` renderer resolves). A single `WorktreeParticipant`
interface cleanly models only the *create* row; it does not model madder's
merge-capture role or either tool's MCP role. So "extract one interface" is not
a slam dunk — the honest shape is *several* small extension points, with each
pin implementing the subset it needs.

### Prior-art correction

FDR 0003 says `mkSpinclass` "mirrors `lib.mkCircus`" from clown. That is
**misleading** and should not propagate into this design: clown's `mkCircus`
pins Claude-Code plugin *directories* (`.claude-plugin/plugin.json`) and burns
`--plugin-dir` flags; madder is not a clown plugin at all. The actual prior art
for spinclass's binary-pin is **moxy**, which pins madder with the identical
`-X ...defaultMadderBin=<path>` ldflag trick. `mkCircus`'s *list-of-records*
ergonomics are still worth borrowing on the Nix side (Option A below), but its
runtime model is not the precedent.

### Options (decision deferred)

- **Option A — Nix list-of-records.** Replace the two named `mkSpinclass`
  slots with `pins = [ { name; binary; } … ]`, lowered to per-name ldflags or
  one delimited string. Borrows `mkCircus` ergonomics; stops the Nix-side
  copy-paste. Does **not** address the Go-side per-site behaviour.
- **Option B — Go-side extension points.** `embeds` becomes a `map[string]string`
  (name → pinned path). Define small interfaces per site —
  e.g. `WorktreeParticipant { OnCreate; GitExcludes; ClaudeAllow }` and a
  separate `MergeArtifactSink` — and have `internal/madder`, a new
  `internal/dodder`, and a trivial `internal/direnv` implement the subset each
  needs. `applyWorktreeConfig` / merge / check iterate registered participants
  instead of hard-coding `if embeds.MadderBin() != ""`. This is the lever that
  actually removes the scattered guard blocks; it has no clown analog because
  clown plugins do no filesystem-lifecycle work.
- **Option C — minimal: bespoke dodder + push MCP to `[[mcps]]`.** Add dodder
  as a third `if embeds.DodderBin() != ""` block, rely on `[[mcps]]` for the
  server, and file a followup to extract Option B once three concrete pins
  exist to generalize from. Ships fastest; pays the abstraction cost later with
  better information.

**Lean:** A + B together — Nix list for ergonomics, per-site Go interfaces for
the lifecycle — but only after the bespoke dodder integration exists as the
third concrete example, so the interfaces are extracted from real usage rather
than guessed. In practice that means **C first, then A+B**, unless the FDR
review prefers to pay the abstraction cost up front.

## Limitations

- **Store reuse verified for one binary pair only.** The CWD `.default` reuse
  is green for `madder 0.3.30+9cbbd1b` / `dodder 0.1.19+dacf90c`. A future
  `mkSpinclass` pinning a different pair must re-run
  `just explore-dodder-reuse-cwd`; this FDR does not claim version-independence.
- **`.default` is CWD-relative.** dodder init must run in the worktree root.
  Not a constraint for `sc start`, but any future "init dodder in a subdir"
  flow would need the XDG-named path instead.
- **Signing couples to pivy-agent availability.** A locked agent at `sc start`
  is unresolved (see open question). No design yet for rotating or
  re-pointing a worktree repo's key after creation.
- **No auto-capture/query.** Like FDR 0003, spinclass never writes or queries
  the dodder repo on its own. The agent and user scripts do all the work.
- **Single repo, fixed shape.** One `.dodder/` per worktree, default object
  index, no remotes wired by spinclass. `dodder remote-add` / `pull` / `push`
  are the user's to run.
- **Abstraction is the gating risk, not the storage.** If Option C ships first,
  the codebase carries three near-duplicate pin blocks until A+B lands; the
  followup issue must be filed at merge time, not deferred to memory.

## More Information

- FDR 0002 (`docs/features/0002-madder-integration.md`) — madder idea space;
  dodder is named there as madder's origin.
- FDR 0003 (`docs/features/0003-per-worktree-madder-blob-store.md`) — the
  per-worktree madder store this design layers on; source of the init / marker
  / ceiling / git-excludes / claude-allow patterns reused here. (Note the
  `mkCircus` mischaracterization corrected above.)
- FDR 0004 (`docs/features/0004-direnv-template-plugins.md`) — the other
  "spinclass exposes a plugin point" record; direnv as the third heterogeneous
  pin.
- FDR 0005 (`docs/features/0005-merge-this-session-output-shape.md`) — madder's
  merge-capture injection site (`madder://blobs/<digest>` resource_link), the
  role no flat `WorktreeParticipant` interface covers.
- Verification recipes (this repo's `justfile`, `[group('explore')]`):
  `explore-dodder-init-plain`, `explore-dodder-reuse-cwd`,
  `explore-dodder-reuse-xdg`.
- dodder references: `dodder-init(1)`, `dodder-info-ssh_agent(1)`,
  `dodder-info-pivy_agent(1)`; `dodder 0.1.19` `init_ecdsa_p256.bats` /
  `init.bats` / `deinit.bats` (store-reuse + agent-key tests).
- `eng-ssh(7)` — the pivy-agent stack the signing-key reuse depends on.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
