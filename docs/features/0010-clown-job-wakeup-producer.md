---
status: testing
date: 2026-06-06
promotion-criteria: |
  experimental -> testing: PROMOTED 2026-06-06. The end-to-end wake pass
  succeeded against a deployed clown >= 7fd142c — (1) a real async
  merge/check terminal event wakes the originating agent via clown
  job-watch: OBSERVED 2026-06-06, both terminal flavors (failed wake in
  spinclass/crisp-catalpa carrying the first `not ok` line; succeeded
  wake organically in clown/sleek-sumac); (2) a directed chat message
  wakes its target session: OBSERVED 2026-06-06 (raw `clown job
  message` leg, then the spinclass chat-send dual-write leg from a
  sender running the presence-gated emit binary — self-echo
  msg-44d10e6a, and moxy/kind-fig confirmed the directed dual-write
  message exactly once via [clown-job]); (3) a broadcast reaches >=2 sessions via the broadcast
  channel: OBSERVED 2026-06-06, three exactly-once receipts on the raw
  leg (sender self-echo — see spinclass#108 — plus two peer sessions,
  one freshly attached), then two peer exactly-once `[clown-job]`
  confirmations on the chat-send dual-write leg
  (tommy/solid-mulberry — inactive at send, so likely the first
  organic replay observation — and madder/clear-larch); (4) replay
  (event emitted while the target's monitor is down delivered on its
  next start): per agreement with the clown side, covered by clown's
  RFC-0009 §9 bats conformance (replay-unacked-on-start, ack-gated
  exactly-once, condvar first-attach); tommy/solid-mulberry's
  inactive-at-send delivery is the likely organic live observation.
  testing -> accepted: ~1 week of real async jobs + chat under clown with
  no missed wakeups and no tuning-lever adjustments. (FDR 0009's chat
  promotion was executed 2026-06-06 by user direction: the legacy
  chat-watch monitor and SPINCLASS_CHAT_WAKE are deleted; this channel
  is the sole chat push path.)
---

# Clown job-wakeup producer integration

> **Post-cutover note (2026-06-14):** cross-session chat left spinclass
> entirely and is now a clown construct — see
> [FDR-0017](0017-clown-session-attach-grouping-chat-rescope.md). The chat
> references below are specifically the now-removed `chat-send` dual-write
> *wake* leg; the async-job-lifecycle emit half (`clown job start`/`done`
> for merge/check) is unaffected and remains the live producer integration.

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

**Chat wake emits**: gated on **presence** like the job emits —
`CLOWN_BIN` set means emit; presence-gating is the only gate (the
legacy chat-watch monitor and its `SPINCLASS_CHAT_WAKE` receive-side
mode were deleted 2026-06-06 — see FDR 0009's deprecation record).
`chat-send` dual-writes (chatroom store first, then `clown job
message`); broadcasts emit once to clown's reserved `*` key
(condvar-style channel broadcast, no fan-out).

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

A directed chat message:

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
- **FDR 0009** (`0009-cross-session-chat-monitor.md`, deprecated) — the
  legacy `chat-watch` monitor this channel replaced, its migration
  history, and the executed promotion/deletion record.
- spinclass#107 — promoting `SPINCLASS_CHAT_WAKE` into the sweatfile
  cascade (obsolete: the variable was deleted with the legacy monitor).
- `internal/clown` — the producer package (`Source`, `Bin`, `Enabled`,
  `SendMessage`, `StartJob`, `FinishJob`); `internal/job/runner.go` —
  the emit points around the job goroutine.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.3.10+e8ff9ee ([build commit](https://github.com/amarbel-llc/clown/commit/e8ff9ee351c67cfdf06e9e61ebe262ec3aaa247d)).
