# Weekly restart of unattended confined children (14-day trip)

Date: 2026-10-26. Status: runbook; the sweep has never been run. Operator ruling
(2026-10-26): cover the trip with (b) a holder restarting each unattended confined
child about every 5 days, plus (c) `session-health` showing the unit clock. Why:
`unit-expiry-research.md` (a confined unattended unit ends at 7 days, nothing
relaunches it, nobody is woken, `restart-child-session` then refuses). Labels: R
read, I inferred, V observed live (none yet).

## Who and what is needed

- The **sweeping holder** (the coordinator) holds an accepted handle with the
  `restart` right on each child (`list-handles`; the spawner has it, a default
  grant `observe,close` does not).
- `[session-entry] restart` hand-off is set in a trusted sweatfile layer (the
  restart prerequisites in `first-live-restart-unattended-runbook.md`).
- The sweeping holder is **unconfined** (a confined caller is refused by
  `restart-child-session`).

## The sweep (every ~5 days; the operator's ruling)

1. `session-health` with no arguments: one line per held session. For each confined
   child look at the tail: `unit age Xh, Yh left of 7d` is fine; `UNIT EXPIRES SOON:
   <36h left` or `UNIT PAST` means restart it now; `UNIT EXITED (code N ...)`
   means it is already gone (see "Already expired"). The age is read from the unit
   record's modification time (R/I, not checked against a live unit); the 7 days is
   clown's `RuntimeMaxSec=604800`, assumed for every confined unit.
2. Skip a child whose verdict is busy with a merge/check job running: the restart
   is refused while a job runs (retry after it ends).
3. Restart each due child. **The command shape** (the MCP tool, never `sc` in Bash,
   which the hook denies):

   ```
   restart-child-session  child="<repo>/<branch>"  reason="weekly unit refresh (7-day RuntimeMaxSec)"
   ```

   `child` is the session key or the worktree directory name exactly as `session-health`
   or `sc list` prints it. **Do not pass `force`** (always-ask, so denied under
   dontAsk, and it kills the current turn and any job). The tool queues the request
   and runs the operator's hand-off (`clown restart --session <principal> --when idle
   --reason ...`); a success reply means queued, not restarted.
4. **Idle children need no nudge now (V, 2026-10-26).** clown#327 (idle-request
   fix) is deployed: a request on an idle child was consumed by the preflight within
   a second and the child restarted and returned (circus/light-beech, 38 s from
   verdict to return). Before #327 an idle child ended no turn, so clown's idle runner
   never asked the preflight (clown logged "exiting with a restart queued and never
   asked about" for circus/solid-elm). That holds on clown >= d3645fac9ff1; on an
   older clown build, or if a child has not restarted within a minute or two, send
   it one short message with `chat_send` (JID from `chat_list`) so a turn ends.
5. Confirm: the requester gets "<key> is restarting now" and later "<key> is back
   after its restart" (the wakes fixed in 8d9ae0c; V pending a deploy). Then
   `session-health` again: the child's `unit age` should have dropped to minutes.

## What a restart before expiry does to the clock

I (never observed): the relaunch is a new `systemd-run` launch, and `RuntimeMaxSec`
is a property of the unit created at that launch (clown `internal/agentscope/unit.go`
:262 appends it on each Launch), with the old unit stopped first. So the 7 days start
again, and the record is rewritten, so `session-health`'s `unit age` should reset.
**The step-5 check is the live verification**: if `unit age` does not drop after the
restart, the assumption is wrong; tell the operator, because the whole sweep rests
on it.

## Already expired (`UNIT EXITED`)

`restart-child-session` refuses: the hand-off fails because no frontend serves the
control socket (R). Only the operator can revive it: `sc resume <target>` at a
terminal (a new launch, sticky confined resume). Until then the session is inactive
and nobody is woken. This is why the sweep must come before day 7, not after.

## Profile changes and the spinclass library (2026-10-27)

A confined session is pinned to the profile digest it launched with, and spinclass
computes that pin from the clown library LINKED in its own flake.lock. After a
clown change to a builtin profile (for example canary-spinclass in clown#331):
confined SPAWNS fail at clown's digest check until spinclass's `clown` input is
bumped and deployed (and the spawning session restarted), and RESTARTS of already
running confined sessions are cancelled by clown's profile gate (`restart CANCELLED
by clown (profile-gate)` wake, 5425ebf) until an operator re-pins. A sweep that
shows several such cancellations right after a clown deploy is this, not a hung
restart. Details and the landing order: `2026-10-27-profile-digest-coupling.md`.

## Cadence

Day 0 spawn/restart, sweep at day 5, again at day 10; a child restarted for any
other reason starts its own 7 days (the clock is per unit). Sweep more often if a
child is long-lived and the coordinator cannot be sure of its schedule: a restart
costs one turn end and a relaunch.
