---
status: proposed
date: 2026-06-13
promotion-criteria: |
  proposed -> accepted: the paired clown RFC is filed and the Venn below
  matches it verbatim; #169 Phase 1 (canonical-id addressing) and the liveness
  reconciliation land; a directed chat-send wakes every live session
  (sc start / resume / spawn) for ~1 week with no "selective by session" drops.
---

# spinclass ⇆ clown: session identity, addressing, and liveness (consumer side)

> **Draft.** This is the spinclass-side half of a two-document contract. The
> canonical/normative half is **clown RFC-0012** (owned by clown; see also
> clown#135/#136). The **Venn boundary** section below MUST read consistently
> with that RFC; treat any divergence as a doc bug to reconcile, not a design
> choice.

## Problem Statement

spinclass and clown each maintain a notion of "a session," and the seam between
them was never written down — only discovered, repeatedly and painfully:

- A spawned worker armed the **driver's** job-wakeup channel because spinclass
  leaked the driver's `CLOWN_SESSION_ID` through `os.Environ()`; directed chat
  wakes to the worker were silently dropped (#169, fixed Phase 0).
- `sc list` showed a dead worker as `active` because spinclass's attach-PID
  liveness went stale while nothing reconciled it (#153; observed live as
  moxy/cool-myrtle).
- clown cannot infer producer liveness at all (clown RFC-0009 §10): a job whose
  producer dies reports `running` forever.

These are not three bugs; they are one missing contract. Two systems derive a
session key, arm/observe a wake channel, and judge liveness — and they only
agreed by luck (clown's boot snapshot of `SPINCLASS_SESSION_ID`). This document
fixes the spinclass side of that contract; the clown RFC fixes clown's.

## The Venn boundary

The single source of most of the pain is unclear ownership. The boundary
(confirm against the clown RFC):

**clown owns** — the *transport and addressing* layer:
- the message store (the per-channel journal) — single source of truth for
  job/chat records;
- wake/delivery (the job-watch monitor) and the lossy nudge;
- the **addressing primitive** (not the key's *value*): the RFC-0009 §2
  resolution precedence (`CLOWN_SESSION_ID > SPINCLASS_SESSION_ID >
  CLAUDE_SESSION_ID > generated`) and the `ChannelID = sha256(key)[:16]`
  derivation. clown owns the *mapping/precedence/derivation*; in a spinclass
  session the key's *value* originates from `SPINCLASS_SESSION_ID` (the seam),
  so "clown owns identity" and "spinclass sets the key" are not in tension.
- job **lifecycle state as recorded** (see Liveness — record-only, never
  process-liveness inference) and the producer/consumer CLI + MCP surface;
- `clown job whoami` (clown#135) — the authoritative read of a session's
  resolved key + armed channel. clown owns it, but it sits **at the seam**: it
  is the resolver spinclass calls to populate its registry (see Addressing).

**spinclass owns** — the *worktree and orchestration* layer:
- the git worktree lifecycle (create / merge / clean / fork);
- the session **registry** and addressing-enrichment: the human-readable
  `<repo>/<branch>` key ↔ the canonical clown id, descriptions, repo/branch
  hints, surfaced in `sc list` / `chat-list-sessions`;
- harness orchestration: spawn / fork / resume, including **session-env
  construction and hygiene** (§ Addressing);
- PID-based liveness of its own sessions and the SessionStart/SessionEnd
  materialization (FDR 0014).

**Shared — the seam** (where bugs live):
- the **session key**: spinclass sets `SPINCLASS_SESSION_ID`; clown derives
  `CLOWN_SESSION_ID` from it. spinclass MUST NOT set `CLOWN_SESSION_ID` (clown
  owns derivation) and MUST NOT leak a parent's into a child (#169).
- the **SessionStart/SessionEnd hooks**: spinclass materializes session state
  in them; clown *declares* the monitor (a synthesized plugin) and the harness
  spawns it in the same process tree (arming-reliability is clown#132/#135, not
  this contract).
- **liveness determination**: clown is record-only (§10), spinclass owns
  process liveness; neither alone is authoritative (see Liveness).

## Addressing

`<repo>/<branch>` is a spinclass **display hint**, not the routing key. The
routing key is the canonical clown id. A directed wake routes iff the sender
targets the exact id the recipient's monitor armed (`ChannelID(SessionKey())`
in the *monitor's* env). spinclass therefore:

1. **never sets `CLOWN_SESSION_ID`**, and **strips an inherited one** when
   launching a child session, so the child re-derives from its own
   `SPINCLASS_SESSION_ID` (`session.StripInheritedSessionIDs`, applied in
   `spawn.workerEnv` and `executor.SessionExecutor.Attach` — #169 Phase 0,
   landed).
2. **records the canonical id** in session state and surfaces it as the chat
   address, with `<repo>/<branch>` as the hint (#169 Phase 1, planned). A
   spinclass worktree can host **several clown sessions**; identity is
   per-clown, so state is recorded per-clown id (generalizing the implicit
   `state-<rand>.json` pattern), grouped under the worktree.

For the registry value, spinclass should **call `clown job whoami`**
(clown#135) rather than capture `CLOWN_SESSION_ID` from its own process env:
the monitor's env can differ from the session's main-process env, so only
`whoami` reports the channel the monitor *actually armed*. Capture-at-state-write
is a best-effort fallback when `whoami` is unavailable. (This resolves the
Phase 1 open fork in #169 in favor of call-`whoami`-for-authority.)

## Job structure (consumer view)

spinclass rides clown's job/channel model for two things:
- **async merge/check** (FDR 0010): `clown job start|progress|done`, woken on
  terminal records.
- **cross-session chat** (FDR 0009): today spinclass keeps its own chatroom
  store and only emits a wake; #170 (Phase 2) converges the store onto clown's
  journal so the journal is the single source of truth and the addressing
  divergence cannot recur — the chat analogue of the moxy async→journal
  convergence (clown#117).

## Liveness

Three signals exist; none is sufficient alone:

| Signal | Owner | Knows | Blind to |
|---|---|---|---|
| attach PID (`state.json`) | spinclass | a tracked attach is alive | reattach via ssh/mosh; a dead-but-not-reaped PID (#153) |
| agent beacon (`agent.json`, FDR 0012) | spinclass | claude+serve running/busy in a worktree | one beacon per worktree — multiple clowns last-writer-win |
| clown channel / `whoami` | clown | the resolved session key + armed channel | producer liveness (RFC-0009 §10): a dead producer never says so |

The contract: **liveness is a join, not any single signal.** A session is
"live and addressable" when its PID is alive AND its armed channel
(`whoami`) equals `ChannelID(<its canonical key>)`. The #153 stale-`active`
class is a reconciliation gap — spinclass must reap an attach PID that is dead
even while other signals look alive, and `sc list` should not report `active`
on a dead PID. SessionStart re-fire and a SessionEnd sweep (FDR 0014) are the
materialization/teardown hooks; this FDR adds the reconciliation that the
multi-signal join implies. FDR 0012 (presence) is the display layer over these
signals and is folded in here rather than standing alone.

## Limitations / non-goals

- This is the spinclass side; clown's transport/identity/liveness *semantics*
  are normative in the clown RFC. Where they disagree, the RFC wins and this
  doc is wrong.
- `bypassPermissions` and degraded mode (no clown / `CLOWN_DISABLE_JOB_WAKEUP=1`)
  are out of scope here (covered where each surfaces).
- The chat store convergence (#170) is referenced, not specified here.

## More Information

- **clown RFC-0012** — the canonical/normative contract (references this FDR
  by number); RFC-0009/0010/0011 (job-wakeup channel, output spool, MCP
  tools); clown#135 (whoami + warn-on-divergence),
  clown#136 (env-hygiene), clown#132 (monitor auto-arm), clown#117 (clown as
  the complete job system).
- spinclass#169 (canonical-id addressing; Phase 0 env-leak fix landed),
  spinclass#170 (chat → journal convergence).
- FDR 0014 (implicit sessions / SessionStart-End materialization), FDR 0013
  (isolated build worktree), FDR 0012 (session presence — the display layer
  over the liveness signals), FDR 0010 (clown job-wakeup producer),
  FDR 0009 (cross-session chat), FDR 0006 (spawn/fork lineage), #153.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.4.1+bc5b0cd;
spinclass-side half of the two-doc spinclass⇆clown contract.
