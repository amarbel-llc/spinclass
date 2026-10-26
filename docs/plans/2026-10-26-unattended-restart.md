# Unattended operation: restart

Date: 2026-10-26. Status: **the spinclass half is built and tested against
fakes; no end-to-end restart has ever run live.** Design: FDR 0037 (#419).
Siblings: `2026-10-26-unattended-confinement.md`,
`2026-10-26-unattended-permissions.md` (cross-referenced below, not repeated).

Labels: **R** read in code, docs or an issue. **V per <ref>** a recorded live
run exists (there is none for restart; see below). **I** inferred. Clown, posh
and troupe are other lanes: their behaviour appears only as the contract
spinclass's docs and code state, marked **(dependency)**.

## 1. Proven live / tested against fakes / unverified

Live evidence search: FDR 0037 (promotion criteria still unmet, R), the
runbook `2026-10-25-first-live-restart-runbook.md` (status "not run", R),
`2026-10-25-restart-arc-status-draft.md` ("no restart has run end to end", R)
and the loose-ends table row for d974419/ae042dd ("No live restart has run",
R). Issue #419 has 0 comments (R). `git log -300` has no commit recording a
live restart (R). The only live-evidence commits are for confinement (FDR 0035
round N), not restart. So **nothing below is proven live.**

Fakes in use: `cmd/spinclass/restart_contract_test.go` execs a shell fake of
clown's `restart` verb (R); `restart_cmd_test.go` and `restart_peek_test.go`
stub the hand-off (`restartHandoffFn`) and the wake (`notifyPrincipalFn`) (R);
`zz-tests_bats/restart.bats` puts a fake `clown` on PATH (R);
`internal/delta/close/closing_mark_test.go` drives close against a state
record, with no real agent process (R).

| Capability | Status | Evidence |
|---|---|---|
| self-restart (`restart-this-session`) | Fakes only. Records `RestartRequest`, runs hand-off, clears it on hand-off failure. | R: restart_cmd.go `queueRestart`; `TestRestartThisSessionQueuesAndRunsTheHandoff`, `...HandoffFailureClearsTheRequest`. Fake-clown argv contract: `TestRestartHandoffMeetsClownContractForSelf`. |
| child restart with the `restart` right | Fakes only. | R: `TestRestartChildSessionAllowedWithTheRestartRight`, `...RefusedWithoutTheRestartRight`, `...AcceptsAPendingHandleOnFirstUse`, `...RefusesAConfinedCaller`. |
| `cancel-restart` | Fakes only. Clears the record; does not call clown. | R: `TestCancelRestart*` (7 tests). Whether clown actually drops its pending restart on the next `none` is **(dependency)**, unverified (I). |
| preflight verdicts (`restart`/`none`/`refused`) | Fakes only. Re-checks, consumes, notifies holders on refusal. | R: `TestRestartPreflight*`. Called by clown at idle **(dependency)**: clown's call shape is as FDR 0037 states it; never observed (I). |
| `--peek` liveness | Fakes only. Read-only; `closed` for not-found, tombstoned, abandoned, closing. | R: `restart_peek_test.go` (3 tests). Clown wiring at clown 613bf9a is a peer report in the runbook (R, secondhand). |
| close/restart ordering (`RestartInFlightWindow`) | Fakes only. Locked `session.Update`; close refused for 2 min after a `restart` verdict unless `--force`. | R: `closing_mark_test.go` (`...RefusesWhileARestartIsInFlight`, `...ProceedsOnceTheRestartWindowHasPassed`, `...ForcedCloseIgnoresARestartInFlight`, `...ClearsTheClosingMark`). The 2-minute window is sized from clown's 30 s countdown plus stop and relaunch (R, FDR); real timing unmeasured (I). |
| hand-off contract with clown | Pinned on both sides by contract tests against a fake of the other: spinclass's `restart_contract_test.go` (R) and clown's `TestRestartVerbArgvContract` (peer report in the status draft, R secondhand). `--when now` accepted since clown 011db3c (R, FDR + fake). | Never run against the real clown binary (R, runbook: "Only the fakes have run it"). |
| same conversation + profile pin on relaunch | **Unverified.** Spinclass pins the profile digest and re-applies it on `sc resume` (ae9efb7, 1c9ad13, d845036 carries `ProfileNarrowing`) (R), but clown, not `sc resume`, relaunches a restarted session, so whether the pin is checked on that path is open (I, runbook "Not covered"). | None. |
| troupe credential not revoked on restart | **Unverified; clown/troupe requirement (dependency).** FDR 0037 states it; the runbook records a peer's report that clown does not revoke (R secondhand). Spinclass owns the other half: a late close revoking a principal the relaunch re-mints, which ordering prevents (R, tests above). | None live. |
| confined sessions restarting inside their unit | **Unverified.** Spinclass allows a confined session to restart itself and cancel its own request, and refuses it for `restart-child-session`/`cancel-restart <child>` (R). A confined session's preflight needs a frontend mapping or is refused (R, `TestRestartPreflightRefusesAConfinedSessionWithoutAMapping`). The relaunch inside the unit is clown's and untested (R, runbook). See the confinement sibling. | None. |
| STALE detection after a deploy | **Detection built, tested with synthetic inputs; not a restart trigger.** `version` (serve only) prints `serve binary: current / same build, different path / STALE / unknown` (R, commands_version.go; 781796d compares build identity); `sc list` has a SERVE column (3cd7175, R); a stale serve refuses a confined spawn (R, servefresh.go). #399 is closed (R). That a restart clears STALE (the #419 acceptance) is unrun (R, runbook step 4). | `commands_version_test.go` (R). |
| operator gating of `--force` | Fakes/unit. `force=true` is always-ask (`perms.AlwaysAsk`); the agent's Bash use of `sc` session verbs is denied (c743a1d, #422 operator ruling: the CLI is operator-only) (R). | Hook behaviour live-observed in confinement round N is not restart-specific; not claimed. |

## 2. What is missing for restarted sessions to keep working for days unattended

1. **Nothing triggers restarts after a deploy.** STALE is reported only when
   an agent calls `version` or someone reads `sc list`; no spinclass code
   enumerates stale sessions and restarts them (R: restart_cmd.go has no
   such path; grep of `internal/` shows restart referenced only in the files
   above). The #419 flow ("a coordinator can list its children, restart each")
   needs a coordinator agent to do it by hand (I).
2. **A coordinator that is an implicit (main-checkout) session cannot be
   restarted, and implicit sessions are refused by `restartPlan`** (R). If the
   fleet's coordinator is such a session (I, #296 describes the eng
   main-checkout coordinator), it is exactly the process that stays stale.
