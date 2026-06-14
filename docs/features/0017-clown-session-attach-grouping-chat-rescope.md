---
status: proposed
date: 2026-06-14
promotion-criteria: |
  proposed -> experimental: clown RFC-0013 (clownfile + per-instance-key
  derivation + the chat construct: derived group channels + presence index;
  owned by clown) is filed and FDR-0017 ⇆ RFC-0013 carry a mutual cross-ref;
  the Venn redraw reads consistently with it;
  and a working implementation of at least one of the four pieces lands. The
  spinclass-side removals (chat surface + multiplexer defaults) and the
  decoration seam are the measurable spinclass signals. experimental ->
  testing: a message addressed to a whole spinclass session reaches every clown
  under it, and a per-clown directed message reaches exactly one, for ~1 week
  with no #118-class sender-flip.
---

# clown ⇆ spinclass: session-attach, grouping, and chat ownership rescope

> **Draft (proposed).** This is the spinclass-side half of a two-document
> contract. The clown-owned design — the clownfile schema, the **per-instance
> key derivation**, the **chat construct**, and the **spinclass-session
> addressing decoration** — is normative in **clown RFC-0013** (the companion,
> owned by clown/fond-sycamore; builds on RFC-0012; **filed on clown master**,
> status proposed; mutual cross-ref with FDR-0017 complete). This FDR **redraws
> the Venn** first drawn in
> **FDR-0016** for four features that straddle clown and spinclass. Treat any
> divergence between the Venn here and clown RFC-0013 as a doc bug to reconcile,
> not a design choice.

## Problem Statement

FDR-0016 wrote down the clown ⇆ spinclass seam for *session identity,
addressing, and liveness* — but four user-facing features still straddle the
two systems, and the boundary is now being **moved deliberately** rather than
merely discovered. The decisive call: **chat is not a spinclass feature at
all** — it is a clown construct. spinclass's only contribution to addressing is
the **decoration** it already sets.

1. **Session-attach / multiplexer management** lives in spinclass today (the
   `[session-entry]` argv templates that drive `zmx`/`posh`); the plan is for
   clown to do multiplexer attach **implicitly, per clown instance**.
2. **There is no clownfile** — clown has no per-instance config analogue to
   spinclass's sweatfile to drive that implicit attach behavior.
3. **The model is 1:1** (one spinclass session ↔ one clown agent) but needs to
   become **1-to-many** (one spinclass worktree ↔ many clown instances), and
   addressing the *group* must be possible.
4. **Chat is split today** (store = spinclass, wake = clown). It should leave
   spinclass **entirely** and become a pure clown construct.

The unifying move is an **addressing decoration**: clown identifies the
spinclass session each clown belongs to (`SPINCLASS_SESSION_ID`, which spinclass
already sets) and decorates its per-instance identity with it, so a message can
target one clown **or** an entire spinclass session. spinclass *sheds* chat and
the multiplexer templates; it *keeps* setting the decoration. This FDR redraws
the FDR-0016 Venn accordingly.

## The four pieces (overview)

| # | Feature | Today | After |
|---|---|---|---|
| 1 | multiplexer attach | spinclass `[session-entry]` argv templates exec `zmx`/`posh` | clown drives attach implicitly per instance, from a clownfile |
| 2 | clownfile | none | clown's unified per-instance config — profile + attach/mux, cascading like the sweatfile (clown-owned) |
| 3 | grouping / group-addressing | 1:1, no grouping | clown identifies the spinclass session as an addressing **decoration**; a message can target the whole group |
| 4 | chat | store=spinclass, wake=clown | **removed from spinclass entirely**; a pure clown construct |

**Dependency order** (clown side): the per-instance key derivation + the
spinclass-session decoration (both clown-owned) are the keystone for
group-addressing (3) and the chat construct (4); the clownfile (1+2) is
independent and can land in parallel. The **spinclass** work is mostly
*removal* — delete chat, drop the mux defaults — plus *keep setting the
decoration env var* (no new code).

