# Stop and start a session's agent as passable rights

Status: design, not built. Builds on FDR 0037 (session restart) and FDR 0032
(principals and handles). A later piece, parking an idle agent while its merge
waits, needs the `park` right named here and is not designed here.

## Problem

A supervisor can restart a session it holds the `restart` right on, but it
cannot stop one and leave it stopped, nor start a stopped one again. A stopped
session costs no memory; today the only way to stop an agent is from a
terminal.

## Interface

### Rights

Three new object rights join `session.HandleRightNames`:

- `stop`: ask for the session's agent to be stopped and left stopped.
- `start`: start a stopped session's agent again.
- `park`: reserved for the parking piece; recorded and grantable now, checked
  by nothing yet.

The spawner holds them, as it holds `restart`. They are granted, narrowed and
released through the existing handle tools; the default grant
(`observe,close`) does not include them.

### Tools

| Tool | Needs | Effect |
|---|---|---|
| `stop-child-session child [when] [reason]` | `stop` on the child | records a stop request |
| `stop-this-session [when] [reason]` | none (one's own session) | records a stop request |
| `start-child-session child [reason]` | `start` on the child | runs the start hand-off |
| `cancel-restart` | as today | also withdraws a queued stop |

`when` is `idle` (default) or `now`; `now` always asks the human, as a forced
restart does.

### State

A stop request reuses `RestartRequest` with a new `Kind` field (`restart`,
the default when absent, or `stop`), so one request is queued at a time and
cancel, the refusals and the in-flight window apply to both unchanged. The
preflight that consumes a stop writes the in-flight stamp as for a restart
plus `StoppingKind = stop`; the stopped mark is `StoppedAt` (with
`StoppedBy`, `StoppedReason`). `sc list` shows `stopped`. A start clears
`StoppedAt` once the relaunched session's SessionStart reports back
(`StartedAt` after `StoppedAt`).

### The preflight verdict (the contract with clown)

`restart-preflight` gains the verdict `stop`:

    {"verdict":"stop","reason":"...","when":"idle|now"}

`posture` and `mode` are absent on a stop. Clown stops the provider and does
not relaunch. Clown names the verdicts it understands in
`CLOWN_RESTART_VERDICTS` (comma separated, exact tokens) in the environment of
the preflight call; spinclass issues `stop` only when the list names it and
otherwise answers `refused` ("this clown cannot stop on request"), which
clears the request and tells the holders like any refusal. A stop clown drops
because a turn started is re-armed by `restart-preflight --rearm`, which
re-emits the same kind (a stop stays a stop), with restart's bounds; a cancel
inside the in-flight window makes the re-arm answer `none` (#497).

The checks before a stop are the restart checks: a running merge or check job
refuses an idle stop; an implicit session, a closed or closing one is `none`.

### Start

`start-child-session` runs the trusted `[session-entry].start` hand-off
(operator layers only, read live, like `[session-entry].restart`), with the
session's key, worktree and principal expanded. It must relaunch the same
principal (the same clown session id) detached, with the conversation resumed:
handles, buffered attestations and the job journal are keyed by the
principal, and a new one would orphan the session's holders. Starting a
session that is not stopped is refused.

### Start carries one notice

A start always passes a first message: the hand-off's message operand is
required, and `start-child-session` refuses an empty one. The hand-off uses
clown's argv form, which resumes this principal and records the message as the
spawn brief, so a later restart does not replay it:
`CLOWN_SESSION_ID=<id> clown --clown-attach=spawn [--profile P] --provider claude -- --resume <id> "<message>"`.
(The key form, `clown resume <key>`, picks the newest conversation under the
key rather than this principal, so it is not used.)
The resumed agent reads exactly one notice and goes on. Draft, for a plain
stop and for the later parked merge:

    You were stopped at <stopped-at> by <who> (<reason>) and started again at
    <now> by <who>. Nothing else changed in your session; carry on from where
    you were.

    You were parked at <parked-at> while your merge <job-id> waited for <the
    merge lock | a host gate slot>, and started again when it ended at <ended-at>:
    <outcome line, e.g. "merge succeeded; push origin/master <sha>">. The job's
    full result: job_status <job-id>. Carry on from where you were.

### Whose state this is

Stopped, parked and closed are spinclass state, in the session record
(`StoppedAt`, the parking mark later, the tombstone). Clown keeps none of it:
it obeys the preflight verdict, stops or relaunches the provider, and reports
back through the status file and SessionStart. A question such as "is this
session stopped" is answered from the session record only.

### Who holds `park`

Coordinators: the spawner holds `park` with the other rights, and grants it
like them. The parking piece adds the queue-wide defaults, writable by a holder
at coordinator level and by the operator, journalled; parking is per session,
default off, and rolled out one session at a time (see the parking design,
later).

## Open questions for the operator

1. Should `stop-this-session` need no right (like `restart-this-session`), or
   should a session be unable to stop itself?
2. Should `start` of a stopped session also be possible for its own holders
   without the `start` right, or strictly by the right?
3. Is `now` (kill the current turn) wanted for stop at all, or only `idle`?
4. Should a stop record an expiry, after which the watchdog starts the session
   again, or does a stopped session stay stopped until someone starts it?

## Relaunch cost

Relaunch cost today is a full re-read; a separate prompt change may remove
it; parking stays on or off with no threshold.

## Parking, recorded for later (not built, reviewed after P1)

- A second merge-job runner, in its own process, coexists with the in-serve
  runner behind a per-session switch; the first is not replaced.
- While parking is on for a session, async merges are disabled for it; only
  the synchronous merge parks.
- Host reboots are out of scope.

## Not covered

- Parking (stop while a merge waits, start on its completion): needs the merge
  job moved out of `spinclass serve` first, and is reviewed after this.
