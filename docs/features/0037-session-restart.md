---
status: experimental
date: 2026-10-24
promotion-criteria: |
  experimental -> accepted: clown ships the frontend half (the idle queue that
  runs `sc restart-preflight` and relaunches on a `restart` verdict), an
  operator's global sweatfile sets `[session-entry].restart` to it, and a
  session restarts itself and a holder restarts a child end to end, the relaunched
  session keeping its principal and not restarting again.
---

# Session restart

Spinclass #419. A session, or a holder of one, can ask for it to be stopped and
relaunched by its harness frontend: pick up a new config or tool set, shed a
wedged context, or recover a worker, without the operator at a terminal.

## Problem Statement

Spinclass owns the session record, the handle table and the sweatfile hierarchy.
It does not own the process: clown (the harness frontend) starts, supervises and
stops it. A restart needs both, and neither may do the other's job. Spinclass
cannot kill and relaunch a harness it did not start, and clown must not decide
who is allowed to ask.

## Decisions

1. **Spinclass authorizes, refuses and records; clown performs.** The two meet at
   one command, a sweatfile hand-off. Spinclass never stops a process.
2. **The hand-off is `[session-entry].restart`**, an argv with `{principal}`,
   `{session-key}`, `{when}` and `{reason}` substituted per element (one pass, so
   a reason cannot smuggle in a placeholder). It is exec'd directly, never through
   a shell, in the session's worktree, with a 15 second bound.
3. **Trusted layers only, read live.** The key is code spinclass runs, so only
   the global sweatfile and layers above the repo may set it
   (`sweatfileio.TrustedRestartHandoff`, the `url-resolver` rule, #335); the last
   trusted layer wins and an explicit `[]` clears it. `Hierarchy.Merged` never
   carries it. It is **not** pinned into session state at creation, unlike
   `start`/`resume`, so sessions that already exist get it the next time they
   restart. A restart is therefore refused, naming the key, until an operator
   configures it.
4. **`when=idle` queueing lives in clown.** Spinclass records the request with
   `when: idle` and hands off immediately; clown holds it until the session's
   current turn ends. `when: now` is what a forced restart records (decision
   8), for clown to restart on without waiting for the turn. Clown's `restart`
   accepts exactly `--when idle` or `--when now` since clown 011db3c (anything
   else, `NOW` or `"now "` included, exits 2). Until then a forced restart was
   refused up front (d974419). `cmd/spinclass/restart_contract_test.go` pins
   the hand-off against a fake of clown's verb. Clown's accepted limits for
   `now` are in clown-restart(1).
5. **`restart-preflight` is the contract between the halves.** Clown runs
   `sc restart-preflight` at the idle transition, before it restarts. Spinclass
   re-checks everything (a job may have started since the request) and consumes
   the request, so the relaunched session does not restart again.
6. **A new object right, `restart`** (FDR 0032 D13, amended). The spawner holds
   it with every other right; the default grant (`observe,close`) does not carry
   it. `restart-child-session` needs it. Restarting yourself needs no right: it is
   the session's own act, gated by the operator's hand-off.
7. **A running merge or check job refuses the restart**, at request time and
   again in the preflight. Restarting would kill the job's owner mid-landing.
8. **`force` is an operator-gated escalation** (operator ruling), never a flag an
   agent sets silently. On both restart tools, `force=true` is always-ask
   (`perms.AlwaysAsk`, enforced by the PreToolUse hook and mirrored in
   `RunCheck`, failing closed on any non-boolean, like `close-child-session`'s
   `force`); the unforced call stays auto-allowed. A forced restart records
   `when: now`, expands `{when}` to `now`, and skips the running-job obstacle at
   request time (`restartPlan(..., force)`) and again in the preflight, which
   treats a `when: now` request as already approved to kill the job. So it kills
   the session's current turn and any running merge/check job. Nothing else is
   skipped: an implicit session, a non-UUID principal, a missing hand-off and (for
   a child) the `restart` right still refuse. At a terminal (`sc restart-this-session
   --force`, `sc restart-child-session --force`) the caller is the operator, so
   nothing prompts, as for `sc close-child-session --force`.
9. **`cancel-restart` withdraws a queued request.** It only clears the recorded
   `RestartRequest`; it does not call the hand-off. The frontend drops its pending
   restart the next time `restart-preflight` answers `none`. See Interface.
10. **A 30 second countdown before the relaunch is an operator ruling owned by
    clown and posh.** Spinclass has no part in it and implements none.

## Interface

- `restart-this-session` (MCP tool, `sc restart-this-session`): optional
  `reason` (default "requested by the session itself") and `force` (decision 8:
  always-ask, when `now`, skips the job obstacle only). The caller is resolved
  like `close-this-session` (principal, frontend mapping, session). Refused, with
  nothing recorded, when no trusted hand-off is configured, a job is running, the
  session is implicit, or its principal is not UUID-shaped (the same check
  `sc resume` applies to `{principal}`, `applyRecordedPrincipal`). Otherwise it
  records `RestartRequest{RequestedAt, Requester, Reason, When}` in session state,
  runs the hand-off, and tells the agent to end its turn. If the hand-off fails,
  the request is cleared and its output returned.
- `restart-child-session` (MCP tool, `sc restart-child-session`): `child` and
  optional `reason` (default "requested by a holder") and `force` (as above; it
  does not buy the `restart` right). Refused for a confined serve caller. Authorized as `close-child-session` is (handle or spawner key,
  accept-on-first-use) plus the `restart` right. The obstacles apply to the
  child, and the hand-off is read from the child's repo hierarchy and run in the
  child's worktree. The child's principal comes from the frontend mapping, not
  from state the agent can write.
- `cancel-restart` (MCP tool, `sc cancel-restart`): optional `child`. Without it,
  cancels the caller's own session's queued restart (resolved like
  `restart-this-session`; open to a confined caller, since it only removes a
  request and needs no hand-off). With it, the same authorization as
  `restart-child-session` (confined serve caller refused, handle with
  accept-on-first-use, the `restart` right). It clears `RestartRequest` and
  writes state; nothing queued is a success saying so (idempotent). It does not
  run the hand-off: the frontend drops its pending restart when
  `restart-preflight` next returns `none`. The original requester, when it is
  neither the caller nor the session itself, is told, best effort. Auto-allowed
  by the hook: it only removes a queued request.
- `restart-preflight [--target T]` (hidden, CLI only; target defaults to the
  session of `CLOWN_SESSION_ID`). Prints exactly one JSON line and exits 0 (non-zero
  only on an internal error):

  ```
  {"verdict":"restart"|"none"|"refused","reason":"...","when":"idle|now"}
  ```

  `none`: no session, no request, an implicit or closed session. `refused`: the
  obstacles fail now; the request is cleared and the holders are told, best
  effort, through `clown.NotifyPrincipal`. A `when: now` request skips the
  running-job obstacle (the operator approved killing the job). `restart`: every check passes; the
  request is cleared (consumed). `when` is the requested timing, empty for `none`.
  The principal is resolved by `closeRunnerPrincipal`'s rules (the frontend mapping
  first, a confined session with no mapping refused), never from the request.