## Addressing: per-clown identity + the spinclass-session decoration

Two clown-owned mechanisms, together, give clown two addressing granularities.
spinclass consumes neither for chat — it only **supplies the decoration value**.

**Per-instance identity (the keystone).** Today clown snapshots
`SPINCLASS_SESSION_ID → CLOWN_SESSION_ID` once at boot, so **every** clown in a
worktree collapses to the **same** key → the **same** channel and cannot be
addressed individually (this is the #118 ambiguity). The fix (clown-owned,
approved): each clown derives a **unique** `CLOWN_SESSION_ID` reusing **clown's
existing per-instance claude-resume identifier** (the `--session-id <uuid>`
clown already mints when it launches/resumes a claude instance) as the basis —
not a new scheme;
`clown job whoami` reports the per-instance key. Supersedes the snapshot; ties
to clown#135/#136.

**The spinclass-session decoration.** `SPINCLASS_SESSION_ID` (= `<repo>/<branch>`
or `<repo>/<rand>`) stays the **group label**. spinclass sets it in every
clown's env — as it already does for identity (`executor.SessionExecutor`,
`spawn.workerEnv`). clown reads it as a **decoration** on each clown's
per-instance identity, so it can fan a message out to every clown carrying the
same decoration. This is how "send to an entire spinclass session" works, and
it requires **no spinclass registry** — the decoration travels in the env each
clown already inherits.

**Derived channels (clown-owned, RFC-0013).** clown realizes both granularities
as channels each clown's monitor subscribes to, with no enumeration: (1) the
per-instance channel `ChannelID(per-instance-key)` for direct messages, (2) the
**group** channel `ChannelID(SPINCLASS_SESSION_ID)` — *derived* purely from the
decoration in the env, so every clown sharing the decoration subscribes to the
same channel — and (3) the global broadcast channel. Group-send is a write to
the derived group channel; pub/sub fans it out. Zero registry, zero
enumeration, zero spinclass involvement beyond setting the env var.

