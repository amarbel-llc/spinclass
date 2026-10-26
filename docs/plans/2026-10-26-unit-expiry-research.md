# What happens when an unattended confined unit hits RuntimeMaxSec

Date: 2026-10-26. Status: read-only research for the 14-day trip (coordinator
request). Nothing was run, restarted or changed; no live unit was examined, so
every "V" below is absent. Labels: R read in code or docs, V observed live, I
inferred. Owners: clown is `clown/rare-walnut`'s lane; the policy is the
operator's.

## Scope

Only **confined, unattended** units carry the cap: clown's `unattendedRuntimeMax
= "RuntimeMaxSec=604800"` (7 days) is appended to the unit properties when the
launching frontend has `CLOWN_UNATTENDED=1` (clown `internal/agentscope/unit.go`
:117, :262; FDR 0020 table; R). Interactive units have none, and an unconfined
worker (flic's default) has no unit at all, so nothing expires it (coordinator
confirmed the first part against unit.go:117). It is a `const`: no sweatfile key,
env var or spec field changes it today (R: grep of `internal/agentscope`; the
unit spec has no such field).

## 1. What happens at 7 days

- systemd stops the unit (SIGTERM to the unit, SIGKILL after the stop timeout).
  The dead-man shim and the provider go with it (R, FDR 0020 text).
- The frontend clown is waiting on `systemd-run --wait`; when the unit ends,
  `Run` records the result file (`exit_code` from systemd-run, `Error` unset on a
  plain non-zero exit) and returns the code (R, `run.go` ~:320-363). Whether the
  frontend process then exits rather than relaunching is not stated anywhere I
  read; a relaunch only happens on a restart verdict (I: the frontend exits, as
  after any provider exit).
- The session's records are untouched: the frontend record stays `active`, the
  unit record and spec files stay (they are removed only by `Stop`/close,
  `confinement/stop.go`), the principal mapping, handles, holders and the
  credential file (the claimed pool slot is renamed into frontend state beside
  it) all stay (R).

## 2. Spinclass state and wakes

1. **State recorded:** none new. Nothing writes "expired" (R: no code path reads
   the unit result or `exit_code`). The state stays `active`.
2. **Wakes:** only the SessionEnd hook emits the "normal exit" wake
   (`hooks.go` `emitWorktreeExitWake`, R). A hook inside the dying unit may or
   may not get to run before SIGKILL (I), and for a confined session its state
   reads are only the display copy. FDR 0032 D6 documents that a worktree session
   that dies silently gets **no crash wake** (R, comment in `runSessionEnd`). So
   assume **nobody is told** (R/I). The restart watchdog (`restart-watch`) only
   covers a restart in flight, not an expiry.
3. **`sc list`:** a confined session records no pid and is kept `active` only
   while `confinement.UnitRunningForWorktree` is true (`session.ResolveState`,
   R). After the unit is gone that is false, so it reads **inactive**; the clown
   presence record goes stale, so it is not rescued to running-detached after
   its freshness window (R/I). It is not shown as dead or expired.
4. **`restart-child-session` on an expired unit:** the spinclass side passes
   (hand-off configured, UUID principal, no running job: `restartPlan`, R), then
   runs the hand-off `clown restart --session <principal>`; clown answers
   `errNoFrontend` when the control socket is absent (`cmd/clown/restart.go`
   ~:425, R) so the hand-off fails, spinclass clears the request and returns "the
   restart hand-off failed ... nothing was queued" (R, `queueRestart`). So it
   **refuses**, with that error. Recovery is the operator's `sc resume <target>`
   (a new launch, sticky-confined resume, `resume_confined.go`, R), which needs a
   terminal and probably the card; nothing unattended can relaunch it.
   `restart-preflight --peek` would still say `live` for the stale record (I).
5. **Restart before expiry:** the relaunch is a new `systemd-run` launch whose
   unit gets `RuntimeMaxSec` set afresh at creation, and the old unit is stopped
   (`stopIfActive`), so the clock starts again at 7 days (R for the property being
   set per launch and the old unit being stopped; **I** that no systemd state
   carries over, never observed).

## 3. Smallest options for a 14-day trip

(a) **Longer `RuntimeMaxSec`.** Owner: clown (a code change in
    `internal/agentscope/unit.go`: today a constant; it would need an env or
    spec field), then the operator's ruling on the number and on where it is set
    (the spinclass `[session-entry]`/launch env is the natural carrier, in a
    trusted sweatfile layer). Cleanest, removes the cliff, costs a clown release
    and loses the 7-day backstop behind the dead-man for a leaked unit (the
    dead-man covers frontend death; this covers a hung frontend that stays alive).
    Not available by configuration today.

(b) **Weekly bounded restart by a holder.** Works today with what is merged: a
    holder with the `restart` right calls `restart-child-session` (unforced) on
    each unattended confined child at least every ~5 days. A busy child restarts
    at the end of its turn; an idle child needs a message first until clown#327 is
    deployed (restart wording). Needs the thing that does it: the coordinator
    agent on a schedule (it cannot use `sc` in Bash; the MCP tool is the surface).
    Risk: the restart fails if the hand-off is not configured, or a merge/check
    job runs (refused, then retry), and a restart that never returns is caught
    by the existing watchdog wake. Cost: no code.

(c) **Spinclass-side expiry wake or display.** Smallest honest form is a pull:
    show the unit's age and time left in `session-health` (read the unit's
    `ActiveEnterTimestamp` from `systemctl --user show`, add 7 days when the
    session is unattended) so the coordinator's health loop knows when to do (b);
    ~1 batch, uses the verdict plumbing already merged. A push (a timer that
    wakes holders at day 6) would reuse the restart-watch `systemd-run --user
    --on-active` pattern, ~1 more batch, and is only unit-tested against argv
    like the watchdog. Neither extends the unit; both need (b) or the operator
    to act on the wake.

Recommendation (labelled opinion, operator rules): do (b) now as the standing
weekly step (no code), add the (c) pull when time allows so the step is data
driven, and ask clown for (a) as the real fix. If nobody does any of these the
unattended confined fleet silently stops at day 7 and shows as `inactive`.
