# Live proofs after the next deploy: plan (spinclass/light-cedar)

Date: 2026-10-26. Status: plan only; nothing here has been run. Run only after
the coordinator and operator announce the deploy, and never restart a session you
do not own. Labels: R read, I inferred, V verified (none yet).

Prerequisite for all: the deployed `spinclass` and `clown` include 9b7dab5
(re-record pid), 6d0574c/f88433f (session-health, restart-hung-child),
fix 15362f3+8d9ae0c (wakes without `CLOWN_BIN`), and clown acc1a8301a93 (status
file) / abe4b084be10 (idle-request fix, clown#327). Check with
`spinclass version` and `just explore-live-restart-prereqs`.

Coupling to check first for any CONFINED proof: spinclass's linked clown library
and the installed clown must agree on the profile digest, or the spawn is refused at
clown's digest check (`2026-10-27-profile-digest-coupling.md`).

## 1. Restart wakes arrive in the requester's inbox

Setup: a throwaway worker W held by requester R (the coordinator or a test
session), with the `restart` right.
1. `just explore-live-restart-baseline W`.
2. R calls `restart-child-session` on W (unforced). If W is idle and clown#327 is
   not deployed, send W one short message (the nudge) so a turn ends.
3. Pass criteria: R's inbox (`chat_read`) holds "W is restarting now (...)" and
   later "W is back after its restart"; `just explore-live-restart-verify W`
   passes, including "recorded pid is one of the session's live processes".
Fail signals: no start wake (check `CLOWN_SESSION_ID` in the serve env: the fix
is `NotifyPrincipalUnderClown`), no back wake (SessionStart did not mark
`RestartedAt`), or the watchdog wake "has not come back".

## 2. Idle restart without a nudge (needs clown#327 deployed)

Same as 1 but W is idle for several minutes before R requests, and **no message
is sent to W**. Pass: W restarts within the 30 s countdown plus relaunch (verify
recipe passes) with `restart_request_queued` false afterwards. Fail: the request
stays queued ("exiting with a restart queued and never asked about", clown log):
then clown#327 is not in the deployed clown; keep the nudge wording.

## 3. Restart then close leaves no process (the 9b7dab5 proof)

After 1 or 2, R calls `close-child-session` on W (it is refused for up to 2
minutes after the restart verdict unless forced; wait). Then
`just explore-live-close-leftovers <W key>` must PASS: no process carries
`SPINCLASS_SESSION_ID=<W key>`. Unverified (I): that SIGHUP to the agent also
ends clown, ringmaster and the MCP children; if leftovers remain, the recipe lists
them and the fix is to signal the process group or the clown frontend.

## 4. Pool spawn without the agent

Prereqs (operator): batch mint into `$XDG_STATE_HOME/spinclass/token-pool` (papi's
runbook placeholder path differs; see the coordinator message), `[auth].forge-hosts`
set, `allow-stale-base` for the repo. `just explore-pool-check` passes first.
Run (needs the operator's go because it spawns): the spawn from a shell with
`SSH_AUTH_SOCK` unset (`just debug-sc`'s `env -u SSH_AUTH_SOCK` form, or the WIP
recipe `debug-profile-pin-spawn` at ec20a5e adapted to the pool). Pass:
`explore-pool-check` shows one more slot `claimed`; the worker's remote is the
token-rewritten https; `just debug-agentless-push <worktree>` succeeds; closing it
runs the pinned revoke (fails until the card is back, then retried: expected).

## 5. session-health and restart-hung-child (needs clown acc1a8301a93)

**A long MCP tool call is NOT a hang drill (V, drill 3 on spinclass/stark-cherry,
2026-10-27).** The throwaway called `run_recipe debug-sleep 900`; after 120 s the
harness returned "moved to the background" and the model's turn ended, so the session
was idle with a background task: `session-health` read `ok ... idle` and
`restart-hung-child` refused it ("not hung (ok: idle); a forced restart needs a
busy-too-long verdict"). That is the not-hung refusal branch, verified live; the
classifier behaved correctly.

What a real stall needs: `turn_running` true (a UserPromptSubmit with no Stop yet) AND no
transcript or hook movement for the bound. Candidate drills, none run yet:
1. **SIGSTOP of the claude process mid-turn** (best): a throwaway doing a long loop of
   read-only calls (the drill-1 brief, N=150); an operator at a terminal runs
   `kill -STOP <claude pid>` (pid from `explore-live-restart-baseline`); the transcript
   freezes, no Stop hook comes, so with `[health] busy-bound = "2m"` the verdict reads
   busy-too-long after about 2 minutes; the holder calls `restart-hung-child` (clown's
   forced restart kills a stopped process with SIGKILL) and a second call is refused.
   Needs a human or a paved recipe to send the signal; an agent's Bash is denied.
2. An unanswered permission prompt in a non-dontAsk throwaway (the real smart-hemlock
   hang): needs a worker launched without the unattended mode.
3. A model-API stall or a wedged hook cannot be produced on demand.
Set `busy-bound` back afterwards (it changes `session-health` for every held session).