So clown can target: **one clown** (per-instance key) or **the whole spinclass
session** (the derived group channel). spinclass's job is to keep setting
`SPINCLASS_SESSION_ID` correctly and to **not** leak a parent's into a child
(FDR-0016 / #169) — that hygiene is what keeps the decoration accurate.

## The Venn redraw (extends FDR-0016)

FDR-0016's Venn ("clown owns transport+addressing; spinclass owns
worktree+orchestration; the seam is the session key + SessionStart/End +
liveness join") still holds. This FDR **moves two whole features to clown** and
**narrows the seam to the decoration**:

**Moves to clown (was spinclass or shared):**
- **multiplexer attach/lifecycle** — the `zmx`/`posh` invocation; clown drives
  it implicitly per instance from a clownfile (Piece 1+2);
- **chat, in full** — store, transport, addressing, sender-identity, cursor,
  *and the grouping/registry*. Not a migration-with-residue: spinclass keeps
  **no** chat surface (Piece 4);
- **group-addressing** — clown builds the group from the decoration each clown
  carries; there is no spinclass-side group index (Piece 3).

**Stays spinclass (unchanged from FDR-0016):**
- git **worktree lifecycle** (create / merge / clean / fork);
- the **session-key value origin** (`SPINCLASS_SESSION_ID`) and identity-env
  injection — now also the **addressing decoration** clown consumes;
- `sc list` and PID-based liveness of its own sessions + SessionStart/SessionEnd
  materialization (FDR-0014). These are worktree-manager core, not chat.

**The seam (narrowed):**
- `SPINCLASS_SESSION_ID` — spinclass sets it (the decoration value origin),
  clown derives per-instance identity from it and decorates addressing with it.
  This single env var is the whole chat-related seam now.

## Piece 1 — multiplexer attach moves to clown

### spinclass current state (the seam is already clean)

spinclass hardcodes nothing about `zmx`/`posh`. **All** multiplexer coupling
flows through `[session-entry]` argv templates + env vars:

- spawn default: `["zmx", "attach", "{id}", "--detach", "{entry}"]`
  (`internal/sweatfile/sweatfile.go:346`; `zmx run` was the wrong shape — #145);
- `start` / `resume` default to `$SHELL`; remote attach default
  `["ssh", "-t", "{ssh}", "spinclass", "resume", "{id}"]`
  (`internal/remote/remote.go`);
- `posh` is only a *candidate future* remote-worker path (FDR-0011, blocked on
  posh#66/#67) — not wired today.

spinclass **owns**: session-key derivation, entrypoint templating + identity
env (`SPINCLASS_*`, `TMPDIR`/`CLAUDE_CODE_TMPDIR`), PID + optional
`liveness-probe`. spinclass does **not** own multiplexer session lifecycle or
ids — the multiplexer is fully opaque (no mux-id field in `session.State`;
`internal/session/session.go`).

### After (confirmed seam)

spinclass still **creates the worktree** and **sets the key value**
(`SPINCLASS_SESSION_ID`) + identity env. clown, on boot, **reads the clownfile
and wraps itself in the multiplexer** — the "implicit per instance" part. The
clownfile subsumes the `[session-entry]` mux templates; spinclass **drops the
mux defaults**. Disposition of the leftover `[session-entry]` orchestration
glue (finalize against the clownfile schema):

- `liveness-probe` — **stays spinclass** (backs `session.State` liveness);
- `tombstone-retention` — **stays spinclass** (worktree/session lifecycle);
- `resume-title` (OSC-2) — **moves** to clown (clown drives resume attach, so
  it sets the title);
- **remote attach** — **stays spinclass, unchanged** (`internal/remote` /
  FDR-0011). This rescope is the **local** `zmx`/`posh` implicit-per-instance
  attach only; the `clown resume` remote template floated earlier was an
  extrapolation, not a decision. Migrating the remote path is a separate,
  bigger call **escalated to Sasha** and deliberately out of scope here.

## Piece 2 — clownfile (clown-owned)

> **Normative in clown RFC-0013.** Summarized here for the consumer-side seam;
> the schema is clown's to set.

The clownfile is **clown's unified per-instance config** — clown's half of the
config split, structurally analogous to the sweatfile (`clownfile : clown ::
sweatfile : spinclass`), **not** a sweatfile replacement. It holds **both**:
- clown's **profile** fields (provider / backend / model / env — clown's
  planned `--profile` / `profiles.toml` work folds into the clownfile, it is
  **not** a separate orthogonal config); and
- the **attach/mux** config (the `[session-entry]` part moving from spinclass).

It uses the **same cascading/hierarchical discovery as the sweatfile** (root →
repo, layered overrides) — same shape, clown-owned. spinclass's sweatfile keeps
everything else (`git-excludes`, `claude-allow`, `envrc-directives`,
`allowed-mcps`, `[[mcps]]`, `[hooks]`, worktree lifecycle) and **drops the
`[session-entry]` mux defaults**.

Proposed `[attach]` shape (clown-owned, refine together; the profile fields
live alongside it in the same file): TOML, an `[attach]` section keyed by mode,
same `{id}`/`{entry}`/`{ssh}` substitution semantics as today:

```toml
[attach]
multiplexer = "zmx"        # zmx | none   (posh later: FDR-0011 / posh#66-67)
spawn  = ["zmx", "attach", "{id}", "--detach", "{entry}"]
resume = ["zmx", "attach", "{id}", "{entry}"]
start  = ["{entry}"]
# remote attach is NOT in the clownfile — it stays spinclass (FDR-0011)
```

clown reads it on boot and re-execs / attaches **itself** under the
multiplexer, instead of spinclass invoking the template. The profile fields
(provider / backend / model / env) live in the same clownfile and cascade the
same way.

## Piece 3 — group-addressing via the spinclass-session decoration

### spinclass current state (1:1, no grouping)

`session.State` has no multiplexer-id field; nothing forbids N clowns in a
worktree but nothing creates them, and there is no way to address a group. The
#118 shared-checkout caveat (`currentSessionKey()` resolves to "the first live
implicit session") is the same collapse: N instances → one identity.

### After (clown owns the grouping; spinclass supplies the decoration)

There is **no spinclass-side group registry**. Grouping is a clown construct
built from the decoration each clown carries:

- spinclass sets `SPINCLASS_SESSION_ID` in every clown's env (existing
  behavior, no new code);
- each clown derives its **unique** per-instance key (keystone) and carries the
  `SPINCLASS_SESSION_ID` **decoration**;
- clown groups by the decoration and can address the whole spinclass session.

spinclass's `sc list` continues to enumerate its own worktree sessions (core,
unchanged).

