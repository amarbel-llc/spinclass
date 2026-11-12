---
status: experimental
date: 2026-11-11
promotion-criteria: |
  experimental -> testing: the operator enables [host-queue] on one host (or
  one test repository through a parent-directory sweatfile), and two
  concurrent gates are seen serialized live, with the waiter's reason in its
  job log and in host-load. testing -> accepted: a week of fleet gates under
  the queue with no wedged gate and no gate admitted past the memory floor,
  and the thresholds re-read against the step-0 cost history.
---

# Host admission queue for gates

Spinclass #424. On a shared host every session's pre-merge gate is a full
build and test run. Run together, they exhaust memory and load and fail one
another; the fleet relied on a coordinator handing out gate slots by hand. This
record makes the hand rule a host-wide admission queue that every merge, check
and guarded gate passes through before its hook starts.

Design notes: `docs/plans/2026-11-05-424-host-merge-queue.md` (options; D
chosen) and `docs/plans/2026-11-10-424-host-admission-queue-follow-up.md`
(the operator's rulings in §10/§10b, the sequence in §11). Bases: FDR 0022 (the
per-repo merge queue: the same flock-marker pattern one level up), FDR 0025
(stacked merges), FDR 0013 (the build worktree the gate runs in).

## Status

Steps 0 to 6 of the §11 sequence:

- 0: cost tracking, `record-costs`;
- 1: the probe;
- 2: the ledger and the predicate;
- 3: the wait in `runHookPhase`;
- 4: the `[host-queue]` admission keys;
- 5: the `host-load` queue section;
- 6: metrics and docs;
- #482: first-come order; #483: the management verbs, priorities, aging and the
  confinement deny-write (see Interface).

The queue is **opt-in**: off unless a trusted sweatfile sets `enable = true`
(operator ruling, 2026-11-11). Held for separate go-aheads:

- `sc host-slot` and `participants`.
- Weighted admission and expected-cost ordering.
- The on-disk ledger RFC.
- The v2 per-node gate history.

## Interface

- **Configuration:** `[host-queue]`, read only from the global sweatfile and
  layers above the repository, per key, nearest wins
  (`sweatfileio.TrustedHostQueue`). A repository's own table is ignored with
  an `sc validate` warning; an invalid value is an `sc validate` error.
  - Keys and defaults (ruled 2026-11-10): `enable` false, `max-gates` 1,
    `memory-floor` 4GiB, `memory-resume` 6GiB, `pressure-memory-avg10` 10,
    `swap-ceiling` 4GiB, `disk-floor` 30GiB, `load-threshold` 1.0 (per-CPU
    load1), `settle` 60s, `poll` 10s, `max-wait` 60m, `idle-grace` 10m.
  - Reference: `spinclass-sweatfile(5)`.
- **Trying it on one repository:** a `sweatfile` with `[host-queue] enable =
  true` in a parent directory of that repository is a trusted layer for the
  repositories below it only. The global sweatfile stays untouched.
- **Waiting:**
  - The job log names the reason at most every 30 s.
  - A gate that waited gets one `host queue wait <t> (<reasons>)` point.
  - After `max-wait` the gate fails with `host queue timeout` and the last
    reason; the attestation stays buffered, as for any failed gate (11-05 Q4).
  - There is no per-gate escape (11-05 Q1).
- **Visibility:** `host-load` lists running gates, waiters (with reason and
  age) and the hold. Its `threshold` defaults to the trusted `load-threshold`.
- **Metrics:** `host.queue_wait.<outcome>`, `host.wait_reason.<reason>`,
  `host.gates_running`, `host.queue_grace` (11-05 Q7 ruling).

## Design

- **Where:** `check.runHookPhase`, before madder and the hook. One admission
  covers merges (inside the per-repo lock, after the landing fetch), checks
  and the guarded gate. Repair, post-merge and the pre-commit hook are not
  admitted in v1 (11-05 Q5).
