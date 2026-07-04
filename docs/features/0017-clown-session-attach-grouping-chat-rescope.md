---
status: experimental
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

> **Experimental** — the clown keystone landed (clown `4d67a42`) and **Piece 4
> (the spinclass chat deletion) landed on master 2026-06-14** (spinclass
> `11f650b`); the spawn hello was first carved into `internal/spawnhandshake`.
> The eng env bump then deployed it, and the cutover is **verified live
> end-to-end 2026-06-14**: clown chat is delivering cross-session (a clown-chat
> message reached a spinclass session and was answered over `clown chat_send`),
> and `clown chat_list` confirms the keystone in production — unique per-instance
> UUID keys (no #118 collapse), the `SPINCLASS_SESSION_ID` decoration, the
> `SPINCLASS_DESCRIPTION` presence label, and the presence index, all rendering.
> **Update 2026-06-15:** Piece 1's design is settled — **clown RFC-0014**
> (awareness seam: `group-id` + presence) is **merged** (clown `c71c2dc`) and
> ratified here (§ RFC-0014 ratification); the framing call is **spinclass exits
> multiplexing entirely**. Remaining: the clown implementation of RFC-0014
> §2/§3/§5, then the spinclass mux-template removal + presence-wired
> liveness/`sc list` (#175); `experimental → testing` also wants the ~1-week soak
> and a multi-clown group-send exercised. This is the spinclass-side
> half of a two-document
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
  extrapolation, not a decision. **Decided (Sasha, 2026-06-14): leave as-is —
  remote sessions are not a used feature yet**, so migrating the remote path is
  out of scope here and is a separate, later call if it's ever wanted.

**Refined by RFC-0014 (2026-06-15) — see § RFC-0014 ratification.** spinclass now
exits multiplexing entirely (not merely "drops the mux defaults"): the
detached-spawn executor also moves to clown; `resume-title` moves via the
`{group}` placeholder; `liveness-probe` stays but **consumes clown presence**;
the decoration is consumed via a config-sourced `group-id`.

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

**Update (RFC-0014, 2026-06-15):** while *addressing* still needs no presence
read, **session liveness now consumes the presence index** (RFC-0014 §4.3) — so
presence-consume is on the critical path for the `liveness-probe` rework, not
purely a deferred `sc list` nicety. See § RFC-0014 ratification.

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

### After (DELETED on master 2026-06-14 — chat is a clown construct)

The entire chat surface **left spinclass**:

- **delete** `internal/chat/` (store, cursor, wake emit, subject handling);
- **remove** the `chat-send` / `chat-read` / `chat-list-sessions` MCP tools and
  their `currentSessionKey` chat role from `cmd/spinclass/commands_mcp_only.go`;
- **drop** the `chatroom/` store and the FDR-0009 / #16 storage model;
- clown owns chat in full — store (journal), transport, addressing (per-instance
  channel + the derived group channel `ChannelID(SPINCLASS_SESSION_ID)`),
  sender-identity, cursor, and the recipient listing. The listing is a clown
  **presence index**: each clown registers `{per-instance key, SPINCLASS_SESSION_ID
  decoration, description}` on SessionStart; clown's chat-list reads it grouped
  by decoration — replacing `chat-list-sessions`. The `description` source is the
  existing **`SPINCLASS_DESCRIPTION`** env (spinclass already sets it alongside
  `SPINCLASS_SESSION_ID` in `executor/session.go` + `spawn/spawn.go`) — no new
  var. It is **launch-time**: `update-this-session-description` rewrites session
  state, not a live process env, so a mid-session rename doesn't propagate.
  Both vars are absent for implicit (main-checkout) sessions spinclass doesn't
  launch — the same decoration boundary, not a new one.

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

## RFC-0014 ratification (2026-06-15)

**clown RFC-0014** ("clown ↔ spinclass awareness seam — `group-id` + presence")
is **merged to clown master** (`c71c2dc`, status: proposed) and is the normative
home for the Piece 1 attach/grouping wire. It refines several dispositions above;
where they differ, **RFC-0014 + this section win**.

**Framing decision (Sasha, 2026-06-15): spinclass exits multiplexing entirely.**
A spinclass session is *just a worktree + identity env + a place to run shells*;
inside it you run any number of shells **and** clowns. clown owns **all**
attach/multiplexing/grouping — interactive **and the detached-spawn executor**
(resolving the RFC-0013 §1.3 open question, RFC-0014 §5). spinclass ships **no
multiplexer templates**: `sc start`/`sc resume` exec `$SHELL`; `sc spawn` execs
clown in spawn mode (the hidden `--clown-attach=spawn` arg, RFC-0014 §5.1),
which is *expected* to self-detach and return promptly. spinclass no longer
**relies** on that, though: `spawn.startDetached` launches the spawn-entry in its
own session (`setsid`) with stdio redirected to `.spinclass/spawn.log` and never
waits for it to exit — readiness is proven solely by the hello handshake. So a
spawn-entry that fails to self-detach (a foregrounded `clown`/`posh` attach, a
blocking `direnv exec` devshell build) can neither wedge nor tether `sc spawn` /
`sc fork`; the command still returns and exits once the hello arrives. `sc resume`
becomes "open a shell in the worktree"; reattaching a live agent is clown's job.
**Driver-side reattach for spawns is now implemented** (direction B): the
spawned worker's SessionStart hook reports its posh session id (clown's minted
UUID == the claude `--session-id`) back in the hello, `WaitForHello` returns it,
and the `spawn-window` template can attach to the live session via the
`{attach-id}` placeholder (`posh attach {attach-id}`) instead of opening a bare
shell. General `sc resume`-side reattach of arbitrary live agents remains a
future exploration.

**The seam is bidirectional** (refines the "narrowed seam = one env var" claim):
- **spinclass → clown:** the `SPINCLASS_*` decoration env (RFC-0014 §6), sourced
  into clown's `group-id` by env interpolation in clown's **burned-in default
  clownfile** (`group-id = "${SPINCLASS_SESSION_ID}"`) — no operator/eng-root
  file required, no hardcoded read in clown (config-only, RFC-0014 §2).
- **clown → spinclass:** the **presence index** (RFC-0014 §4) that spinclass
  *consumes* for session liveness and `sc list`.

**Disposition refinements (supersede Piece 1 "After" / Piece 3 "Deferred"):**
- **`resume-title` → clown via `{group}`** (RFC-0014 §3.1): default `"{group}"`
  with `{id}` fallback, so the title shows `<repo>/<branch>`, not a minted UUID.
  spinclass drops `emitResumeTitle`/`SessionResumeTitle`/`ResumeTitle` once clown
  ships `{group}`.
- **`liveness-probe` — stays spinclass, but consumes presence** (RFC-0014 §4.3),
  not a spinclass-owned mux grep. This **promotes presence-consume from Piece 3's
  "deferred future pass" onto the critical path** for the liveness rework: live
  iff some presence record has `decoration == group-id` within the 2-min
  staleness window; degrade stale/missing → dead, never false-alive. A generic
  config-driven `liveness-probe` argv remains the non-clown fallback.
- **detached-spawn executor → clown** (RFC-0014 §5): spinclass keeps only
  worktree-create + identity-env + the hello handshake; clown's spawn-mode
  re-exec preserves the worker's `SPINCLASS_*` decoration, and the #169
  CLOWN/CLAUDE strip stays spinclass's.
- **double-wrap hazard dissolved + flag-day eliminated** (RFC-0014 §7): with
  spinclass no longer wrapping, clown's `[attach]` is the sole multiplexer; and
  because the `group-id` binding ships in clown's burned-in default, the
  read-removal + binding land in one atomic clown release — no config-ordering
  dance (supersedes the "config leads removal" framing in Migration sequencing
  step 4).

**Companion contract.** The spinclass-export half — the `SPINCLASS_*` decoration
contract (names, set/strip incl. #169) and the consumer side (liveness + `sc
list` from presence) — lives in
`docs/plans/2026-06-15-clown-spinclass-awareness-seam.md` and is **ratified by
this FDR**. RFC-0014 §6 states the var names normatively and references that doc
for producer-side semantics.

**Status.** Piece 1 design is **settled** (RFC-0014 merged; clown#146 shipped the
`[attach]` self-wrap + burned-in defaults). Remaining spinclass implementation
(gated on clown shipping RFC-0014 §2/§3/§5): drop the mux templates, pass
`--clown-attach=spawn` from `sc spawn`, and wire liveness + `sc list` onto
presence (#175 item 1 + item 2).

## Migration sequencing

The order is fixed by one rule: **the spinclass work is deletion, and nothing is
deleted before clown's replacement is live.** Steps 1–3 are clown's and unblock
the spinclass removals; the spinclass decoration foundation
(`SPINCLASS_SESSION_ID` + the #169 no-leak strip in `session/env.go`) is already
landed, so it gates nothing.

1. **clown — per-instance key** (the keystone): a unique `CLOWN_SESSION_ID` per
   instance (basis: the `--session-id` uuid). Independent. As a side effect this
   fixes the #169 directed-wake drop *by construction* — once each instance arms
   its own channel, the decoration-derived group channel is the only shared one.
2. **clown — chat construct**: the derived channels (per-instance, group
   `ChannelID(SPINCLASS_SESSION_ID)`, broadcast), the per-reader cursor, the
   `chat-*` surface, and the presence index. Depends on (1). Its only spinclass
   input — the decoration — is already in place. **The load-bearing piece is the
   journal record itself**: today's journal is *wake-only* (it carries the
   subject + a recovery hint, never the body — the body lives only in
   spinclass's `chatroom/` store, the #103 truncation guard). Journal-as-store
   therefore requires the **full body** stored distinctly from the subject-only
   wake notification (a full body in the wake line re-triggers #103).
   **Resolved (RFC-0013 §3.1, clown-side): the body lands in the RFC-0010
   per-message *spool*, not a fat record field; the journal record keeps
   `Message` = the ≤200-rune subject; record + spool = the store.** See the
   body-gap below.
3. **clown — clownfile** (attach + profile, cascading): clown reads it on boot
   and wraps itself in the multiplexer. Independent of (1)/(2); can land in
   parallel.
4. **spinclass — exit multiplexing entirely** (RFC-0014; supersedes "drop the
   mux defaults"). Drop all `[session-entry]` mux templates (start/resume →
   `$SHELL`; `sc spawn` execs clown via `--clown-attach=spawn`, clown
   self-detaches). Gated on clown shipping RFC-0014 §2/§3/§5. The **flag-day is
   eliminated** — the `group-id` binding ships in clown's burned-in default, so
   the hardcoded-read removal + binding land atomically (RFC-0014 §7); no "config
   leads removal" step. (`liveness-probe` stays but consumes presence, RFC-0014
   §4; `tombstone-retention` stays; `resume-title` → clown `{group}`.)
5. **spinclass — delete chat** (`internal/chat`, the `chat-*` tools, the
   `chatroom/` store, `currentSessionKey`'s chat role). Gated on (2) being live
   **and** the fleet having moved to clown's chat-read (see the mixed-fleet
   window). Then mark **FDR-0009 superseded by FDR-0017**.
6. **deferred — spinclass-consumes-presence** (clown#137): optional, later, for
   a per-clown group view in `sc list`. Not on the critical path.

**The body-gap (corrects an earlier over-claim).** It is *not* true that "every
sent message already reaches the journal." Today `chat-send` writes the full
message to the `chatroom/` store and emits a `clown job message` wake carrying
**only the subject** (≤200 runes) + a recovery hint — never the body
(`internal/chat/wake.go`, `internal/clown/clown.go`; the latter's header:
"the journal … is the wake layer only"). So the journal is a notification log,
not a store, until step 2 makes clown's chat-send write full bodies (to the
RFC-0010 per-message spool; the record keeps the ≤200-rune subject).

**Mixed-fleet window (the one real hazard — step 5).** The chat surface is
**per-binary** — a session reads/writes via whatever its binary serves (old →
spinclass `chatroom/`; new → clown journal). There is **no in-binary
coexistence**: the spinclass release that deletes `chat-*` ships in lockstep
with the environment gaining clown's `chat-*` (a **hard swap at the binary
boundary**), so the two never collide on the `chat-send`/`chat-read` tool names.
The window is purely *cross-binary*. The only thing lost in it is cross-binary
**body** delivery (an old-binary session and a new-binary session use different
stores, and neither reads the other's). Two ways to handle it:
- **accept the brief window** — hard-swap at rebuild, lose cross-binary bodies
  for the (short) duration; or
- **bridge it (zero-loss)** — an additive spinclass pre-step: upgrade the emit
  to write the full body to the journal *and* read from the journal *before*
  deleting `chat-*`, so both binaries share one store.

**Decided (Sasha, 2026-06-14): accept the brief window — no bridge.** Chat is
ephemeral coordination and FDR-0009 already accepted a transitional window, so
the brief cross-binary loss is acceptable; spinclass builds no dual-write
pre-step. clown is the full-body journal writer; spinclass's chatroom store +
subject wake stay untouched until the deletion. Regardless of that choice:
**delete spinclass chat last and fleet-global**, after clown's chat is the
universal path — never while an old binary is still live.

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
- Migration sequencing is no longer deferred — see § Migration sequencing. The
  dependency order (clown keystone + chat construct + clownfile → spinclass
  drops mux defaults → spinclass deletes chat) is fixed; only the rollout
  *timing* tracks clown's implementation pace.

## More Information

- **clown RFC-0013** — *Clownfile, per-instance identity, and chat ownership*
  (`docs/rfcs/0013-clownfile-per-instance-identity-and-chat-ownership.md` on
  clown master, status proposed; builds on **clown RFC-0012**; references
  FDR-0017) — the clown-owned normative half: clownfile schema (unified
  profile+attach), per-instance key derivation, the chat construct (derived
  group channels + presence index). clown#137 tracks the deferred spinclass
  presence-read (RFC-0013 §3.3).
- **clown RFC-0014** — *clown ↔ spinclass awareness seam (`group-id` + presence)*
  (clown `docs/rfcs/0014-clown-spinclass-awareness-seam.md`, **merged** clown
  `c71c2dc`, status proposed) — the normative clown half of the awareness seam:
  the config-sourced `group-id` (env-interpolated, config-only), the `{group}`
  title, the presence index schema + query contract, and the detached-spawn
  executor wire (resolves the RFC-0013 §1.3 open question). Ratified here (§
  RFC-0014 ratification).
- **spinclass companion export contract** —
  `docs/plans/2026-06-15-clown-spinclass-awareness-seam.md`: the `SPINCLASS_*`
  decoration export (names, set/strip incl. #169) and the consumer side
  (liveness + `sc list` from presence). Referenced normatively by RFC-0014 §6.
- **FDR-0016** — the prior spinclass-side half of the clown ⇆ spinclass
  contract (identity / addressing / liveness); this FDR redraws its Venn.
- **FDR-0009** — cross-session chat; its spinclass store + `chat-*` surface were
  **removed** by Piece 4 (chat is now a clown construct). FDR-0009 is marked
  **superseded by FDR-0017** (Piece 4 landed 2026-06-14).
- FDR-0014 (implicit sessions / SessionStart-End materialization), FDR-0011
  (remote sessions — **unchanged**; remote attach stays spinclass, migration
  out of scope — Sasha decided 2026-06-14: not a used feature yet),
  FDR-0010 (clown job-wakeup producer),
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
