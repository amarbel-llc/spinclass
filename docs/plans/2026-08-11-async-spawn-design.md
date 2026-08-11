# Async spawn-session: non-blocking spawn with hello delivered as a wake (#266)

Status: APPROVED 2026-08-11 (Sasha, interactive decision walkthrough)
Date: 2026-08-11
Depends on: #262 (spawn/fork unified; fork-session removed) — landed.

## Problem

`spawn-session` blocks synchronously on the worker's SessionStart hello (60s
default). A cold nix-cache harness boot regularly exceeds the window (observed
twice on 2026-08-10), pinning the caller's MCP request for the full wait and
leaving a dangling worktree + session state to reap on timeout.

## Decisions (interactive)

1. **Timeout → reap only if the worker process is dead.** spinclass does not
   track the worker PID (it guarantees detachment; the entry process exits
   promptly). Liveness signal: the worker's `.spinclass/spawn.log` mtime/activity
   — recent activity ⇒ still booting (keep + name the session in the wake);
   stale ⇒ dead/stalled (auto-reap, clean by construction — pre-hello, no
   commits, the `close-child-session` no-force condition). The heuristic errs
   toward KEEP, so an actively-booting worker is never yanked. (Tuning lever: the
   staleness window; a clown-presence query is a possible future upgrade.)
2. **Default hello-timeout 5m for async `spawn-session`** (sync `sc spawn` CLI
   stays 60s). Async makes the wait costless, so a generous window just stops
   cold boots from failing. `hello-timeout` still overrides.
3. **`spawn-session` is async-only** — one MCP tool, no separate
   `spawn-session-async`. Async needs clown's wake channel, so: under clown the
   tool returns the session key + a ringmaster job id immediately and delivers
   the hello/timeout as a wake; without clown it falls back to synchronous
   blocking (no wake channel). The `sc spawn` CLI (human shell, no wake) stays
   synchronous.

## Design

### spawn.Launch split

Split `spawn.Launch` into:
- `LaunchDetached(home, repoPath, driverKey, brief, desc, model)` → the
  synchronous prefix: validate templates, create the worktree (`shop.Create`),
  write session state, `startDetached` the entry. Returns the resolved path +
  the pending-hello context (startTime, window, sessionEnv). The session key is
  known here — this is what the async tool returns immediately.
- `WaitHello(rp, driverKey, desc, deadline, startTime, window, sessionEnv)` →
  the blocking tail: `spawnhandshake.WaitForHello`, then the spawn-window, then
  the `Result`. Mirrors merge's `PrepareMerge`/`FinishMerge` split.

`Launch` becomes `LaunchDetached` + `WaitHello` (the sync CLI/fallback path).

### MCP spawn-session handler (async under clown)

1. `LaunchDetached` synchronously → session key + worktree (surfaced right away).
2. `clown.StartJob(ctx, "spawn", clown.Source)` → jobID. Return the session key,
   worktree path, and jobID as the tool result (the driver ends its turn and is
   woken). NOTE the result states the hello is delivered as a wake.
3. Background goroutine: `WaitHello(deadline=5m unless overridden)`.
   - hello → `clown.FinishJob(jobID, succeeded, "worker <key> is up; it will
     message you at <driver>")`.
   - timeout → liveness check on `spawn.log`; dead ⇒ `close.RunResolved` reap +
     `FinishJob(aborted, "spawn timed out after <d>; session <key> reaped (no
     work existed)")`; alive ⇒ keep + `FinishJob(aborted, "spawn hello timed out
     after <d> but the worker may still be booting; session <key> is dangling —
     reap with close-child-session, or it may hello late")`.

A direct clown job (not `internal/job`) is used: the hello-wait is a bounded,
simple wait, and `internal/job`'s worktree-keyed running-map + `OnJobDone` merge
-queue wiring do not fit (the spawn job is about a *worker's* worktree, and must
not touch the driver's merge queue). The job is still inspectable via
ringmaster's `job_status`/`job_read`.

Without clown: run the full synchronous `Launch` (LaunchDetached + WaitHello) and
return the `Result` as today.

### sc spawn CLI

Unchanged: synchronous `Launch`, 60s default. A human wants to see the outcome.

## Rollback

Async is gated on `clown.Enabled()`; the sync path is preserved verbatim as the
no-clown fallback and the CLI path, so a rollback is reverting the handler's
clown branch. No wire-format or persisted-state change.

## Tuning levers

- **Async default hello-timeout: 5m.** Change signal: cold boots still timing
  out (raise) or wedged boots hanging a job too long (lower).
- **spawn.log staleness window** for the reap-if-dead liveness check. Change
  signal: live-but-idle workers wrongly reaped (widen) or dead workers left
  dangling too long (narrow / switch to clown-presence).

## Ordering

After #262 (landed), before #267. #266 is the last spawn-surface item.