- `restart-preflight --peek [--target T]` (added 2026-10-25, with clown's
  restart at 720ffd7). Read-only: it never consumes, clears or writes. It prints
  the same one JSON line with `"verdict":"none"` and an added
  `"state":"closed"|"live"`. Clown calls it just before relaunching an agent it
  has already stopped, and aborts on `closed`; a failing or unparsable peek is
  logged and treated as unknown (clown's choice: it relaunches as before).
  `closed` means not found, tombstoned, abandoned, or **marked closing**: close
  sets `session.State.ClosingAt` once the close is decided and before it signals
  the agent, and clears it again if the close fails before the tombstone. Without
  the mark, a close that had signalled the agent but not yet tombstoned would
  still read as live, and a racing restart could relaunch into a worktree about
  to be removed.
- **Close and restart are ordered** (added 2026-10-25, with clown/pure-teak).
  The preflight answers `none` for a session marked closing, even with a
  request queued. A `restart` verdict consumes the request and stamps
  `session.State.RestartingAt` in one locked update (`session.Update`) that
  re-checks the closing mark, and close sets `ClosingAt` under the same lock.
  So a close that begins after the frontend took the restart is refused while
  `RestartingAt` is under `session.RestartInFlightWindow` (2 minutes: clown's
  30 s countdown plus the stop and relaunch), unless `--force`. Otherwise the
  relaunched agent's idempotent `troupe mint` could revive a principal the
  late close just revoked. The mark is never cleared, only aged out: a
  confined session's SessionStart reaches only the display copy, so it could
  not clear the frontend record.

- **Outcome reporting and the relaunch watchdog** (added 2026-10-26). A `restart`
  verdict is no longer silent:
  - *Start wake.* The preflight tells the request's requester and every other
    holder (never the restarted session's own principal), best effort through
    `clown.NotifyPrincipal`: "<key> is restarting now (<reason>, requested by
    <requester>)". A failed wake is logged to stderr and changes neither the
    verdict nor the one JSON line on stdout. The same locked update that stamps
    `RestartingAt` also records `RestartingBy` and `RestartingReason`, since the
    request itself is consumed.
  - *Return mark and back wake.* The relaunched session's SessionStart hook finds
    `RestartingAt` set and `RestartedAt` absent or older, stamps `RestartedAt` in a
    locked `session.Update`, and sends the same audience "<key> is back after its
    restart". A re-fire (resume/clear/compact) finds the mark and does nothing. A
    hook inside a confined unit does nothing: it reaches only the display copy.
  - *Watchdog.* At the `restart` verdict of an unconfined session the preflight
    arms a one-shot `systemd-run --user --on-active=<D> --unit=spinclass-restart-watch-<key>-<ts>
    <sc> restart-watch --target <key> --since <RestartingAt>` with D =
    `RestartInFlightWindow` + 3 minutes (5 minutes), outliving both the preflight
    and the restarted session; it carries `--setenv` for `XDG_STATE_HOME`, `HOME`,
    `PATH`, `CLOWN_BIN` and `RINGMASTER_BIN`, since a user manager's environment
    lacks them. A confined session gets none (its return can never be marked, so
    the watchdog would always cry wolf). A scheduling failure (no `systemd-run`)
    is a stderr warning that no watchdog is armed, never a changed verdict.
  - `restart-watch --target T --since S` (hidden, CLI only, run by the timer):
    silent (exit 0) when `RestartedAt >= S`, when the session is gone, closed or
    closing, or when a newer restart (`RestartingAt > S`) superseded it; otherwise
    it tells the requester and holders "<key> has not come back <D> after its
    restart began at <S>; check the session (sc list) and its clown log". It
    exits 0 in every handled case.

## Requirement on clown and troupe

A restart exit must not run `troupe mint-revoke`. A restart relaunches the same
principal, and revoking its credential on the way out would strip the
relaunched session of the identity the restart exists to keep. Only a real
close revokes.

## Limitations

- Nothing here performs the restart. Until clown implements the idle queue and
  the preflight call, a configured hand-off only has spinclass's half.
- The hand-off's exit status is all spinclass sees at request time; whether the
  frontend later restarts is reported through the preflight verdict. Since
  2026-10-26 the requester and holders also get a start wake, a back wake once the
  relaunched session's SessionStart marks `RestartedAt`, and a watchdog wake when
  no return is marked within 5 minutes. A confined session has no return mark and
  no watchdog. The watchdog rests on `systemd-run --user` timers and is only
  unit-tested against a captured argv; a host without a user manager arms none.
- A refusal at preflight time reaches the holders as a wake; the requesting agent
  has already ended its turn.
- The request lives in session state, which an agent in an unconfined session can
  write. It carries no authority: the preflight re-checks everything and takes the
  principal from the frontend mapping. A forged request can at worst cause a
  restart the hand-off already allowed, except that a forged `when: now` also
  skips the running-job check in the preflight (the always-ask on `force` guards
  the tool, not a hand-edited state file).
- 2026-10-25 (#422): the forced CLI forms (`sc restart-*`, `sc close-child-session
  --force`) get no Bash-side always-ask gate; instead the PreToolUse hook denies an
  agent's Bash use of non-read-only `sc` verbs, so the MCP tools are the agent
  surface and the CLI is the operator's (spinclass-sweatfile(5), `[permissions]`).

## Hung children: `session-health` and `restart-hung-child` (2026-10-26)

A child stuck in a turn never goes idle, so the idle restart never fires and the
`force` form is always-ask. Operator ruling (2026-10-26): a coordinator may
force-restart a hung child, bounded.

- `session-health` (read-only, serve-gated) reports, for each session the caller
  holds, ok / busy-too-long / frontend-dead / unknown from clown's status file
  (`$XDG_STATE_HOME/clown/status/<sha256(principal)>.json`, clown doc 0023).
  Busy-too-long: a turn running past `[health] busy-bound` (default 60m, trusted
  layers only) with no hook and no transcript mtime within the same bound, and no
  spinclass merge/check job running. A missing file is `unknown`, never healthy.
  Package `internal/0/hungstatus`.
- `restart-hung-child` force-restarts a held child: it needs the `restart` right,
  re-checks the verdict itself (not hung, or excused: refused), works at most once
  per child per 24h (claimed under the session lock in `forced_restart_at`, rolled
  back if the hand-off fails), is logged, and wakes the holders. Confined callers
  and a session naming itself are refused. The hook allows it; the unforced and
  `force` forms are unchanged.

Caveats, stated plainly:

- **The verdict is soft against an unconfined holder.** The status file's
  `turn_running`, `last_hook_at` and `transcript_path`, the job record and the
  session record are all writable by code running as the same user. A malicious
  unconfined holder that already has the `restart` right could forge a hung
  verdict and restart a sibling once a day. The real bounds are the `restart` right
  and the confined-caller refusal; the verdict and the 24h cap protect against
  honest mistakes.
- **No push detector.** Nothing watches periodically: the coordinator pulls
  `session-health` and decides. There is no timer that wakes holders on a hung
  verdict yet.
- **Synchronous gates write no job record.** Only async merge/check jobs are
  visible to the verdict, so only the 60m default keeps a long synchronous gate
  from looking hung. Raise `busy-bound` if gates can run longer.
- Clown cannot see a tool hang, a dialog or a model stall apart; an interrupted
  turn can leave the busy mark set, so expect a false positive after an Escape
  until the next Stop.
- Not yet proven live.

## More Information

FDR 0032 (principals, handles, D13 rights), FDR 0035 (the confinement and
frontend-record model), `close-this-session` (#389), spinclass-sweatfile(5).
