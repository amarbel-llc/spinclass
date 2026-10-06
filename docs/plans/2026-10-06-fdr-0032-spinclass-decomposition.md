# FDR 0032: spinclass's implementation decomposition for worktree-less agents

**Goal:** Say what spinclass builds for the worktree-less agent work, and in
what order. The work has two stages, set by FDR 0032 D7:

- **v1:** the lifecycle of a worktree-less agent lives in `juggler spawn`
  (clown FDR 0019). spinclass builds almost nothing. Its job is to make the
  contract juggler must match explicit.
- **v2:** the lifecycle moves to a spinclass **unit session**, making
  spinclass the sole isolation boundary and enforcer for worktree and unit
  agents and scripts. Tracked as spinclass#354. Not started.

This file covers spinclass only. clown has its own decomposition (cited from
clown FDR 0019) and circus has its own (circus FDR 0039). Where a brief leans
on another repo's binary, it says so.

Each brief is sized for one subagent and names a model by judgement: Sonnet
for work with a clear specification, Opus where the design is not settled.

## v1: what the pebble tracer bullet needs from spinclass

Nothing in the pebble path calls spinclass. These briefs keep v2 a relocation
and not a migration (FDR 0032 D7).

1. **Sonnet. Write down the handle-record contract.** Add a normative section
   to FDR 0032's "Interface (slice 0)" giving the field meanings juggler's
   records must reproduce (D7, D12, D13):
   - a holder is a principal string;
   - a granted handle is pending and confers nothing until first use;
   - release removes only the caller;
   - rights are recorded per holder as a comma-separated list, defaulting to
     `observe,close`.

   Docs only. Verify each statement against `internal/session` before writing
   it, since this section becomes the reference clown implements against.

2. **Sonnet. Exit reasons as named constants.** `clown.EmitExitWakes` takes a
   free-form reason string, and the code uses four: `normal`, `shutdown`,
   `killed`, `crash`. Add D6's five as constants, including `failed`, and use
   them at the three existing call sites. No behaviour change: no worktree
   path emits `failed` yet.

3. **Sonnet. Manpages and tests for brief 2.** Trails it.

Parallel: 1 and 2. Then 3.

**Status (2026-10-06): done.** Brief 1 is the "Handle-record contract"
subsection of FDR 0032's slice-0 interface. Brief 2 is `clown.ExitReason`.
Brief 3 turned out empty: brief 2 carried its own tests, and no manpage or
generated doc mentions the exit reasons.

No brief depends on another repo's binary beyond ringmaster, which the
emitter already calls.

## v2: unit sessions (spinclass#354), not started

Gated on the first handle held across the two session kinds, for example a
worktree session holding a handle on a juggler agent. An outline, so the other
lanes can see the shape. Brief 4 must land before any of 5 to 9.

4. **Opus. The unit-session FDR.** Settle the record shape with no worktree
   path or branch, the liveness signal, what close does, whether resurrect
   applies, and whether a per-run root principal (FDR 0032 D2) is a unit
   session or a new group record above sessions. spinclass has no group
   concept today.

5. **Opus. Storage with no worktree.** A second persistence path keyed on
   `State.Kind`. Today every state write requires a worktree path on disk and
   the index key is a hash of that path.

6. **Opus. Liveness, close, and the surviving emitter.** Liveness from the
   unit's state, close as stop plus tombstone, and an exit-wake emitter that
   outlives the principal (D6). This brief also takes the **dead-PID sweep for
   worktree sessions**, which slice 0 deferred: both kinds get a surviving
   emitter from the same design. Depends on 5.

7. **Opus. Spawn through a systemd transient unit.** Launcher-minted identity
   (spawn does not mint the child's principal today) and a hello sender that
   needs no worktree. Depends on 5. Leans on systemd, and on `juggler run` as
   the spawn-entry, which is clown's.

8. **Sonnet. `sc list`, `sc clean` and the handle operations for the new
   kind.** Today a missing worktree reads as `abandoned`, which hides the
   session and refuses handle operations on it. Depends on 5 and 6.

9. **Sonnet. Manpages, bats lanes, flake wiring.** Trails everything.

Parallel after 4: 5 alone, then 6 and 7, then 8, then 9.

## What v2 has to replace

From a read-only survey of the code on 2026-10-06. It is the reason v1 is not
spinclass.

Reusable with no git dependency:

- principal resolution
- the grant, accept and release logic on `Holders` and `PendingHandles`
- the exit-wake emitter
- the spawn hello file format
- the `State.Kind` discriminator (only value today: `implicit`)

Worktree-bound:

- **Storage.** Read, update, remove and tombstone take a repo and a branch.
- **Liveness.** A missing worktree path resolves as `abandoned`.
- **Close.** Mostly git steps; two hard-fail without a worktree and branch.
- **Spawn.** Creates a worktree unconditionally.
- **Resurrect.** Inherently git.

No session is launched through systemd today. Spawn is a detached exec, and
the worker's PID is not tracked.
