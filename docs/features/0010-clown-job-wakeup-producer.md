---
status: experimental
date: 2026-06-06
promotion-criteria: |
  experimental -> testing: the end-to-end wake pass succeeds against a
  deployed clown >= 7fd142c — (1) a real async merge/check terminal event
  wakes the originating agent via clown job-watch; (2) a directed chat
  message wakes its target session; (3) a broadcast reaches >=2 sessions
  via the broadcast channel; (4) replay verified (event emitted while the
  target's monitor is down is delivered on its next start). NOTE: running
  sessions keep the previous clown binary until the user redeploys home —
  coordinate the pass with a redeploy.
  testing -> accepted: ~1 week of real async jobs + chat in clown mode with
  no missed wakeups and no tuning-lever adjustments; then FDR 0009's
  chat promotion criteria govern deleting the legacy chat-watch monitor.
---

# Clown job-wakeup producer integration

## Problem Statement

Spinclass's async merge/check tools return a job id immediately, but the
agent then has no push signal for completion: it must poll
`session-job-status` (the documented anti-pattern) or block on
`session-job-wait` and re-expose itself to the MCP request timeout.
Similarly, cross-session chat's push path was a bespoke 1s-poll monitor.
Clown's job-wakeup channel (clown RFC-0009) provides the shared
machinery — a durable per-session journal plus a `clown job-watch`
monitor that turns waking events into agent notifications — and
spinclass should produce onto it instead of owning notification
plumbing.

## Interface

`internal/clown` is the producer-side integration; everything funnels
through the clown CLI located via `$CLOWN_BIN` (exported by clown into
every plugin MCP server) with a `PATH` fallback. Spinclass state is
always the system of record; the clown journal is only the wake layer.

**Background-job lifecycle emits** (`merge-this-session-async` /
`check-this-session-async`): gated on **presence** — `CLOWN_BIN` set
means "running under clown, may emit"; there is no spinclass-side
switch. The job goroutine emits `clown job start --label merge|check
--source spinclass` at launch (the returned id is persisted as
`clown_job_id` in the worktree's `job.json`) and `clown job done
--state succeeded|failed|cancelled --message <summary> --result-ref
"spinclass session-job-status"` after the terminal record is durable.
The agent is woken with one notification line; the failed-state message
carries the first `not ok` line of the result when present. Emit
failures are appended to `job.log` (`[clown] ... emit failed`) and
never affect the job result. The synchronous tools emit nothing — their
caller is already awake. The async tool descriptions switch guidance at
serve startup: under clown, "start it and end your turn — the wake
arrives" replaces the poll-discipline warnings.

**Chat wake emits**: gated on **mode** (`SPINCLASS_CHAT_WAKE=clown`,
default `legacy`) because the migration must displace the legacy
`chat-watch` monitor — see FDR 0009's migration section for modes,
rollback, and the chat-specific promotion criteria. `chat-send`
dual-writes (chatroom store first, then `clown job message`); broadcasts
emit once to clown's reserved `*` key (condvar-style channel broadcast,
no fan-out).

**Rollback**: clown's `CLOWN_DISABLE_JOB_WAKEUP=1` turns every emit
into an exit-0 no-op, so the whole facility switches off without
touching spinclass.

## Examples

An agent backgrounds a merge and is woken instead of polling:

    merge-this-session-async        -> "started background merge job ..."
    # agent ends its turn; ~9 minutes later clown job-watch delivers:
    [clown-job] spinclass merge-9f3c1a2b succeeded: merge succeeded · spinclass session-job-status

A failing check wakes the agent with the first failure:

    [clown-job] spinclass check-3a1b2c4d failed: check failed: not ok 2 - pre-merge hook · spinclass session-job-status

A directed chat message (mode=clown):

    chat-send to="clown/sleek-sumac" message="rebase landed"
    # the target session is woken:
    [clown-job] spinclass msg-7e21aa90 message from spinclass/crisp-catalpa: rebase landed · chat-read from=spinclass/crisp-catalpa peek=true

## Limitations

- **`interrupted` never wakes.** That status is assigned by a later
  `session-job-status` read when the owning serve process is found
  dead — a dead producer cannot emit. The pull surface still reports
  it; the channel's at-least-once guarantee covers emitted events only.
- **Terminal events only for jobs.** `progress` emits are deliberately
  skipped in v1: they are journal-only (never wake) and one exec per
  hook-output line is wasteful; `job.log` + `session-job-status` remain
  the observability surface.
- **Wakes require a live monitor host.** Plugin monitors are gated off
  on macOS today (`tengu_amber_sentinel`, see FDR 0009); there the
  journal still records events and `clown job read` /
  `session-job-status` are the pull paths.
- **New clown required.** The `message` waking class and broadcast
  channel need clown >= 7fd142c; running sessions pick it up only after
  a home redeploy. Older monitors treat unknown event types as
  non-waking (journal-only), per RFC-0009's compatibility rule.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| job progress emits | none (journal-only skipped entirely) | one exec per output line is absurd; job.log is the observability surface | demand for `clown job read` mid-job visibility |
| failed-wake message detail | first `not ok` line | names what broke in one line; result_ref carries the rest | messages truncated/unhelpful in practice |
| emit timeout | 10s | local journal append + datagram; generous | wedged-clown warnings appearing in job.log |
| emit execution point | inside job goroutine, sequential before/after hook | keeps Start's immediate-return contract; simple ordering | start-emit latency measurably delaying hook start |

## More Information

- clown **RFC-0009** (job-wakeup channel: journal schema, nudge, replay,
  at-least-once) and clown **FDR-0013** (feature-level treatment) — the
  contract this produces onto; spinclass#104 / clown#110 are the
  originating issues.
- **FDR 0009** (`0009-cross-session-chat-monitor.md`) — the chat
  migration's modes, dual-architecture rollback, and promotion criteria
  for deleting the legacy `chat-watch` monitor.
- spinclass#107 — promoting `SPINCLASS_CHAT_WAKE` into the sweatfile
  cascade.
- `internal/clown` — the producer package (`Source`, `Bin`, `Enabled`,
  `SendMessage`, `StartJob`, `FinishJob`); `internal/job/runner.go` —
  the emit points around the job goroutine.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.3.10+e8ff9ee ([build commit](https://github.com/amarbel-llc/clown/commit/e8ff9ee351c67cfdf06e9e61ebe262ec3aaa247d)).
