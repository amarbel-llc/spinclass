# Hung-session detection: the spinclass side

Date: 2026-10-26. Status: design note, no code. The clown side is clown#319 and
#322 (clown/pure-teak); the questions for clown are at the end. Labels: R read
in code or docs, V verified, I inferred.

## Problem

Restart (FDR 0037) and the restart wakes (bfe4525) handle a session that is
asked to restart. Nothing tells a coordinator that a worker has stopped making
progress. Three cases matter for an unattended run (I):

1. **Stuck tool.** A tool call has been running far past any normal bound: a
   hung shell command, a merge gate wedged outside ringmaster's view, an MCP
   call that never returns.
2. **Waiting on a dialog.** A permission prompt or confirmation that nobody
   will answer. Under dontAsk this should not happen (asks become denies, R
   #369/05ce039), but auto-mode workers and the harness's own dialogs remain.
3. **Silent.** No progress and no idle turn end, without either of the above
   being visible.

An idle session, one that ended its turn and is waiting for a message, is
normal and must not be flagged.

## What spinclass already knows (R)

- The holders of each session and their rights (FDR 0032), so it knows whom to
  tell.
- Whether a merge or check job is running for a session (the job and ringmaster
  records), with its liveness flock (#368). A running gate is progress, not a
  hang.
- RestartingAt and RestartedAt (bfe4525), so a hang during a relaunch is
  already covered by the restart-watch timer.
- The session's principal. Clown resumes by `clown://claude/{principal}` (R,
  pure-teak, clown#318), which suggests the principal is the Claude session id
  (I).

What spinclass does not know: transcript progress, the current tool call, or
whether a dialog is open. Those are the harness's facts.

## Proposed design (spinclass side)

**Facts in, verdict out.** Clown (or the harness) exports per-session facts.
Spinclass classifies them against bounds and tells the holders. The
classification is spinclass's because it already owns the holders, the rights,
the job state and the restart remedy.

1. **Input facts per session,** read from whatever clown exports (question 1):
   - `last_progress_at`: last transcript append, or the last assistant or tool
     event;
   - `turn_state`: idle (turn ended, waiting) | busy;
   - `current_tool` plus `tool_started_at`, when busy in a tool;
   - `dialog_open` plus `dialog_since`, when a prompt or confirmation is up.
2. **Classification,** with bounds in a trusted sweatfile table
   `[health]` read live (I: defaults to be agreed):
   - idle → ok (never flagged);
   - busy with a spinclass merge or check job running → ok (the gate has its
     own watchdog and timeouts);
   - `dialog_open` for more than `dialog-bound` (default 10m) → **waiting**;
   - `current_tool` running for more than `tool-bound` (default 45m) →
     **stuck-tool**;
   - busy, no progress for more than `silent-bound` (default 60m), neither of
     the above → **silent**.
3. **Surface:** a read-only MCP tool `session-health` (and `sc session-health`)
   that reports each session the caller holds an accepted handle on, with its
   verdict, the facts behind it and their ages. It is gated like `list` for
   confined callers (serve gate). The coordinator's health loop calls it, so
   the first version needs no new daemon (pull, I).
4. **Push, later:** a one-shot systemd timer per child, like restart-watch,
   that wakes the holders on a non-ok verdict. Deferred until the pull version
   has been used.
5. **Remedy:** the message names it. For **waiting** and **silent**, the remedy
   is `restart-child-session` (when idle) or asking the worker by chat. For
   **stuck-tool**, an idle restart never fires, because the session never
   idles. The remedy is a forced restart, which is always-ask (#422) and so
   operator-only. That is the main gap; see decision 1.

## Work list (spinclass; estimates I)

| # | Item | Batches | Depends on |
|---|---|---|---|
| 1 | Read clown's per-session facts (format per clown's answer) | 1 | clown export (#319/#322) |
| 2 | Classifier with `[health]` bounds, trusted layers only | 1 | 1 |
| 3 | `session-health` MCP tool and CLI, serve-gated, held children only | 1 | 2 |
| 4 | Coordinator page update (health loop calls it) | under 1 | 3 |
| 5 | Push timer per child | 1 | 3, a ruling |

## Decisions for the operator

1. **Stuck tools while you are away:** may a coordinator force-restart a child
   classified stuck-tool past `tool-bound`, bounded (for example once per child
   per day, only with the `restart` right), or does a stuck tool wait for you?
   Today force is always-ask, so it waits.
2. The default bounds (dialog 10m, tool 45m, silent 60m).

## Interim answer from clown (pure-teak, 2026-10-26 14:48; I, being verified)

- The split is agreed: clown exports facts, and spinclass classifies and wakes
  the holders. Force and `now` stay operator-gated.
- #319 is frontend death, not hang detection: a dead-man socket so that a
  SIGKILLed frontend's unit ends, plus the 7-day RuntimeMaxSec backstop already
  merged in #320. #322 is the notification half, design TBD. Clown will not
  build a detector.
- Facts clown has:
  - UserPromptSubmit (busy) and Stop (idle) from the idle hooks;
  - per-session PermissionRequest and PostToolUse state under
    `$XDG_STATE_HOME/clown/hook-toolcost` (timestamps being checked);
  - the provider pid and the unit name, in the frontend (whether a file records
    them is being checked).
- The transcript mtime in Claude's projects dir is the cheapest progress signal,
  and spinclass can stat it itself without clown.
- Presence lastSeen proves the frontend is alive, not that Claude is
  progressing.
- Under CLOWN_UNATTENDED (dontAsk) no PermissionRequest fires. A stuck session
  there is a tool call that never returns, or a model or API stall, not a
  prompt. So case 2 (waiting on a dialog) applies only to auto-mode sessions.
- Preferred form: a small JSON state file under `$XDG_STATE_HOME/clown/`,
  written by the hooks (busy-since, last hook event time, tool call open
  since), read by spinclass. No new verb.

## Agreed status-file shape (pure-teak 14:49 plus spinclass changes; not built)

R from pure-teak:
- The frontend keeps "busy" in memory only.
- Nothing records "tool running since T".
- Permission state carries no times.
- Under dontAsk an unattended hang reads as "busy since T, no Stop".

So clown writes `$XDG_STATE_HOME/clown/status/<sha256(principal)>.json`. It is
written atomically by the frontend's idle listener, outside the unit, so a
worker cannot forge it. Fields:

- `version`, `written_at`
- `principal`, `spinclass_session_key`, `claude_session_id`
- `turn_running`, `turn_started_at`, `last_hook_at`
- `unit` (when confined), `main_pid`, `transcript_path`
- `frontend_pid` plus `frontend_start_ticks` (asked for by spinclass: an idle
  session writes nothing, so written_at alone cannot tell idle from dead)
- later and optional: `tool_open_since`

Spinclass verdicts:
- **busy too long:** `turn_running` and now - `turn_started_at` exceeds the
  bound. This replaces the separate stuck-tool and silent cases above, which
  clown cannot tell apart.
- **frontend dead:** `frontend_pid` is not alive, or the file is absent while
  the session record says active.
- Optional corroboration: a flat cgroup CPU from `unit`.

## Questions for clown (pure-teak; relay; partly answered above)

1. Which of these facts does clown already have per session: last transcript
   append, the turn state (idle or busy), the current tool and its start time,
   an open dialog and since when, and the claude process state?
2. What do #319 and #322 propose? Does clown detect hangs itself, or only
   export the facts?
3. In what form could spinclass read them? A file under `$XDG_STATE_HOME/clown`
   (preferred: no process spawn per check), a `clown status --json` verb, a
   troupe presence field, or a ringmaster event?
4. Is the principal always the Claude session id, so that spinclass could fall
   back to the transcript file's mtime if clown exports nothing? That would be
   a harness-layout dependency, so clown's call.
5. Does clown see harness dialogs (permission prompts in auto mode,
   confirmation dialogs) at all, or only tool calls?