**Deferred — spinclass-consumes-presence (clown#137).** spinclass *reading*
clown's presence index (RFC-0013 §3.3) to surface, in `sc list`, the set of
clowns running within one of its sessions is a **future pass**, not rescope
core. Crucially, **addressing needs no presence read**: it is derivation-based
(per-instance / derived-group / broadcast channels), so the core — per-instance
identity + derived-channel chat — ships without spinclass ever enumerating the
presence index. Tracked as clown#137; explicitly **not** a spinclass chat
registry.

## Piece 4 — chat removed from spinclass entirely

### spinclass current state (store=spinclass, wake=clown)

- **Store (spinclass):** `internal/chat/` — flat JSON message files
  `<RFC3339Nano>-<sha256[:8]>.json` under `$XDG_STATE_HOME/spinclass/chatroom/`,
  schema `{from, to, timestamp, subject, body}`; per-session read cursor
  `.cursor-<sha256(key)[:8]>.json`.
- **Send (spinclass):** `chat-send` dual-writes — store first, then a wake emit
  `clown job message --target <key> --from <sender> --source spinclass …`
  (`internal/chat/wake.go`, `internal/clown/clown.go`).
- **Receive (clown):** clown's job-watch monitor is the push path; `chat-read`
  polling is the pull fallback.
- **Recipients (spinclass):** `chat-list-sessions` via `session.ListAll`.

### After (deleted — chat is a clown construct)

The entire chat surface **leaves spinclass**:

- **delete** `internal/chat/` (store, cursor, wake emit, subject handling);
- **remove** the `chat-send` / `chat-read` / `chat-list-sessions` MCP tools and
  their `currentSessionKey` chat role from `cmd/spinclass/commands_mcp_only.go`;
- **drop** the `chatroom/` store and the FDR-0009 / #16 storage model;
- clown owns chat in full — store (journal), transport, addressing (per-instance
  channel + the derived group channel `ChannelID(SPINCLASS_SESSION_ID)`),
  sender-identity, cursor, and the recipient listing. The listing is a clown
  **presence index**: each clown registers `{per-instance key, SPINCLASS_SESSION_ID
  decoration, description}` on SessionStart; clown's chat-list reads it grouped
  by decoration — replacing `chat-list-sessions`.

What spinclass retains for chat is **only the decoration**:
`SPINCLASS_SESSION_ID` set in each clown's env, kept accurate by the FDR-0016 /
#169 no-leak hygiene. This is what dissolves #118 — each clown has a unique
identity and a correct group decoration, so neither a directed nor a group send
can resolve to the wrong target.

## Remaining clown-owned design (clown RFC-0013)

Scope and inputs are settled. What the clown side owns (now filed in RFC-0013):

1. the **per-instance key derivation** (basis: clown's existing claude-resume
   identifier; supersedes the SPINCLASS→CLOWN snapshot; ties to clown#135/#136);
2. the **clownfile schema** — unified profile + attach/mux config, cascading
   like the sweatfile, with the planned `--profile`/`profiles.toml` folded in
   (Pieces 1+2);
3. the **chat construct** in full — store/journal, the `chat-*` tool surface,
   the per-reader cursor, the **derived group channels**
   (`ChannelID(SPINCLASS_SESSION_ID)`), and the **presence index** that replaces
   `chat-list-sessions` (Pieces 3+4).

This is **clown RFC-0013** (filed on clown master, status proposed; it
references FDR-0017 — the mutual cross-ref is complete). The spinclass-side
**presence read** (consuming RFC-0013 §3.3) is deferred to clown#137.

## Limitations / non-goals

- This is the spinclass side; clown's transport/identity/attach/chat
  *semantics* are normative in clown RFC-0013. Where they disagree, RFC-0013
  wins and this doc is wrong.
- **No clown = no chat, by design.** chat rides clown's job channel, so wherever
  chat is unavailable — `CLOWN_DISABLE_JOB_WAKEUP=1` **or** no clown binary —
  there is no chat. (An earlier "decouple store from wake" refinement, proposed
  as RFC-0009 §8, is **retracted as moot**: with chat a pure clown construct
  there is no separate spinclass store to preserve.) The spinclass-local
  `chatroom/` store is removed regardless; FDR-0009's survive-without-clown
  store goes with it. A no-clown chat fallback, if ever wanted, is a clown
  concern, not a spinclass one.
- `sc list` and spinclass's own session/liveness tracking are **unaffected** —
  they are worktree-manager core, not chat. Only the chat surface and the
  multiplexer defaults leave.
- `posh` is not wired today; this FDR does not block on posh#66/#67.
- Migration sequencing (when the chat-deletion lands relative to clown's chat
  construct going live; the mixed-fleet window) is deferred to the
  `proposed → experimental` transition. The dependency order (keystone +
  decoration first, then spinclass deletes chat) is fixed.

## More Information

- **clown RFC-0013** — *Clownfile, per-instance identity, and chat ownership*
  (`docs/rfcs/0013-clownfile-per-instance-identity-and-chat-ownership.md` on
  clown master, status proposed; builds on **clown RFC-0012**; references
  FDR-0017) — the clown-owned normative half: clownfile schema (unified
  profile+attach), per-instance key derivation, the chat construct (derived
  group channels + presence index). clown#137 tracks the deferred spinclass
  presence-read (RFC-0013 §3.3).
- **FDR-0016** — the prior spinclass-side half of the clown ⇆ spinclass
  contract (identity / addressing / liveness); this FDR redraws its Venn.
- **FDR-0009** — cross-session chat; its spinclass store + `chat-*` surface are
  **removed** by Piece 4 (chat becomes a clown construct). FDR-0009 should be
  marked superseded by FDR-0017 on the spinclass side once Piece 4 lands.
- FDR-0014 (implicit sessions / SessionStart-End materialization), FDR-0011
  (remote sessions — **unchanged**; remote attach stays spinclass, migration
  out of scope / escalated to Sasha), FDR-0010 (clown job-wakeup producer),
  FDR-0006 (spawn/fork lineage — the local multiplexer attach templates Piece 1
  moves).
- spinclass#118 (shared-checkout sender ambiguity — dissolved by the keystone +
  decoration), spinclass#169 (canonical-id addressing / no-leak hygiene),
  spinclass#170 (chat → journal convergence — subsumed by Piece 4's full
  deletion), spinclass#145 (`zmx attach --detach`); clown#135 (`whoami`),
  clown#136 (env-hygiene).
- `internal/sweatfile/sweatfile.go` (`[session-entry]` defaults),
  `internal/executor/session.go` (SessionExecutor attach + env — sets the
  decoration), `internal/spawn/` (spawn/fork env — sets the decoration),
  `internal/chat/` (**to be deleted**), `internal/remote/` (remote attach —
  **unchanged**, stays spinclass), `cmd/spinclass/commands_mcp_only.go`
  (`currentSessionKey` + the `chat-*` tools — **to be removed**).

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.4.1+5123117
([commit](https://github.com/amarbel-llc/clown/commit/5123117c7a489c22c1842f57ba470c922dfd4e7a));
spinclass-side half of the two-doc clown ⇆ spinclass rescope.
