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
   current turn ends. `now` is part of the vocabulary and the verdict, but no tool
   asks for it yet.
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
8. **Planned follow-ups, not built here:** a `force` parameter (an escalation, so
   always-ask, like `close-child-session`'s) and a cancel-restart verb. Both tools
   reject a `force` argument today.
9. **A 30 second countdown before the relaunch is an operator ruling owned by
   clown and posh.** Spinclass has no part in it and implements none.

## Interface

- `restart-this-session` (MCP tool, `sc restart-this-session`): optional
  `reason` (default "requested by the session itself"). The caller is resolved
  like `close-this-session` (principal, frontend mapping, session). Refused, with
  nothing recorded, when no trusted hand-off is configured, a job is running, the
  session is implicit, or its principal is not UUID-shaped (the same check
  `sc resume` applies to `{principal}`, `applyRecordedPrincipal`). Otherwise it
  records `RestartRequest{RequestedAt, Requester, Reason, When}` in session state,
  runs the hand-off, and tells the agent to end its turn. If the hand-off fails,
  the request is cleared and its output returned.
- `restart-child-session` (MCP tool, `sc restart-child-session`): `child` and
  optional `reason` (default "requested by a holder"). Refused for a confined
  serve caller. Authorized as `close-child-session` is (handle or spawner key,
  accept-on-first-use) plus the `restart` right. The obstacles apply to the
  child, and the hand-off is read from the child's repo hierarchy and run in the
  child's worktree. The child's principal comes from the frontend mapping, not
  from state the agent can write.
- `restart-preflight [--target T]` (hidden, CLI only; target defaults to the
  session of `CLOWN_SESSION_ID`). Prints exactly one JSON line and exits 0 (non-zero
  only on an internal error):

  ```
  {"verdict":"restart"|"none"|"refused","reason":"...","when":"idle|now"}
  ```

  `none`: no session, no request, an implicit or closed session. `refused`: the
  obstacles fail now; the request is cleared and the holders are told, best
  effort, through `clown.NotifyPrincipal`. `restart`: every check passes; the
  request is cleared (consumed). `when` is the requested timing, empty for `none`.
  The principal is resolved by `closeRunnerPrincipal`'s rules (the frontend mapping
  first, a confined session with no mapping refused), never from the request.

## Requirement on clown and troupe

A restart exit must not run `troupe mint-revoke`. A restart relaunches the same
principal, and revoking its credential on the way out would strip the
relaunched session of the identity the restart exists to keep. Only a real
close revokes.

## Limitations

- Nothing here performs the restart. Until clown implements the idle queue and
  the preflight call, a configured hand-off only has spinclass's half.
- The hand-off's exit status is all spinclass sees; whether the frontend later
  restarts is reported through the preflight verdict, not back to the caller.
- A refusal at preflight time reaches the holders as a wake; the requesting agent
  has already ended its turn.
- The request lives in session state, which an agent in an unconfined session can
  write. It carries no authority: the preflight re-checks everything and takes the
  principal from the frontend mapping. A forged request can at worst cause a
  restart the hand-off already allowed.

## More Information

FDR 0032 (principals, handles, D13 rights), FDR 0035 (the confinement and
frontend-record model), `close-this-session` (#389), spinclass-sweatfile(5).
