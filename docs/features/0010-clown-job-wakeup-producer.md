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

> **CLI rename note (2026-07-03):** clown RFC-0015 split the `clown job`
> subcommands into a standalone `ringmaster` binary (shipped alongside `clown`
> on PATH). spinclass's producer (`internal/clown`) now shells out to
> `ringmaster start`/`ringmaster done` instead of `clown job start`/`done` — a
> behavior-preserving rename (same flags, exit codes, output). ringmaster is
> resolved from PATH (override with `$RINGMASTER_BIN`); spinclass deliberately
> does **not** pin clown as a flake input for it (that would drag clown's whole
> input closure in for one small binary) — the job platform is slated to move
> to its own lightweight repo, pinnable then. The `clown job …` spellings below
> are historical; read them as `ringmaster …`. The design and wake semantics
> are unchanged.
>
> **Superseded in part (2026-07-28, spinclass#253):** the move happened. See
> the *checkPhase pin* amendment below — the closure rationale no longer holds
> and ringmaster IS now a flake input, though only for tests.

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
every plugin MCP server) with a `PATH` fallback. Spinclass state remains
the system of record for job *results*; the clown journal is the wake
layer — but see the **id adoption** amendment below, which makes the
start emit load-bearing rather than purely additive.

**Background-job lifecycle emits** (`merge-this-session-async` /
`check-this-session-async`): gated on **presence** — `CLOWN_BIN` set
means "running under clown, may emit"; there is no spinclass-side
switch. Spinclass emits `ringmaster start --label merge|check --source
spinclass` and, after the terminal record is durable, `ringmaster done
--state succeeded|failed|cancelled --message <summary> --result-ref
"spinclass session-job-status"`. The agent is woken with one
notification line; the failed-state message carries the first `not ok`
line of the result when present. The synchronous tools emit nothing —
their caller is already awake. The async tool descriptions switch
guidance at serve startup: under clown, "start it and end your turn —
the wake arrives" replaces the poll-discipline warnings.

### Amendment (2026-07-28, spinclass#243): id adoption

The original design allocated the clown job **inside the job goroutine**,
so `Start` could return before it completed, and treated every emit
failure as non-fatal. Both were revised:

- **The start allocation now runs before the goroutine**, and its returned
  id becomes the job's own `ID` (still also persisted as `clown_job_id`).
  Previously spinclass minted `<kind>-<unix-ts>` while the wake carried
  ringmaster's `<kind>-<hash>` — the same prefix with a different suffix,
  so the two read as one scheme and an agent matching them concluded its
  own wake belonged to another session. Reproduced 4/4 before the fix.
- **A failed start allocation under clown now refuses the dispatch.** The
  wake is how an agent learns an async job finished, so a job dispatched
  without one completes into silence. This is the one place the
  integration is **no longer purely additive**: the wake layer can now
  block a merge from starting. It cannot corrupt or fail a *running*
  job — the terminal emit stays non-fatal and logged — and the rollback
  (`CLOWN_DISABLE_JOB_WAKEUP=1`) still disables the facility wholesale.
  Without clown at all, nothing changes: the caller's local id stands.

Dispatch-time allocation is what `ringmaster start` is for; it measured
~7ms on the reference host (5–6ms of that bare process startup) against a
dispatch that has already paid for `PrepareMerge`'s network `git pull` and
rebase, so the original "a slow clown must not delay Start's
immediate-return contract" rationale guarded a cost that was not there.
That contract now means "does not wait for the job body," which is what
`internal/job`'s tests pin.

### Amendment (2026-07-28, spinclass#253): checkPhase pin

This record twice justified resolving ringmaster from bare PATH on the grounds
that pinning would "drag clown's whole input closure in" and that the platform
was only *slated* to become standalone. Both are now false: `ringmaster` is an
extracted repo whose four inputs (`igloo`, `nixpkgs-master`, `utils`, `bats`)
are a strict subset of spinclass's, so every one `follows` an existing pin.
Adding it grew the lock by four `follows` lines and no transitive inputs.

The consequence of the old position was not merely stylistic. **No sandboxed
lane had a `ringmaster` binary, so no part of this contract had ever been
regression-tested.** `internal/job`'s and `internal/clown`'s suites assert argv
against shell stubs they install themselves, and a stub agrees with whatever it
is sent — the `--state` flag could have been renamed upstream and every test
would still have passed. That blind spot had been open since this record
landed.

ringmaster is now a **checkPhase input only** (`nativeCheckInputs`), which:

- keeps the shipped closure unchanged — it is a build-time input, never a
  runtime dependency, and `packages.default` / `checks.spinclass` stay one
  derivation;
- leaves **runtime** resolution on PATH deliberately. A runtime pin would be
  actively wrong: the wake must land in the journal of whatever clown is
  hosting the process, so the binary has to be the one clown ships, not one
  spinclass froze at build time;
