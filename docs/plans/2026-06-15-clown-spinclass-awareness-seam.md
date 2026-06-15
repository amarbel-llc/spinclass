# clown ↔ spinclass awareness seam — spinclass-side working draft

> **Status:** working draft (2026-06-15). spinclass's half of the **co-authored
> clown RFC-0014** ("clown ↔ spinclass awareness seam", clown
> `docs/rfcs/0014-clown-spinclass-awareness-seam.md`, status: proposed).
> Boundaries ACK'd; this lands as a **companion** export contract — RFC-0014 §6
> states the decoration var *names* normatively and references this doc for
> producer-side set/strip semantics; ratified via spinclass FDR-0017. Owner:
> clown/fond-locust (spinclass side). Peer: clown/deft-elm (clown side).

## Decision that frames this (Sasha, 2026-06-15)

**spinclass exits the multiplexing business entirely.** A spinclass session is
*just a worktree + identity env + a place to run shells*; inside it you run any
number of shells and clowns. **clown owns all attach / multiplexing / grouping**
(its `[attach]` self-wrap, its `group-id`), including the **detached spawn**
executor. spinclass ships **no multiplexer templates**.

Consequences (all confirmed):
1. `sc start` / `sc resume` exec `$SHELL` in the worktree (identity env set);
   they no longer wrap in a multiplexer. Reattachment to a live agent is
   clown's concern (clown's `[attach]` keeps each clown reattachable). Native
   `sc`-side reattach convenience hooks are a **future** exploration, not v1.
2. `liveness-probe` **stays a spinclass concern** but must be **decoupled +
   clown-compatible** (see §3 — it consumes clown presence rather than grepping
   a spinclass-owned mux).
3. `start`/`resume` default to `$SHELL` (already spinclass's baked default);
   users drop `[session-entry]` mux config from their personal sweatfile.

This **dissolves the double-wrap hazard**: once spinclass stops wrapping, clown's
`[attach]` is the *sole* multiplexer. **No flag-day** (RFC-0014 §7): the
`group-id` binding ships in clown's burned-in default clownfile, so the clown
release that removes the hardcoded `SPINCLASS_SESSION_ID` read *also* supplies the
binding atomically — no operator/eng-root config must lead it. clown#146 (the
`[attach]` self-wrap) already shipped, so spinclass can drop its mux templates
without a no-wrap window.

## Ownership split (post-decision)

| Concern | Owner |
|---|---|
| Worktree create/teardown, session-state tracking, tombstone retention | spinclass |
| `SPINCLASS_*` decoration env export (§1) | spinclass |
| Session liveness (consumes clown presence) + `sc list` rendering (§3) | spinclass |
| Remote attach (`internal/remote`, FDR-0011) | spinclass (unchanged, out of scope) |
| Multiplexer attach/wrap (interactive **and** detached spawn), grouping, per-instance identity, chat | clown |
| `group-id` (config field, env-interpolated), `{group}` title, group-channel/presence keying | clown |
| Presence index (the thing spinclass consumes for liveness) | clown (clown#137) |

Principle — **lightly coupled**: neither binary hard-requires the other. The
only contract is the documented decoration env set (§1) flowing one way and the
presence index (§3) flowing the other; the *binding* is one line
(`group-id = "${SPINCLASS_SESSION_ID}"`) shipped in **clown's burned-in default
clownfile** (no operator/eng-root file required). The "no orchestrator name in
clown" rule is scoped to **code**: a config default naming
`${SPINCLASS_SESSION_ID}` is permitted because it gracefully interpolates to `""`
(ungrouped) when absent. Bare clown (no `SPINCLASS_*`) and bare spinclass
(harness ≠ clown, no presence) both still work.

## Section boundaries (ACK'd by peer, 2026-06-15)

- **spinclass drafts (this doc):** §1 export contract; §3 consumer side
  (liveness + `sc list` from presence); the ownership split above; the
  bare-spinclass-still-works guarantees.
- **clown drafts (normative wire):** `group-id` field + env interpolation +
  config-only; `{group}` title surface; group-channel/presence keying; the
  **presence index schema** (the §3 input); the **detached-spawn executor wire**
  (spawn-mode signal, prompt-return guarantee, SessionStart-fires,
  `SPINCLASS_SESSION_ID` passthrough); flag-day ordering; bare-clown-still-works.
- **Joint/normative:** the decoration env-var **names** are the contract.
  Stated normatively in the clown RFC (clown is the consumer), referencing this
  export doc.

## §1. spinclass → clown: the `SPINCLASS_*` decoration env contract

spinclass guarantees to export the following into the harness's process
environment when it launches a session (interactive via
`executor.SessionExecutor`, detached via `spawn.workerEnv`). Applied **after**
any user `[session-entry].env`, so spinclass-owned vars are authoritative and
cannot be clobbered.

| Var | Value | Notes |
|---|---|---|
| `SPINCLASS_SESSION_ID` | `<repo-dirname>/<branch>` | The session key. **The group-id source** clown interpolates. Stable per worktree. |
| `SPINCLASS_REPO` | repo dirname | The repo half of the key. |
| `SPINCLASS_BRANCH` | branch | The branch half (display hint; may be empty for detached HEAD). |
| `SPINCLASS_WORKTREE` | absolute worktree path | |
| `SPINCLASS_DESCRIPTION` | session description | Human-readable; surfaced in presence/`sc list`. |
| `TMPDIR` / `CLAUDE_CODE_TMPDIR` | `<worktree>/.tmp` | Session-scoped scratch. |

**Set/strip rules (normative):**
- spinclass-owned vars are written **last** (after user env) — authoritative.
- **#169 strip:** spinclass strips any inherited `CLOWN_SESSION_ID` /
  `CLAUDE_SESSION_ID` from the child environment, so the child harness
  re-derives its own per-instance channel from `SPINCLASS_SESSION_ID` instead of
  arming the launcher's channel. (Interactive: `executor.session.go`; detached:
  `spawn.workerEnv` via `session.StripInheritedSessionIDs`.)
