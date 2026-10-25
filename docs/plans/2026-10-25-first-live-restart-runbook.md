# First live restart of a throwaway worker (#419): runbook

Date: 2026-10-25. Author: spinclass/firm-mulberry/bozo. Status: **not run.**
It waits for the prerequisites below and for the coordinator's go.

Claims are labelled V (observed), R (read in code, a doc or a peer's report),
or I (inferred).

## What exists

- spinclass side, on origin/master:
  - restart-this-session, restart-child-session and cancel-restart (c942d87,
    38f08f0). Force is always-ask.
  - restart-preflight, plus a read-only `--peek` that reports
    `state: closed|live` (249eb17).
  - The closing mark: close sets ClosingAt before it signals the agent
    (249eb17).
  - Design: FDR 0037. (R)
- clown side: `clown restart --session <principal> [--when idle] [--reason
  <text>]`. `--when now` and unknown flags exit 2, so spinclass refuses a
  forced restart up front until clown ships `now` (agreed with
  clown/pure-teak/pennywise, 2026-10-25). It calls `spinclass restart-preflight` at idle and does not revoke
  the troupe account on a restart exit. Merged at clown 720ffd7, tested only
  against fakes. (R, clown/pure-teak/pennywise)
- Deployed on flic: spinclass 22e43a0 and clown ca610b0, neither of which has
  `clown restart` or `--peek`. (R, coordinator, 2026-10-25)

## Prerequisites (all must hold before the run)

1. **Installed clown with 720ffd7 or later.** Check `clown --version` on flic.
   `clown restart --help` must exist. (I: those are the observable signs.)
2. **Installed spinclass with --peek** (2a79497 or later), so clown's pre-relaunch
   `--peek` call resolves. Check `spinclass version`, then run
   `spinclass restart-preflight --peek` from any directory. It should print one
   JSON line with `"state":"closed"`.
3. **pennywise's `--peek` wiring in clown**, merged at clown 613bf9a. That
   makes the clown prerequisite 613bf9a or later, which includes 720ffd7.
   Clown runs `--peek` just before the relaunch exec and skips the exec on
   `state: closed`. Anything else (live, an error, a timeout, unparsable
   output, or no spinclass on PATH) is logged as unknown and it relaunches.
   (R, pennywise.) Against a spinclass without `--peek` it therefore takes the
   unknown path, so prerequisite 2 matters for the race check.
4. **The global `[session-entry].restart` hand-off.** It goes in a sweatfile
   layer above the repo; the global `~/.config/spinclass/sweatfile` is the
   natural one. A repo or worktree value is ignored, and `sc validate` warns
   about it.

   ```toml
   [session-entry]
   restart = ["clown", "restart", "--session", "{principal}", "--when", "{when}", "--reason", "{reason}"]
   ```

   Setting this is an operator config change (I: the operator's call, because
   it lets every session's restart tools exec clown). Without it every restart
   is refused ("no restart hand-off is configured").
5. **Posh opt-in (attached sessions only).** The posh countdown is an operator
   ruling: 30 s with cancel, restart on expiry. Its design note is with
   posh/sleek-banyan, and its release is held. (R) A **detached** throwaway
   worker needs no posh, and clown prints the notice inline or not at all.
   (R, pennywise) So the first run should use a detached worker, and posh can
   stay off.

## The run (throwaway detached worker only)

1. Spawn a throwaway worker in a scratch repo (`spawn-session`, unconfined,
   brief: "idle; reply when asked"). Record its session key, principal (from
   `sc whoami` in it, or its spawn record) and jid (from `chat_list`).
2. Capture the baseline:
   - `spinclass restart-preflight --peek --target <key>` should report
     `state: live`;
   - in the worker, `spinclass version` (serve binary line);
   - in the worker, `chat_read`'s cursor state, by sending it one DM before
     the restart.
3. From the driver: `restart-child-session <key> --reason "first live restart"`.
   It should succeed and run the hand-off.
4. Wait for the worker's next idle. Confirm all of:
   - `$XDG_STATE_HOME/clown/close-self-run.log` shows the preflight verdict
     `restart` and the relaunch;
   - the same key, principal and jid as before (the troupe identity survives:
     R, coco);
   - the conversation resumes, and the worker can recall the DM sent before
     the restart;
   - in the worker, `spinclass version` no longer reports STALE.
5. Race check, if pennywise's `--peek` wiring is installed:
   - queue another restart;
   - before the worker idles, `sc close` it from a terminal;
   - expect clown's log to show `--peek` reporting `closed` and no relaunch.
6. Tear down: `close-child-session <key>`. Check that its troupe account is
   revoked on this real close.

## Not covered here

- Confined workers: restart in the unit. clown owns this, untested. (I)
- Forced restart (`--force`, always-ask) over a running merge job.
- Attached sessions and the posh countdown.
- Pre-existing sessions without a `[session-entry].resume` carrying
  `{principal}`: clown, not spinclass, relaunches them, so that pin does not
  gate a restart. (I, unverified against clown 720ffd7.)