- makes `internal/clown/contract_test.go` drive the real lifecycle (`start` →
  `spool-path` → spool write → `status --tail` → `done` → `read`) against a
  scratch `XDG_STATE_HOME`, verified hermetic: `done` succeeds with no monitor
  bound, and nothing escapes the temp journal.

The suite skips when no binary is reachable so a devshell `go test` still
passes, but **fails hard inside the sandbox** (`NIX_BUILD_TOP` set) if the pin
is ever dropped. Without that guard a removed pin would silently return
coverage to zero while the lane stayed green — the precise way the gap stayed
invisible the first time. Verified by removing the pin: both tests fail with
that message; restored, both pass.

Two contract facts this immediately corrected, neither observable through a
stub: `spool-path` is a **pure path computation**, not a lookup (it answers
normally for a job that never existed; only a *malformed* id is an error), and
`done` rejects an unknown flag with exit 2 rather than ignoring it.

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
| start-emit execution point | before the goroutine, on the dispatch path (#243) | the returned id must be the id the wake carries; measured ~7ms against a dispatch already doing a network pull | start allocation measurably delaying dispatch |
| start-emit failure policy | refuses the dispatch (#243) | a job with no wake completes into silence, which is worse than not dispatching | refusals seen for transient clown hiccups rather than real breakage |
| terminal-emit failure policy | logged, non-fatal | the work is already done and recorded; only the wake is lost | agents missing completions without noticing |

## Future direction

**Retiring spinclass's pull surface** (spinclass#251). Since #243 the two
sides name the same job identically, which raises whether spinclass should
own `session-job-status`/`-cancel`/`-wait` at all rather than producing onto
the channel and letting `ringmaster status`/`tail`/`wait`/`cancel` be the
interface. Measured today, ringmaster knows almost nothing about a spinclass
job: two journal records, a one-line message, `spool_bytes: 0`, and a
`result_ref` pointing back at `session-job-status`. Closing that gap means
writing hook output to `ringmaster spool-path` and attaching the rendered
verdict ladder via `done --resource <madder-uri>` — both worth doing on their
own merits, since a wake that carries its result beats one that says "call my
other tool."

Retiring the tools themselves would **invert this record's model**: the
journal, not `job.json`, would become authoritative for job observability.
It also needs spinclass to implement its half of ringmaster's *cooperative*
cancel (observe the `cancelled` record, tear down its own process tree —
ringmaster records no worker PID by design), which in turn is gated on the
pre-merge hook's teardown actually being prompt (spinclass#188). Not decided;
tracked in #251.

**Piece 2 is done** (2a `96469e0`, 2b spinclass#251). The hook's output is teed
into ringmaster's spool, and the terminal emit attaches the rendered verdict
ladder as a `madder://blobs/<digest>` through `done --resource` instead of the
self-referential `result_ref`. The two are alternatives rather than companions:
where a blob exists it *is* the result, so `result_ref` survives only as the
fallback for a build with no madder pin. Both are best-effort — a failed blob
write degrades to no attachment and logs, because the job has already finished
and its result is durable in `job.json`, so failing the wake over a missing
attachment would trade a working notification for none.

One contract detail found by driving the real binary: plain `ringmaster read`
renders attachments as a count (`· 2 resource(s)`) rather than listing them;
`--json` is required to recover the URIs. So an agent reading only the wake
line learns *that* a result is attached and how many, and must go to the JSON
to fetch one.

One prerequisite that *was* blocking it has cleared. Retirement would sharply
raise spinclass's reliance on the ringmaster contract — today a silent upstream
change loses wakes (recoverable: `job.json` is still the record and
`session-job-status` still works), whereas afterwards it would lose job
observation and cancellation outright, with no fallback. Betting that on an
untested contract was not defensible while no lane could exercise one. The
#253 pin removes that objection: the contract is now covered, and the
remaining questions are the model inversion and cooperative cancel above.

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
  the start allocation ahead of the job goroutine and the terminal emit
  inside it.
- spinclass#243 — the id-adoption amendment above (dispatch-time
  allocation, refuse-on-failure). spinclass#251 — the proposed retirement
  of the pull surface. spinclass#188 — the pre-merge teardown latency that
  gates cooperative cancel. spinclass#253 — the checkPhase pin amendment
  above (stale closure rationale, first real coverage of the contract).
- `internal/clown/contract_test.go` — the real-binary suite; contrast with
  `clown_test.go` / `internal/job/wake_test.go`, which stub ringmaster and
  can only verify the argv spinclass *intends* to send.
- `ringmaster(1)` — the job-control CLI. Note its CAVEATS: `cancel` is
  cooperative, not forceful, because the journal deliberately records no
  worker PID; the owning producer is expected to tear down its own process
  tree. `cancel`, `ls` and `tail` are CLI-only — the `mcp` subcommand
  exposes only `job_start/progress/done/read/status/spool_path/wait`.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.3.10+e8ff9ee ([build commit](https://github.com/amarbel-llc/clown/commit/e8ff9ee351c67cfdf06e9e61ebe262ec3aaa247d)).