3. **Operator config not known to be set.** The hand-off
   `[session-entry].restart` must be in the global sweatfile or above; without
   it every restart is refused (R). The status draft says "not known to be set
   on flic" (R). The principal must be UUID-shaped (R).
4. **No retry on refusal.** A running merge/check job refuses the restart at
   request time and again at preflight; the preflight clears the request and
   wakes the holders (R). There is no "hold until the job ends" (the #419 text
   offered it; FDR chose refusal) (R). A deploy wave that lands while merges
   run will lose those requests; someone must re-issue them (I).
5. **A restart that never comes back is invisible.** Spinclass sees only the
   hand-off exit status; the later outcome is "reported through the preflight
   verdict, not back to the caller" (R, FDR Limitations). After a `restart`
   verdict there is no success wake, no failed-relaunch wake and no watchdog;
   `RestartingAt` merely ages out after 2 minutes (R). A session that clown
   stopped but failed to relaunch is a dormant session; dormant sessions are
   invisible to presence (#296, open, R) and a parent is not told of external
   reaps (#331, open, R).
6. **Wedged sessions.** `when: idle` never fires for a session that never goes
   idle (I). The remedy, `force` (`when: now`), is always-ask by operator
   ruling and unavailable to an agent through Bash (R), so with nobody at the
   keyboard a wedged session cannot be restarted by anything but the operator
   (I). A forced restart also kills a running merge job (R).
7. **No bound on a queued request.** A queued `RestartRequest` has no expiry;
   only `cancel-restart` or a preflight consumes it (R). A request queued
   against a session whose frontend never runs the preflight stays queued
   (I).
8. **Holders are told only of refusals and cancels** (R, `notifyRestartRefused`,
   `notifyRestartCancelled`), not of a completed restart. The restarted agent
   is not told by spinclass why it restarted; #419 proposed a wake, FDR 0037
   defines none (R).
9. **Coordinator restarting its own children needs the `restart` right.** The
   spawner holds it; the default grant (`observe,close`) does not carry it, and
   a grandchild reached through a granted handle lacks it unless granted (R).
   Confined callers cannot run `restart-child-session` at all (R); see the
   permissions sibling.
10. **Cleanup interplay.** A close right after a restart is refused for up to
    2 minutes (R), which can fail an automated close/clean pass (I). #270
    (reaping leaves a bodyless clown process) is mitigated by a warning only
    (4387264), not by killing the process (R), so a stale process can survive
    a "restart plus close" sequence.
11. **Hand-off bound.** The hand-off has a 15 s timeout; exceeding it clears
    the request and reports failure (R). Behaviour of clown's `restart` verb
    on a busy host is unmeasured (I).

## 3. Smallest ordered work list

1. Operator sets `[session-entry].restart` in the global sweatfile; no issue
   (config), owner: operator. Prerequisite for all below.
2. Deploy clown >= 613bf9a and spinclass >= 2a79497 on the fleet host; no issue,
   owner: operator (runbook prerequisites 1-3).
3. Run the first live restart of a detached throwaway worker per the runbook,
   incl. same-principal, conversation resume, no STALE afterwards; #419
   (acceptance), owner: operator + spinclass; clown for any defects found.
4. Verify the relaunch keeps the profile pin and the troupe credential
   (no `mint-revoke` on restart) during that run; none yet (add runbook
   steps), owner: clown/troupe (dependency), spinclass records the result.
5. Live-test a confined worker restarting in its unit and a confined session's
   preflight mapping; none yet, owner: clown (relaunch) + spinclass
   (preflight).
6. A "restart all stale children" path: enumerate stale sessions (the SERVE
   column data) and restart those whose holder has the right; none yet,
   owner: spinclass.
7. Outcome reporting: wake the holders/requester on `restart` verdict and on a
   relaunch that does not come back (watchdog on `RestartingAt`); none yet,
   owner: spinclass + clown (dependency: relaunch result).
8. Retry or hold for a job-blocked restart (keep the request until the job
   ends instead of clearing it), if the operator wants it (decision A below);
   none yet, owner: spinclass.
9. Decide how a wedged session is restarted unattended (decision B); none yet.
10. Dormant-session visibility to presence (#296, open) and parent wake on
    external reap (#331, open); owner: spinclass + clown.
11. Make `close` of a recently restarted session wait instead of fail, or
    document the 2-minute window to automation; none yet, owner: spinclass.
12. Implicit-session coordinator restart story; none yet, owner: spinclass.
13. #429 (other D13 rights recorded but unenforced) is not on the critical path
    for restart (`restart` is enforced, R); keep open.

## 4. Decisions only the operator can make

- **A.** On a restart blocked by a running merge/check job: keep refusing and
  clearing (current), hold until the job ends, or auto-retry? (Affects item 8
  and whether a deploy wave is safe during merges.)
- **B.** Wedged sessions: may any unattended actor ever force a restart, or is
  `--force` operator-only for the whole two weeks (current ruling, #422)? If
  a bounded unattended force is allowed, who holds it and over which sessions?
- **C.** Whether the restart hand-off is configured globally on the fleet host
  (it lets every session's restart tools exec clown, runbook item 4).
- **D.** Who is the restart authority after a deploy: one coordinator holding
  the `restart` right on all children, a scheduled operator job, or manual?
  And may the coordinator be an implicit session (item 2)?
- **E.** Whether restart acceptance (FDR 0037 criteria) requires the forced
  path to run live once, or the forced path is recorded as out of scope
  (proposed in the status draft).
- **F.** Whether the 30 s countdown (an operator ruling owned by clown/posh)
  stays on for detached, unattended sessions, where nobody can cancel it (I).
