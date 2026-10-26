# Live proofs after the next deploy: plan (spinclass/light-cedar)

Date: 2026-10-26. Status: plan only; nothing here has been run. Run only after
the coordinator and operator announce the deploy, and never restart a session you
do not own. Labels: R read, I inferred, V verified (none yet).

Prerequisite for all: the deployed `spinclass` and `clown` include 9b7dab5
(re-record pid), 6d0574c/f88433f (session-health, restart-hung-child),
fix 15362f3+8d9ae0c (wakes without `CLOWN_BIN`), and clown acc1a8301a93 (status
file) / abe4b084be10 (idle-request fix, clown#327). Check with
`spinclass version` and `just explore-live-restart-prereqs`.

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

On a throwaway worker W that runs a long no-progress command: `session-health`
shows `ok`, then after the bound (set `[health] busy-bound = "2m"` in the global
sweatfile for the test) `busy-too-long`; `restart-hung-child` restarts it once and
a second call within 24h is refused. The first live use is the proof that clown's
status file fields (`turn_started_at`, `last_hook_at`) behave as documented
(R only so far).
