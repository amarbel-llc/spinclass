# Draft status text for the restart arc (FDR 0037, FDR 0032)

2026-10-25, spinclass/firm-mulberry. A draft for the coordinator: no status
moves, and nothing is applied to the FDRs. Evidence labels: V verified (code
or test), R reported by another session, I inferred.

## FDR 0037, Session restart: stays `experimental`

Proposed status paragraph (to replace nothing; to add under the front matter
when the coordinator agrees):

> Status, 2026-10-25: experimental, both halves merged, neither deployed.
> spinclass: restart-this-session, restart-child-session and cancel-restart
> (c942d87, 38f08f0); restart-preflight and `--peek` (249eb17, 2a79497);
> the closing mark (249eb17); close and restart ordered under the session lock
> (ae042dd); `--force` refused until clown accepts `--when now` (d974419).
> clown: `clown restart` (720ffd7), the pre-relaunch peek (613bf9a), the
> pre-mint peek and the `--when now` design note (9be85c7) (R, clown/pure-teak).
> The hand-off is pinned on both sides by contract tests against a fake of the
> other: spinclass's restart_contract_test.go (V) and clown's
> TestRestartVerbArgvContract (R). flic runs spinclass 22e43a0 and an older
> clown (R), so no restart has run end to end.

Promotion criteria as written (experimental -> accepted), checked:

| Criterion | State |
|---|---|
| clown ships the idle queue that runs `sc restart-preflight` and relaunches on `restart` | Merged at clown 720ffd7 (R); not deployed |
| an operator's global sweatfile sets `[session-entry].restart` | Not known to be set on flic (I) |
| a session restarts itself end to end, keeping its principal, not restarting again | Not run |
| a holder restarts a child end to end | Not run; spawns deferred until the ssh-agent is stable (operator, 2026-10-25) |

Proposed added criterion: the forced path (`--force`, `when: now`) is either
built on both sides and run once live, or recorded as out of scope for
acceptance. Today it is refused by design (d974419).

## FDR 0032, Principals, handles and provenance: stays `proposed`

The restart arc touched only D13 (the `restart` object right, added
2026-10-24). Proposed one-line status note under D13:

> 2026-10-25: `restart` is enforced by restart-child-session and
> cancel-restart with a child (requireHandleRight), tested in
> cmd/spinclass/restart_cmd_test.go (V). Not exercised live.

Nothing in the restart arc bears on FDR 0032's own promotion criteria, so its
status is unchanged.
