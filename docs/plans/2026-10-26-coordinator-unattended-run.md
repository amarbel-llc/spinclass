# Coordinator runbook: keeping a worker fleet healthy, operator absent

Date: 2026-10-26. Status: operating instructions, docs only. Nothing here has
run live as a whole; restart has never run end to end (restart status page).

Audience: you, the unconfined coordinator session that spawned workers and holds
handles on them. Cites: FDR = `docs/features/`, SW = `spinclass-sweatfile(5)`,
INT = `spinclass-internals(7)`, LDR = `spinclass-local-default-ref(7)`,
status pages = `docs/plans/2026-10-26-unattended-{confinement,restart,permissions,spawn-without-agent}.md`
and `...-confined-merge-and-delegation.md`. **(I)** = inferred, not read in code.

## 1. Standing rules: never do these, queue them for the operator

- Never `force` a close or restart. `force=true` is always-ask on both
  (`perms.AlwaysAsk`; FDR 0037 decision 8, FDR 0032). In a dontAsk session it is
  a hard deny. A forced restart also kills a running merge job.
- Never spawn `unconfined`, and never a non-confined spawn: always-ask until
  #384 (permissions page). Only confined spawn is auto-allowed (#382).
- Never grant, release or `accept`: every one is always-ask. Do not try to widen
  rights; default grant is `observe,close`, no `restart` (FDR 0037 decision 6).
- Never try to land a confined worker's branch. merge/check/attestation tools
  refuse a confined caller; `sc merge <target>` is the operator's path
  (confined-merge page s1).
- Never run non-read-only `sc` verbs through Bash: the hook denies them (#422,
  FDR 0037 Limitations). Use the MCP tools. Bash `sc` allowed: list, version,
  validate, whoami, list-handles (no accept), perms-list, host-load.
- Never accept a post-merge digest or change a `[[post-merge]]` section; needs
  an operator-typed digest (permissions page).
- Never do card acts (`profile-grant-bless`, `--repin-profile`): TTY, PIN, touch.
- Never delete state, worktrees or `.spinclass/`, never edit sweatfiles
  (sweatfile edits ask by default, SW), never write another session's state.
- Never retry a refused action in a loop. One retry, then report (section 3).
- Never `sc resume -y` an `active` session: the warning is not skippable (CLAUDE.md).

## 2. Health loop: every 30 minutes (hourly when quiet)

1. `chat_read` (troupe). Restart start/back/never-returned wakes and refusal
   wakes arrive here via `clown.NotifyPrincipal` (FDR 0037). Also watch for
   `[clown-job]` wakes from async merges (INT; ringmaster `job_status`).
2. `list` (MCP). Read each session's state (`active`/`inactive`/`abandoned`)
   The MCP `list` output observed on 2026-10-26 carries no SERVE column (V:
   tab-separated key, state, name, worktree, description, lineage). A worker's
   staleness is therefore not visible to you through MCP (I): after a deploy, ask
   each worker by chat to run its own `version` and report the serve line, or
   restart them all one at a time (section 4). The SERVE column is on the
   operator's `sc list` (restart page, 3cd7175).
