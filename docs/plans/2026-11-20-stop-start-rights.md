# Stop and start a session's agent as passable rights

Status: built (stop, start, the stop-now allow-list); parking not built.
Builds on FDR 0037 (session restart) and FDR 0032
(principals and handles). A later piece, parking an idle agent while its merge
waits, needs the `park` right named here and is not designed here.

## Problem

A supervisor can restart a session it holds the `restart` right on, but it
cannot stop one and leave it stopped, nor start a stopped one again. A stopped
session costs no memory; today the only way to stop an agent is from a
terminal.

## Interface

### Rights

Four new object rights join `session.HandleRightNames`:

- `stop`: ask for the session's agent to be stopped and left stopped.
- `stop-now`: ask for it to be stopped now, killing the current turn.
- `start`: start a stopped session's agent again.
- `park`: reserved for the parking piece; recorded and grantable now, checked
  by nothing yet.

The spawner holds them, as it holds `restart`. They are granted, narrowed and
released through the existing handle tools; the default grant
(`observe,close`) does not include them.

**Every new right needs the re-stamp (ruled).** A spawn records its
spawner's rights as an explicit set, the vocabulary as it stood that day, and
explicit sets stay: a record never widens on its own. So a right added to
`session.HandleRightNames` reaches only handles minted afterwards, and every
existing handle gains it only when the operator runs `sc handle-rights`
(TTY-only, journalled, adds only). The change that adds a right therefore
also tells the operator to re-stamp. For the stop and start rights, plus
`jobs`, which older spawner records also lack:

    sc handle-rights --all-on-host --spawners --add jobs,stop,stop-now,start,park --dry-run
    sc handle-rights --all-on-host --spawners --add jobs,stop,stop-now,start,park

`--spawners` touches only each session's recorded spawner, still a holder and
not released, and skips a spawner with no explicit entry (the implicit rule
already gives it every current right). Other holders are re-stamped one at a
time with `--holder`.

### Tools

| Tool | Needs | Effect |
|---|---|---|
| `stop-child-session child [when] [reason]` | `stop` on the child | records a stop request |
| `stop-this-session [when] [reason]` | none (one's own session) | records a stop request |
| `start-child-session child [reason]` | `start` on the child | runs the start hand-off |
| `cancel-restart` | as today | also withdraws a queued stop |
| `sc stop SESSION...\|--all-on-host [--except S...] [--now] [--reason R]` | the operator at a terminal, no handle | records a stop request per session, after one prompt |
| `sc start-stopped SESSION... --message M` | the operator at a terminal, no handle | runs the start hand-off per session, after one prompt |

The two operator verbs (ruled: the most privileged access is by CLI first)
share the request path of the tools above. `sc start` is taken (it creates a
session), hence `start-stopped`. A target is a session; the form
`SESSION@<principal>`, one clown of a session that runs more than one, is
reserved and refused today, so per-clown addressing can be added without
changing what a session target means.

`when` is `idle` (default) or `now`; see Decided, item 3, for who may stop
`now` and when a human is asked.

### State

A stop request reuses `RestartRequest` with a new `Kind` field (`restart`,
the default when absent, or `stop`), so one request is queued at a time and
cancel, the refusals and the in-flight window apply to both unchanged. The
preflight that consumes a stop writes the in-flight stamp as for a restart
plus `StoppingKind = stop`; the stopped mark is `StoppedAt` (with
`StoppedBy`, `StoppedReason`). A start clears the stopped mark once its
hand-off has succeeded; the relaunched session's SessionStart then stamps the
existing return mark (`RestartedAt`), which tells the holders it is back. Not
built: an `sc list` column showing stopped.

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

`start-child-session` runs the trusted `[session-entry].start-handoff`
(operator layers only, read live, like `[session-entry].restart`; the name
`start` is taken by the session's own start entrypoint) in the session's
worktree, with `{principal}`, `{session-key}` and `{message}` expanded. It must relaunch the same
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

## Decided

1. A session may stop itself with no right (`stop-this-session`, like
   `restart-this-session`).
2. `start` is strictly by the `start` right; holding another right does not
   let a holder start a stopped session.
3. Stop `now` (kill the current turn) is its own extended right, `stop-now`,
   granted like the others; `stop-child-session` with `when=now` needs it, and
   `stop-this-session` with `when=now` needs nothing more than item 1. Whether
   a human is asked is separate and operator-only: a trusted
   `[[permissions.stop-now-allow]]` list (layers above the repo only), with
   entries naming one principal or one session key, an optional expiry and a
   note, works as `[[permissions.spawn-allow]]` does. A caller listed there
   stops now without a prompt, each use journalled and refused if the journal
   cannot be written; any other `now` always asks the human, as a forced
   restart does.
4. No expiry: a stopped session stays stopped until someone starts it.

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