- spinclass does **not** set `CLOWN_SESSION_ID` — that is clown's per-instance
  key, minted by clown (RFC-0009 §2 precedence). The decoration is the *only*
  identity spinclass exports.

**Lightly-coupled guarantee:** clown never reads `SPINCLASS_SESSION_ID` by name
**in code**; it appears only in clown's burned-in default clownfile as the
interpolation template `group-id = "${SPINCLASS_SESSION_ID}"`. A clown launched
outside spinclass sees no `SPINCLASS_*`, interpolates an empty `group-id`, and
runs ungrouped.

## §3. clown → spinclass: liveness + `sc list` consume presence

spinclass session liveness must NOT assume spinclass owns the multiplexer (it no
longer does). Instead:

- **Liveness** (`liveness-probe`, retained): in a clown-coupled deployment,
  "is this session alive" is answered by consuming clown's **presence index**
  (RFC-0014 §4 / clown#137). The index is per-instance JSON files under
  `$XDG_STATE_HOME/clown/presence/` (record:
  `{sessionKey, channelId, decoration(=group-id), description, lastSeen}`),
  readable directly or via `clown chat list` JSON. A session with group key `G`
  is **live iff** some record has `decoration == G` and a `lastSeen` within the
  **2-min staleness window** (RFC-0014 §4.2). Liveness MUST degrade a
  stale/missing record to dead/unknown — never false-alive. The probe stays
  **config-driven argv** for non-clown harnesses (the generic fallback is ours);
  the clown-aware path reads presence.
- **`sc list`** (#175 item 2): renders the 1-to-many "which clowns under this
  worktree" view from the same presence index.

Both are the **clown→spinclass** direction of the seam; they make `liveness` and
`sc list` decoupled (no spinclass-owned mux) yet clown-compatible.

## Resolved (by clown RFC-0014, 2026-06-15)

This draft's original open questions are now answered normatively in RFC-0014;
implementation is a follow-up after the RFC lands:

1. **Presence schema** → RFC-0014 §4 (consumed by §3 above).
2. **Detached-spawn wire** → RFC-0014 §5: spawn-mode signal is the hidden arg
   `--clown-attach=spawn` (`sc spawn` passes it; clown resolves `[attach].spawn`);
   prompt-return is a normative MUST (outer clown exits promptly so `cmd.Run`
   returns inside the hello deadline); the inner worker boots cwd=worktree, fires
   `SessionStart`, and clown preserves the `SPINCLASS_*` decoration env (the #169
   `CLOWN`/`CLAUDE` strip stays ours).
3. **group-id / {group} / config-only** → RFC-0014 §2–§3, confirmed verbatim.
4. **Flag-day** → ELIMINATED (RFC-0014 §7): the `group-id` binding ships in
   clown's burned-in default, so the read-removal and the binding land in one
   atomic clown release. No config-ordering dance.
