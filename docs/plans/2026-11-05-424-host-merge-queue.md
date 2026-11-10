# A host-wide gate admission queue (design exploration for #424, 2026-11-05)

> **2026-11-10:** the operator answered §7's questions. The rulings are
> recorded in `2026-11-10-424-host-admission-queue-follow-up.md` §10b. That
> note also renames the commands to `sc queue-*`; the section stays
> `[host-queue]`.

Design note, no code. The operator asked (through the coordinator, comment on
spinclass#424 of 2026-11-05) for a host-specific merge queue to be explored
when there is spare bandwidth: one queue per host across repositories, admitting
a pre-merge gate only when host load and available memory allow, weighted by
how heavy the gate is, interacting with the per-repo queue (FDR 0022) and the
stacked-merge queue (FDR 0025, #265), with wait reasons visible to agents
(#316, #366). Related: #387 (resource budgets for confined workers), #388 (the
`host-load` tool), #430 (disk floor), #131 (the original concurrency
exploration). This note lays out the options, recommends one, lists the open
questions for the operator, and says what the existing `host-load` tool can
already feed it.

Labels: **R** read in code or docs, **V** observed in a run, **I** inferred or
proposed, **U** unverified.

## 1. What exists (R)

**The per-repo landing lock (FDR 0022).** `merge.FinishMerge` takes an
advisory `flock(2)` on `spinclass-merge.lock` in the repo's git common dir
before the gate and holds it through landing, push and post-merge
(`internal/delta/merge/merge.go:535-582`). Under the lock: landing fetch,
ancestry check, landing rebase in a `.land-*` worktree when the tip moved,
the gate on the landing sha, `ff-only`/push, post-merge. Waiters heartbeat
`merge queue: waiting behind <session> (<elapsed>)` to the async job log every
30 s and emit one `merge queue wait <elapsed> (behind <session>)` test point
on acquisition (`merge.go:563-582`). Queue depth is telemetry from a marker
directory, `spinclass-merge.queue`, one flocked marker per merge, dead markers
removed rather than counted (`internal/0/mergelock/queue.go`, #363). The lock
is host-local and self-releases on process death; there is no daemon.

**The intra-session stack (FDR 0025).** A second `merge-this-session-async`
while a gate runs enqueues, in-process and per worktree
(`cmd/spinclass/merge_queue.go`). `job.OnJobDone` dequeues the head after the
running job ends; a failed merge drains the queue with one aborted wake. A
dequeued merge gets a start notice carrying its job id (#366, closed). A
queued merge has no job id until then (#316, open).

**The gate funnel.** Every gate, whatever asked for it, runs through
`check.runHookPhase` (`internal/charlie/check/check.go:308`, `:373`, `:556-563`):
the repo's pre-merge via `hookrun.PreMergeInDir`, or the guarded gate via
`hookrun.ConfinedPreMergeInDir` (#449). Callers: `merge.FinishMerge` (under the
repo lock), `sc check` and `check-this-session(-async)`. **A check takes no
lock**: the check package never references `mergelock`, so checks run
concurrently with each other and with merges' gates, even in one repository.

**Watchdogs.** `[hooks].inactivity-timeout` wraps only the hook subprocess, so
time spent waiting for a lock is exempt (`merge.go:553-557`). Post-merge runs
under the repo lock with a default 10-minute wall-clock cap
(`sweatfile.DefaultPostMergeTimeout`).

**`host-load` (#388).** `internal/0/hostload.Collect` reads `/proc/loadavg`,
counts CPUs from `/proc/stat`, samples per-process `utime+stime` over a window
(default 500 ms) and attributes the busiest processes to sessions by process
`cwd` against the session list. It reports `load1/5/15`, `load_per_cpu`
(`load1 / cpus`), a `threshold` (parameter, default 1.0) and `over_threshold`.
The `/proc` root is injectable, so tests run against a fake tree. The MCP tool
refuses a confined caller; the CLI does not (`cmd/spinclass/hostload_cmd.go`).
The threshold was deliberately left as a parameter, not a sweatfile key, until
the admission behaviour that consumes it is decided (#387 comment, firm-mulberry).

**What spinclass does not read today.** Nothing reads `/proc/meminfo`, and
nothing calls `statfs` (grep over non-test Go: no match). There is no `[host]`,
`[admission]` or `[resources]` sweatfile section. #430 (disk floor) is open and
unbuilt.

**Metrics (#363).** Per phase, `merge.<repo>.<phase>.<outcome>` timings,
including `queue_wait`; `merge.<repo>.queue_depth` and `stack_depth` gauges.
No memory or disk figure is recorded for a gate, so a gate's weight cannot be
read from history yet.

**Confinement.** A confined worker runs in a transient systemd user unit (FDR
0035). The gate, however, is started by the session's `spinclass serve` in the
detached build worktree, and nix builds are forked by `nix-daemon` in its own
cgroup (#387 comment); so unit budgets, when they exist, would not bound a gate
(U: which cgroup the gate lands in today has not been checked).

## 2. The problem as observed (V, from the issue threads and this session)

- 2026-10-20: on one 8-CPU host, four gates and two long evaluations ran at
  once, load 40-72; at least one gate failed on a timeout that looked
  load-induced (U, #387 comment).
- 2026-10-25/26: concurrent gates drew the root filesystem from 178 GB to
  58 GB free within hours; every gate was held by hand (#430).
- 2026-11-04/05: with a `circus update` cascade running in a terminal beside
  several workers, the coordinator serialized merges by chat: a HOLD line to
  every worker, release one at a time, watching available memory with a hold
  line near 4 GB and a resume line above 6 GB, and a stagger order after each
  hold (this session received and followed those lines). One worker (this one)
  started a test job at 0.9 GB available before a hold arrived.

The per-repo lock prevents none of this: the colliding gates were in different
repositories, and the cascade is not a spinclass gate at all. The coordinator
is the host queue today, by hand.

## 3. Design axes

Four questions, mostly independent:

1. **What is counted.** Admissions (a count cap), declared weights (a budget),
   or measured host state (load, memory, disk), or a combination.
2. **Where the wait happens.** Around the gate alone, around the whole
   repo-lock critical section, or at the tool call before anything is pinned.
3. **How admissions are coordinated across processes.** Each session has its
   own `spinclass serve`; there is no host daemon. Candidates: a host-wide
   flock plus a marker ledger (the FDR 0022 pattern), or a new daemon.
4. **What the waiter is told.** Heartbeats, a test point, the enqueued-merge
   text, `host-load`, `sc list`.

## 4. Options

### A. One host-wide gate lock (count = 1)

A second flock, host-wide (under `$XDG_STATE_HOME/spinclass/`), taken around
the gate in addition to the per-repo lock. Exactly what the coordinator does
by hand: one gate at a time on the host.

- For: smallest change; reuses `mergelock` verbatim with a different path; the
  same heartbeat and test point shape; self-releasing on death; checks can
  take it too.
- Against: a 2-minute docs gate waits behind a 30-minute build gate; it sees
  nothing outside spinclass (the cascade keeps running beside it); on a
  16-core host with memory to spare it leaves the host idle.

### B. A weighted budget with a marker ledger

Each gate declares a weight (CPUs, memory) and the host has a budget. The
coordination is the `spinclass-merge.queue` pattern at host scope: a ledger
directory of one flocked marker per admitted gate, each naming its session,
weight and admit time; a short ledger lock serializes the admission decision;
admit while the sum of live weights plus mine fits the budget. Dead markers
(lock takeable) are reaped on read, so a crash never leaks capacity.

- For: no daemon, crash-safe, FIFO by marker time, and the ledger is a
  readable record of who holds what, which is what agents and `host-load`
  want to show.
- Against: weights are declarations, not measurements; a repository that
  under-declares admits itself; there is no measured gate cost to seed the
  numbers from (section 1, metrics); it still sees nothing outside spinclass.

### C. Admission by host state (probe)

Before admitting, read `/proc/loadavg`, `/proc/meminfo` (`MemAvailable`) and
`statfs` of the nix store filesystem; admit only when per-CPU load is under a
threshold, available memory is above a floor, and free disk is above a floor
(#430); re-probe on an interval while waiting; apply hysteresis the way the
coordinator does (hold under a floor, resume only above a higher line).

- For: this is the only option that sees the cascade and every other
  non-spinclass load; it is what the operator and coordinator actually watch;
  the disk floor of #430 is one more predicate in the same probe.
- Against, if used alone: a gate that was just admitted has not shown its load
  yet, so every waiter probing at the same instant sees "ok" and all start
  together (the herd). A probe needs a reservation to be safe, which is B's
  ledger, or at least a settle window after each admission.

### D. B's ledger with C's probe as the predicate (recommended)

Admission is one function evaluated under the ledger lock: admit when (1) the
count of live markers is below the cap, (2) the probe passes (load, memory,
disk, with hysteresis), and (3) the last admission on the host is older than a
settle window. Weights are a later refinement of (1): a count cap of one or two
is enough to start, and weights can be added when gate costs have been
measured.

- For: the ledger gives reservations and visibility; the probe gives the
  outside view; the settle window closes the herd; every piece reuses a
  pattern already in the tree (`mergelock`, `hostload`).
- Against: three knobs to default well; a probe is only as good as its
  thresholds; a wedged host (memory never returns) needs a timeout rule.

### E. A host gatekeeper daemon

A long-lived process owning admission, answering over a socket. Rejected for
v1: spinclass runs no host daemon today, a daemon's death would wedge every
gate or need its own reaper, and the flock-ledger model already gives
crash-safe cross-process coordination with no new unit. Revisit if the policy
grows beyond what a predicate over a ledger can express (dependency-aware
ordering, fair-share across repositories).

### F. cgroup budgets instead of a queue (#387)

Orthogonal, not a substitute: limits make load predictable but schedule
nothing (eight workers capped at four CPUs each still oversubscribe eight
CPUs), the gate does not run in the worker's unit, and nix-daemon builds
escape the unit anyway. #387 stays its own design; a queue is wanted either way.

## 5. The recommended shape, in detail (I)

**Where.** Inside `check.runHookPhase`, immediately before `run(hookCtx, sink)`
(`check.go:562`), so one admission covers the repo's pre-merge, the guarded
gate, merges and checks alike. For a merge this is **inside** the per-repo
lock: a repo-lock holder waiting on the host blocks its repository's queue,
but nothing in that repository could run anyway, and the landing fetch and
ancestry check already happened. Admitting before the repo lock instead would
let a host slot be held while waiting for a repo lock, wasting it.

**Not admitted through it in v1.** The repair phase (runs in the session
worktree before the pin, bounded by `repair-timeout`), post-merge targets
(under the repo lock after landing, bounded by `post-merge-timeout`; a deploy
waiting on host memory would hold the repository), and the pre-commit hook.
Open question 5 asks whether post-merge should join later.

**The predicate.** Under the ledger lock, in this order, each failure naming
its reason:

1. count of live markers < `max-gates` (default 1, the hand-run practice);
2. `MemAvailable` ≥ `memory-floor`, or, when this waiter has already been
   refused once for memory, ≥ `memory-resume` (hysteresis; defaults to the
   coordinator's 4 GB / 6 GB unless the operator says otherwise);
3. free bytes on the nix store filesystem ≥ `disk-floor` (#430 folded in);
4. `load1 / cpus` ≤ `load-threshold` (default 1.0, the `host-load` default);
5. the newest marker is older than `settle` (default 60 s), so a just-admitted
   gate has shown its load before the next decision.

An all-pass admits: write the marker (session key, repository, weight 1,
admit time, the reasons it waited), release the ledger lock, run the gate,
remove the marker after the gate (deferred, idempotent). A fail waits and
re-probes every `poll` (default 10 s) under context cancellation.

**Waiting is bounded.** `max-wait` (default 60 m, "0" for none). On expiry the
gate fails with `host queue timeout: <last reason>`; the merge is a plain
retry, and the attestation is released, not consumed (the FDR 0025 rule for a
merge that never ran). The alternative, admit anyway with a warning, is open
question 4.

**What the waiter is told.**

- Heartbeats to the async job log every 30 s, the FDR 0022 cadence:
  `host queue: waiting for memory (3.1 GB available, resume line 6 GB), 1 gate
  running (clown/rare-walnut), 2m30s`.
- One test point on admission: `host queue wait 4m10s (memory; behind
  clown/rare-walnut)`, a sibling of `merge queue wait`.
- The result of `merge-this-session-async`, `check-this-session-async` and the
  stacked-merge start notice (#366) gain one sentence: "its gate waits for a
  host slot before it runs; the job log names the reason".
- `host-load` gains a `gates` section read from the ledger: each admitted gate
  (session, repository, since when) and each waiter (session, waiting since,
  last reason). The tool's `threshold` default becomes the sweatfile's
  `load-threshold`, the one-line change the #387 comment anticipated.
- `sc list` stays as it is (FDR 0022 made the same choice); open question 10.

**Configuration.** A new trusted-layer section, read like `[health]` and
`[confinement]` through `sweatfileio` (global and layers above the repository
only; a repo or worktree value is ignored), because it is host policy a
repository must not relax:

    [host-queue]
    max-gates = 1
    memory-floor = "4GiB"
    memory-resume = "6GiB"
    disk-floor = "30GiB"
    load-threshold = 1.0
    settle = "60s"
    poll = "10s"
    max-wait = "60m"
    disable = false

`disable = true` is the rollback (no ledger, no probe, today's behaviour),
mirroring `disable-merge-queue`. A per-gate weight, when added, would be
`[hooks].gate-weight` read from the repository with a floor of 1 and a
trusted-only `budget`; see open question 3.

**Metrics.** `host.queue_wait.<outcome>` (ok, timeout, cancelled),
`host.gates_running` gauge at admission, `host.wait_reason.<reason>` counter,
and, per gate, `MemAvailable` before and after and the peak load during, so
weights can be seeded later from real costs.

**Interaction with the per-repo queue.** Unchanged. The repo lock still
serializes landings and still runs the gate on the landing sha; the host queue
is nested inside it. The FDR 0022 `disable-merge-queue` path (`finishMergeUnqueued`)
also runs its gate through `runHookPhase`, so it is admitted too.

**Interaction with the stack (FDR 0025).** Unchanged. A dequeued merge runs
`PrepareMerge` and `FinishMerge` inside its job; its gate then waits for host
admission like any other. The start notice's "its gate is running now" is
softened as above. A drained queue is unaffected.

**Checks.** Admitted through the same funnel, which is new: today a check runs
at once. This is deliberate: a check is the same gate with no landing, and the
2026-10-20 pile-up counted checks among the gates. `sc check` from a terminal
is admitted too; a `--no-host-queue` escape for the operator is open question 1.

**Non-gate work.** Not a participant in v1. The probe sees its load and memory
and waits for it, which already respects the cascade. A later `sc host-slot
<cmd>` (or an MCP tool) could let a plain `just` recipe or `circus update` take
a marker, as the #387 comment asked; open question 6.

**Cross-host.** None, as with FDR 0022: flock is host-local. A `host:` remote
session (FDR 0011) is admitted on the host it runs on.

**Load-induced failure.** When a gate fails and the probe was over threshold
at any sample during its run, the failure result carries one line saying so,
so an agent does not mistake a load-induced timeout for a real one. Automatic
retry is open question 7 (it doubles the gate time and can mask a real
failure).

## 6. What `host-load` already feeds, and what it lacks

| Needed by the predicate | In `hostload` today | Gap |
|---|---|---|
| CPU count | `countCPUs` | none |
| `load1`, per-CPU load, threshold verdict | `Collect`, `Report.OverThreshold` | threshold is a parameter, not a key |
| who is loading the host | `busiest` + session attribution | none; also tells a waiter whom it waits behind |
| available memory | not read | add `MemAvailable` from `<root>/meminfo`; the injectable `/proc` root makes it testable the same way |
| free disk on the store filesystem | not read | `statfs` of the nix store path (#430); not under `/proc`, so a second injection seam |
| admitted gates and waiters | nothing | the ledger directory; `host-load` reads it |
| a sustained view | `load5`, `load15` reported | the predicate could use `load5` to avoid admitting into a spike; open question 2 |

`Collect`'s per-process sampling (500 ms sleep) is not needed for the
predicate; the probe should read load, memory and disk without the sample and
without session attribution, and `host-load` should keep both.

## 7. Open questions for the operator

1. **Scope of v1.** Merges and checks both (recommended), or merges alone?
   Should the operator's `sc check` at a terminal have a `--no-host-queue`
   escape, or does the operator wait like everyone else?
2. **Default lines.** `max-gates` 1 or 2? Memory floor and resume at the
   practiced 4 GB / 6 GB, or scaled to the host (a fraction of total)? Load on
   `load1` (reactive) or `load5` (sustained)? Settle 60 s?
3. **Weights.** Start with a count cap and add weights after a few weeks of
   the new per-gate memory and load metrics (recommended), or declare weights
   from day one? If declared, trusted-only budget with a repository-declared
   weight floored at 1, or trusted-only both?
4. **Timeout behaviour.** After `max-wait`, fail with the reason and keep the
   attestation (recommended, matches FDR 0025), or admit with a warning so an
   unattended worker is never stranded by a wedged host?
5. **Post-merge targets.** Out of the queue in v1 (recommended: they hold the
   repository and have their own cap), or admitted too because a deploy is
   also heavy?
6. **Non-gate participation.** Is a `host-slot` wrapper for recipes and the
   cascade wanted, and when? The probe already waits for their load.
7. **Load-induced retry.** Annotate only (recommended), or retry once
   automatically when the gate failed while the host was over threshold?
8. **Section name and home.** `[host-queue]` in trusted layers only, as
   proposed, or under `[hooks]` beside `disable-merge-queue`? The `host-load`
   tool's default threshold would read the same key.
9. **#430 folded in.** Fold the disk floor into this predicate (recommended:
   one probe, one wait reason, one wake shape), or build #430 as a separate
   refusal first? Note #430 asked for a refusal before the gate with the
   attestation kept; the queue waits instead, and times out into the same
   refusal.
10. **Visibility in `sc list`.** Keep `sc list` unchanged and point at the job
    log and `host-load` (recommended, FDR 0022's choice), or add a wait
    column?
11. **Record type.** An amendment to FDR 0022 (it is the same lock pattern one
    level up) or a new FDR? The author leans to a new FDR with FDR 0022 and
    FDR 0025 as its bases, since the predicate and the ledger are new
    mechanisms.

## 8. Sequence (I, after the operator's answers)

1. A probe package (`internal/0/hostprobe`, or `hostload` extended): load,
   `MemAvailable`, store-filesystem free bytes, injectable roots, tests on a
   fake `/proc` and a fake `statfs`.
2. The ledger (`internal/0/hostqueue`, the `mergelock/queue.go` marker pattern
   at host scope) with the admission predicate as a pure function over
   (ledger view, probe, config, waiter state) and table tests for every
   reason and the hysteresis.
3. The wait in `check.runHookPhase` with heartbeats, the test point, the
   timeout and the attestation release path; a bats lane with a stub gate and
   two concurrent `sc check` runs showing one admitted and one waiting with a
   reason.
4. The `[host-queue]` section, trusted-only, `sc validate` rules, the
   `disable` knob.
5. `host-load`: the `gates` section and the threshold default.
6. Metrics, the result-text sentences, the `sweatfile(5)` and
   `spinclass-internals(7)` passages, the FDR.
7. Weights, when the metrics have run long enough to seed them.

Test-first throughout. None of it is confinement or gate-policy code in the
#449 sense (it only delays a gate; it never changes what the gate runs), but
it changes when every merge on the fleet runs, so the first build should be
gated on an operator go and rolled out with `disable = true` available.

## 9. Sources (R)

- `internal/delta/merge/merge.go:480-640` (`FinishMerge`, the repo lock, the
  landing worktree), `:1392-1396` (post-merge under the lock).
- `internal/0/mergelock/mergelock.go`, `internal/0/mergelock/queue.go`.
- `cmd/spinclass/merge_queue.go` (the stack, start notice, drain wake,
  enqueued-merge text).
- `internal/charlie/check/check.go:308`, `:373`, `:556-563` (`runHookPhase`).
- `internal/0/hostload/hostload.go`, `cmd/spinclass/hostload_cmd.go`.
- `internal/0/sweatfile/sweatfile.go:100-130` (`[hooks]` knobs), the trusted
  readers in `internal/alfa/sweatfileio/trust.go`.
- FDR 0022, FDR 0025, FDR 0035; issues #424 (and the 2026-11-05 scope
  comment), #265, #316, #366, #387 (and the 2026-10-20 comment), #388, #430,
  #131.
