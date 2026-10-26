# spinclass pickup note, 2026-10-26 (for the replacement session)

Written by spinclass/firm-mulberry (the session being replaced). Labels: V
verified on this host, R read in code or docs, I inferred. The brief is the
operator's unattended-trip goal: restarts, confinement and permissions.

## Standing rules

- Report each landed SHA to the coordinator (circus/eager-holly) by chat. One gate
  at a time, one build at a time. Check `env_meminfo` (more than 4 GB) first. Obey
  the coordinator's hold, "memory low" and "deploy done" broadcasts.
- Tests first, with red observed before green. Merge with targets=[] after a
  `nothing-but-the-truth` attestation with honest per-skill reasoning.
- Do not spawn sessions (the ssh-agent is flaky). In-session subagents are fine.
- Never read other sessions' `.spinclass/` directories. Do not change sweatfile
  gate or confinement settings. Do not touch #346, #78, #236.
- Sign off as Clown with the commit link from your system prompt.

## What landed today (origin/master)

bdb34e6 (#398 terminal query), 9760f7b (durable attestation store, `sc attestation`),
3adf040 (restart --peek and rights-table tests), 76b0a0a / 5c8882f / 913484b (the
three unattended status pages), 3abe698 (confined merge, delegation, token-lifetime
page), bfe4525 (restart start/back/never-returned wakes plus the relaunch
watchdog), e2c8777 (#369 permission counters), a85c2f1 and c6d9992 and d025aa0
(lint fixes for the nikulin cascade, including `lint-invisible-chars`), 6ae9124
(#372), 05ce039 (`[session-entry] unattended-permission-mode`), 41d6fa5 (sticky-
confined resume and exec), 8994fb8 (#431, F16), b70c3ab (live-restart recipes and
runbook), dc78e34 / 102d235 (hung-session note), c3ddbbf (coordinator page).

## In flight at the time of writing

- **Pool intake**, commit 3135b90 on `firm-mulberry`, with the two small commits
  9625e46 and 8b328f9 below it. It was tested red then green in
  `internal/charlie/auth` and `internal/bravo/confinement` (V). Its gate is started
  right after this note lands. If `git log origin/master` does not show 3135b90,
  the gate failed or is running: read it with `job_status`, retry once, and
  otherwise fix from the WIP branch below.
- **WIP branch on origin: `wip/spinclass-firm-mulberry-2026-10-26`.** It holds
  everything above plus ec20a5e, an UNRUN recipe `debug-profile-pin-spawn` (#407
  end to end; it spawns a real worker, so run it only with the coordinator's go).
  To resume: `git fetch origin wip/spinclass-firm-mulberry-2026-10-26` and
  `git cherry-pick ec20a5e`.

## Ordered queue

1. Finish the pool gate (above). Then ask papi/crisp-juniper whether the pool's
   revoke command form (`papi forge token revoke --host H --user U --card-login
   --session '<slot>'`) is right: I added `--host/--user/--card-login` to match the
   FDR 0028 form; papi's note gave only `--session` (I).
2. Pool follow-ups: the operator runs the papi batch mint into
   `$XDG_STATE_HOME/spinclass/token-pool` and sets `allow-stale-base` for the trip
   repos (the base-branch fetch still needs ssh). Then a sticky unit-spec socket
   list, so a respawn without SSH_AUTH_SOCK is not a changed boundary on clown's
   side (`docs/plans/2026-10-26-unattended-spawn-without-agent.md`, about 1 batch).
3. Require the `frontend-deadman` capability (clown#319, clown master c4efcf09c641)
   in `confinement.RequiredCapabilities` before confined spawns. Do it only after
   the deployed clown reports it, or every confined spawn is refused.
4. Update `docs/plans/2026-10-26-unattended-confinement.md` for clown's unit
   resource limits (#320: MemoryMax and so on, partly closing #387) and the
   dead-man socket (#319).
5. Hung-session detection (`docs/plans/2026-10-26-hung-session-detection.md`).
   pure-teak will write `$XDG_STATE_HOME/clown/status/<sha256(principal)>.json`
   (fields in the note) and ping the SHA. Then: a reader, a classifier with a
   trusted `[health]` table (operator ruled: busy bound 60m), a read-only
   `session-health` tool for held children, and a bounded coordinator forced
   restart (operator ruled yes: at most once per child per day, only with the
   `restart` right, logged and woken to holders). About 3 batches (I).
6. The restart-verdict gap. Answered by pure-teak (2026-10-26 15:58, R from clown's
   code plus its log): `clown restart --when idle` only queues in memory, and the
   preflight runs only when the frontend receives a Stop event. An already-idle
   session gets no Stop, so `restart-child-session` on an idle child never
   reaches `restart-preflight`: circus/solid-elm logged "exiting with a restart
   queued and never asked about", `restarting_at` stayed null, and no wake or
   watchdog fired (V). The relaunch was the fleet restart through the resume
   entry, not this request (I). The fix is clown's (queue on an idle frontend runs
   the idle path at once, about 2-3 h, not built). Spinclass side, still open:
   reword the queue message, which says "once its current turn ends", for an idle
   child; and when testing live, make the child run a turn first.
   **Silent wakes (fixed in the commit after 15362f3).** The start/back wakes of
   bfe4525 never arrived: `clown.NotifyPrincipal` is a silent no-op unless
   `CLOWN_BIN` is set, and neither the clown frontend (pid 3664821) nor the
   relaunched agent (pid 3939957) had it (V, from /proc environ). My unit tests
   stubbed the sender, so they could not see it. The fix is
   `NotifyPrincipalUnderClown` (gate: `CLOWN_BIN` or `CLOWN_SESSION_ID`), used by
   the restart preflight and the hooks seam, plus `CLOWN_SESSION_ID` in the
   watchdog env; `Enabled()` is unchanged. Tests run a stub ringmaster with
   `CLOWN_BIN` unset. STILL OWED: the live proof on a restarted child (the
   requester must receive the start and back wakes; needs a deploy or a manual
   build of the installed spinclass) and an e2e check in the live-restart recipes
   ("the requester received the start and back wakes"). Idle children need a
   nudge message after `restart-child-session` until clown ships its idle fix.
   Reword the queue message ("once its current turn ends" is wrong for idle).
   Also still open from the coordinator: which clear-site (hand-off timeout or
   cancel) cleared solid-elm's RestartRequest (read the servelog for that key).
   **Close after restart (reported by the coordinator, 16:13).** `close-child-session`
   warned that the recorded pid 3657102 (the `sc` launcher) is not provably the
   session's process, so the relaunched agent, clown and the MCP children kept
   running after the worktree was deleted. Needed: re-record pid/session key on
   relaunch (SessionStart hook), or close finds the process group via the session
   key in the environment; a restart-then-close test and a verify-recipe check that
   no process with the session key in its environment remains.
7. Follow-ups filed: #427 (store retention), #428 (`sc run` attestation env, handler
   test), #429 (five unenforced rights), #432 (confined landing, after the trip),
   #435 (who granted a handle and when), #157 (go-mcp/huh bridge: igloo e178fea is
   on master, resume steps are in the issue), conformist#149 (eng template
   `lint-invisible-chars`), papi#91 (does the TTL sweep revoke live tokens).

## Recipes and commands

- `explore-live-restart-prereqs`, `explore-live-restart-baseline <key>`,
  `explore-live-restart-verify <key>` (read-only; the runbook is
  `docs/plans/2026-10-26-first-live-restart-unattended-runbook.md`).
- `debug-lint-all-findings`: golangci with --keep-going, to reproduce the cascade
  (after a plain `nix flake update`; revert the lock and the regenerated tommy
  codec before committing). `lint-invisible-chars` is in `lint`.
- `debug-restart-watch-timer`, `debug-madder-user-store`, `debug-go-test <dir>`.
- Gate: `nothing-but-the-truth` (all five skills, honest reasoning), then
  `merge-this-session-async` with targets=[].

## Traps

- A restart cancels a running gate AND drops the buffered attestation. Re-attest.
- A direct `clown restart` skips the running-job check; only the restart tools go
  through the preflight and send the wakes.
- A dirty tracked tree is refused at the merge's repair step (#345): commit or
  stash first. Stashes die with the worktree.
- `just build-tommy-codegen` rewrites go.nix (#410): restore it afterwards.
- The Edit tool decodes backslash-u escapes in what you type: to write a
  six-character Unicode escape into a file, type backslash, `u005c`, then the
  digits, and check with rg. Never leave a literal Cf/Cc character in a tracked
  file (`lint-invisible-chars` will fail the gate).
- The nikulin cascade runs a plain `nix flake update` before `just`, so its
  linters are newer than the lock: findings can appear one at a time.
- `whoami` lists circus/light-elm and circus/ready-acacia as held. They are
  not yours: do not close or message them.

## Waiting on

pure-teak (status file SHA, the preflight question, the deployed `frontend-deadman`),
papi/crisp-juniper (the pool format is in; #88 and #91 are theirs), the operator
(first restarts, prerequisites on flic, the batch mint), the coordinator (gates and
holds).

## Owed to the operator

The prerequisites on flic: clown at least 1f5e57c5 (c4efcf09c641 for the dead-man),
spinclass bumped past 05ce039, and `[session-entry] restart` plus
`unattended-permission-mode = "dontAsk"` in the global sweatfile. The batch mint
and its TTL. `allow-stale-base` for the trip repos. The 168h profile-grant cap
lapses mid-trip. The #432 ruling that confined landing waits until after the trip.