3. `whoami`: confirm your own serve is not STALE. `version` prints
   `serve binary: current / same build, different path / STALE / unknown`
   (restart page, `commands_version.go`). Your own STALE needs the operator
   (you cannot restart yourself usefully; (I) your hand-off is the same path,
   but your context is the fleet's only memory).
4. `host-load` (read-only; load averages, over-threshold flag, busiest
   processes mapped to session by working directory, #388). Defaults are
   fine; pass `threshold` only to tighten.
5. Permission counters with stats-me: `stats-me_series` pattern
   `stats_counts\.spinclass\.permission\..*` then `stats-me_query`, e.g.
   `sum by (__name__)(increase({__name__=~"stats_counts\\.spinclass\\.permission\\..*\\.unattended_deny\\..*"}[1h]))`.
   Stored names (V: existing counters appear as `stats_counts.spinclass.merge.landed`,
   dots kept): `stats_counts.spinclass.permission.<repo>.{prompt,tier_allow,unattended_deny}.{main,subagent}.<tool>`. Under dontAsk no PermissionRequest fires, so `prompt`
   stays silent there; `unattended_deny` is the signal. `prompt` is nonzero only
   for sessions in auto mode. Counters exist only if statsd is configured (LDR);
   an empty series is not proof of health.
6. Per worker, `chat_list` shows live presence; a listed session with no wake or
   commit activity for hours is "silent" (section 3).
7. Read-only inspection of held children's worktrees is auto-allowed for
   `folio_read/ls/glob/tree/file-type`, `grit_branch` log/rev-parse/diff and
   `grit_stage` status/diff, only on accepted handles (#372, INT). Use those, not
   Bash. Anything else outside your own worktree prompts and, unattended, stalls.

## 3. Signals and responses

| Signal | Meaning | Do |
|---|---|---|
| Wake "X is restarting now (reason, by Y)" | preflight returned `restart`; clown is stopping X (FDR 0037) | Nothing. Do not close X for 2 min (`RestartInFlightWindow`; close is refused, FDR 0037) |
| Wake "X is back after its restart" | SessionStart stamped `RestartedAt` | Done. Optionally `list` to confirm `active` |
| Wake "X has not come back <D> after its restart began" | `restart-watch` fired at ~5 min; relaunch failed or is slow (FDR 0037) | `list`, `chat_list`; if X reappears, done. Else try `resume` once (no mint, works with ssh-agent down; spawn page s2). If still dead, report to operator. No force |
| Restart refused wake | job running, implicit session, hand-off missing, or no UUID principal (FDR 0037 interface); request is cleared | Wait for the job (ringmaster `job_status`), re-issue once. No hold/retry exists (restart page s2.4) |
| Tool refusal naming `[session-entry].restart` | operator never configured hand-off (FDR 0037 decision 3) | Stop restarting; report once |
| SERVE STALE on session X | X's serve predates a deploy; a stale serve even refuses confined spawns (#399) | Restart X when idle (section 4). Fleet-wide after a deploy: one at a time, wait for "back" before the next ((I) limits blast radius) |
| `unattended_deny` rising for tool T | a dontAsk session is being refused T; it will loop or give up (confinement page s2.10) | Find who (`main` vs `subagent`, repo); message that worker via `chat_send` to stop using T; if T is a legit need, report to operator. Do not edit tiers/sweatfiles |
| Worker silent for hours | no hung-detection exists (restart page s2.6) | In order: `list` (state), `chat_read`, read its worktree via `grit_stage` status/`grit_branch` log (#372), `chat_send` a one-line status ping, wait one loop. Still silent and `active`: request a normal restart (idle-queued; will not fire if wedged). Wedged beyond that needs the operator |
| Merge gate failure (worker's own `merge-this-session`) | pre-merge hook is the CI lane (CLAUDE.md) | Read the wake; `chat_send` the worker once to fix and retry. A second failure: report, stop |
| 401 / `Permission denied` on push or merge fetch | token revoked or ssh-agent flap (spawn page s2) | Retry once after a loop; report. Re-mint needs the card |
| `host-load` over threshold | pile-up; busiest processes name the sessions | Message the heaviest worker to pause; do not kill. Persistent: report. No unit limits exist (#387) |
| Low memory or disk | no spinclass tool for it | Use `env_meminfo`, `folio_df`; report to operator, delete nothing |
| Session `abandoned`/closed unexpectedly | external reap; parent is not told (#331) | Report; `resurrect` needs the ssh-agent (spawn page) so expect it to fail |

## 4. Restarting a worker (the exact calls)

Preconditions (all must hold, else refused with nothing recorded; FDR 0037):

- Operator set `[session-entry].restart` in the global sweatfile or above.
- You hold the `restart` right on the child. Spawner has all rights; a handle
  from `grant` does not carry it unless granted. `list-handles` (no accept)
  shows what you hold; a pending handle needs `accept`, which is always-ask,
  so (I) it stalls: only spawned children are reliably restartable.
- The child is not implicit, has a UUID-shaped principal, and runs no merge/check
  job (check `job_status`). The child is unconfined: `restart-child-session`
  and `cancel-restart <child>` refuse confined children (FDR 0037; restart
  page s1).

Steps:

1. `restart-child-session` with `child=<repo/branch or worktree name>` and a
   short `reason` (no `force`). Auto-allowed by the hook (FDR 0037).
2. Expect the tool to return success ("request recorded, hand-off ran") or the
   hand-off's failure output; on hand-off failure the request is cleared
   (15 s bound). Clown restarts at idle (`when: idle`); the 30 s countdown is
   clown's (decision 10).
3. Expect, in order, the start wake, then the back wake. A systemd one-shot
   watchdog (`restart-watch`) fires 5 min after the verdict if no back mark
   (FDR 0037). No watchdog on a host without `systemd-run --user` (Limitations)
   or for confined sessions.
4. Withdraw a request you no longer want with `cancel-restart child=<key>`
   (idempotent; clears the record only, clown drops it at the next `none`).
5. For your own session: `restart-this-session` ends your turn on success; the
   relaunched you has no memory of this page beyond files. Prefer not to.
6. 2-minute close window: after a `restart` verdict, `close-child-session` on
   that child is refused for `RestartInFlightWindow` (2 min). Do not close or
   `clean` the child in that window; do not use force to get past it.

All of this is fakes-only: no live restart has run (restart page s1). Treat the
first one as an experiment: restart the least valuable worker first.

## 5. Landing work

- Unconfined workers land their own work: `merge-this-session(-async)`, pre-merge
  hook = project CI, attestation via `nothing-but-the-truth` (self-attested,
  #301; CLAUDE.md, permissions page). Each uses the session's existing forge
  credential; no mint (confined-merge page s3). Do not re-run `just` first.
- Your checks: worker's wake says landed; `grit_branch` log on the child shows it;
  `list` shows the session. Counters `spinclass.merge.landed` and
  `...push_refused` (LDR) if statsd is on.
- Merge stacking: a second async merge enqueues (#265). Never cancel another
  session's job (`session-job-cancel` is own-session; (I)).
- A post-merge section change blocks agent merges outright (permissions page);
  tell the worker to split it out and queue it for the operator.
- Confined workers: do not merge. Ask the worker (`chat_send`) to push its
  branch with grit tools and finish its report; list it as "awaiting operator
  `sc merge`". Their work waits for the operator's return.

## 6. Not yet possible unattended

- Spawn or resurrect with the ssh-agent down: card needed at mint and at the
  base fetch (spawn page). Plan: spawn the whole fleet before the operator
  leaves; you only resume, restart and merge. Retry window is ~10 s (#411).
- Landing confined branches (confined-merge page s1; needs hook-in-unit work).
- Hung-session detection and forced restart of a wedged session (restart page
  s2.6; force is operator-only, #422).
- Restart of confined sessions in their unit, restart-all-stale automation,
  retry of a job-blocked restart (restart page s2-3).
- Delegated approval of worker prompts (confined-merge page s2): a dontAsk worker
  that hits a deny simply cannot do it.
- Handle grants and accepts (always-ask; #356 pending grants never deliver).
- Resource limits per session (#387), credential leak cleanup (#394).
- Card-signed acts, post-merge digests (permissions page).
- Report-worthy facts the loop cannot resolve: collect them in one chat or
  summary message to the operator; do not improvise around gates.