- **Ledger:** see `spinclass-internals(7)`, HOST ADMISSION QUEUE.
- **Predicate:** `hostqueue.Decide` checks these in order, and the first
  failure is the reason:
  1. the operator's hold;
  2. the queue: an earlier waiter is ahead (#482; first come, first
     admitted; the reason reads `position N of M: behind <session>`);
  3. the gate cap;
  4. memory, with floor and resume hysteresis;
  5. PSI memory pressure, or the swap ceiling where PSI is absent;
  6. store free space (#430 folded in, 11-05 Q9);
  7. per-CPU load1;
  8. the settle window.

  `idle-grace` admits past failing probe terms, never past a hold, an earlier
  place or the cap, once they have failed continuously with no gate running.
- **Order (#482):** a gate takes a numbered place under the ledger lock at its
  first look; admission goes by place, not by who polls first. A gate that
  leaves (timeout, cancel, crash) gives up its place.
- **Management (#483, landed):** `sc queue-status` (also a read-only MCP tool),
  `sc queue-hold`, `sc queue-release`, `sc queue-priority`, `sc queue-cancel`,
  `sc queue-hold-waiter`, `sc queue-release-waiter`. The queue order is effective
  priority (low, normal, high; aging one level per 30 min), then place. A held
  waiter keeps its place, is skipped, and pauses its max-wait clock and aging;
  holds expire at max-wait (never with `max-wait = "0"`). A cancel is taken
  before the gate's decision; one that loses the race is journalled too late.
  Design: `docs/plans/2026-11-12-483-host-queue-management.md`.
  - **v1 authority:** the operator at a terminal only (stdin and stdout are
    terminals, as for `sc handle-grant`). Every verb passes
    `hostqueue.Authorize`, and every act is journalled to
    `$XDG_STATE_HOME/spinclass/host-queue/journal.ndjson` with actor `tty`
    (as strong as isatty, not an authenticated act). Priority and cancel fail
    closed without a journal line; the whole-queue hold and release fail open
    with a warning, since the hold is the brake. No handle right (not `merge`
    or `check`, which are execution-class) delegates queue control.
  - **Later:** a narrow object right `spinclass:queue`, not in the default
    grant, is one more case in `Authorize`. Target state (operator ruling
    2026-11-12): an unattended parent holding it may cancel its child's waiter,
    journalled; v1 grants nothing of that kind. Priority stays operator policy;
    sessions may only lower it.
  - **Confinement:** the ledger directory under `$XDG_RUNTIME_DIR` (kept out by
    F14) and `$XDG_STATE_HOME/spinclass/host-queue` (a protected state dir,
    F16) are not reachable from a confined unit (FDR 0035).
  - **Command lifecycle:** a verb writes a command file that names the waiter by
    place, session, pid and wait start rather than the marker's inode (a waiter
    rewrites its marker on every reason change, so the inode is unstable); the
    waiter applies it at its next look. The files are not a security boundary
    against same-uid code; the tool is the check.
- **Stale binaries:** the gate runs in the calling session's serve, so a serve
  that predates the queue code runs gates unqueued. `host-load` flags it
  (`QUEUE BYPASS`), and `just explore-live-queue-bypass` checks from a shell.
- **Failure stance:** a ledger, probe or config that cannot be used admits the
  gate with a warning. The queue delays gates; it must never wedge them.

## Limitations

- Host-local, like FDR 0022's flock. A `host:` remote session is admitted on
  the host it runs on.
- Work outside spinclass gates (the circus cascade, a terminal build) is seen
  only through the probe, not reserved, until `sc host-slot` (step 7).
- nix-daemon builds land in the daemon's cgroup. The probe sees their load and
  memory, but per-gate attribution (step 0) undercounts them.
- `load1` is reactive. The 11-05 Q2 choice of load1 versus load5, and the 60 s
  settle, are unruled and kept as drafted.
- The result text of `merge-this-session-async` and `check-this-session-async`
  does not yet say that a gate may wait for a host slot. The job log and
  `host-load` say so.
