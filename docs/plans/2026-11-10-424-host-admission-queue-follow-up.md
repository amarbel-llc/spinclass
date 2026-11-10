# Host-wide admission for gates and heavy builds: follow-up (#424, 2026-11-10)

**Status:** design note, no code. The operator answered this note's ten
questions on 2026-11-10 (section 10). Building waits for the operator's
approval of the design, and tracking (step 0) is the first thing built
after it.

It extends `2026-11-05-424-host-merge-queue.md`
("the 11-05 note") and does not repeat it. The 11-05 note already lays out
the options and recommends option D: a host ledger of flocked markers, a
probe of host state as the admission predicate, and a settle window, all
admitted at one point, `check.runHookPhase`. Its open questions are still
unanswered (no ruling recorded on #424; R). This note answers the
coordinator's six points of 2026-11-10 against that design. It adds the
operator's three asks of the same day:

- every knob is configurable in sweatfiles;
- heavy builds are tracked, so their history can guide queues, scheduling
  and concurrency later;
- the mechanism may become prior art for queuing heavy tool use in moxy and
  clown.

Labels: **O** observed (a tool result or a run seen this session), **R** read
in code, docs or issues, **I** inferred or proposed, **U** unverified.

## 0. What is new since the 11-05 note

- **O, 2026-11-09:** the coordinator held every worker by chat when flic
  reached 2.4 GB available with 7.5 GB of swap in use. It resumed them at
  10 GB, while a nix GC was still running.
  - **I:** `MemAvailable` alone misses swap pressure. A host can thrash
    before `MemAvailable` falls below a 4 GB floor.
- **O, 2026-11-09:** this session's docs-only merge (`targets=[]`, one
  Markdown file changed) ran the full pre-merge `just`. That includes
  `test-nix`, which failed building a godyn vet derivation of a dependency.
  - **R:** `targets` selects post-merge deploy targets only (the
    merge-this-session tool text).
  - **I:** `targets=[]` therefore says nothing about how heavy a gate is. A
    docs-only diff still changes the source, so it still rebuilds. Point 3
    depends on this.
- **O, stats-me, last 7 days:** the longest successful gate per repository
  (`stats.timers.spinclass.merge.<repo>.pre_merge_hook.ok.upper`, max over
  7 d) was:

  | repo | longest gate |
  |---|---|
  | cutting-garden | 23.4 min |
  | piggy | 20 min |
  | madder | 19 min |
  | spinclass | 17.6 min |
  | circus | 13.4 min |
  | moxy | 12.3 min |
  | tommy | 12 min |
  | posh | 10.3 min |
  | smith | 10.3 min |
  | trapeze | 8.5 min |
  | purse-first | 7.3 min |
  | clown | 3.3 min |

  - The sum of the per-repo `queue_depth` gauges peaked at 4.
  - **R:** durations are all the history records. No CPU or memory figure
    exists per gate (11-05 §1, confirmed by grepping the statsd call sites).
- **R:** every pre-merge hook run with a scope id runs inside a transient
  systemd user scope. The id is the async job id, or a sync gate's local
  scope id (`hookrun.runHookInDirEnv`). The command line is
  `systemd-run --user --scope --unit=… --property=KillMode=control-group`
  (`clown.ScopeArgv` → ringmaster `jobwake`).
  - **I:** this gives each gate its own cgroup already. That is the hook for
    tracking (section 2).
- **U:** whether flic exposes `/proc/pressure/*` (PSI). This session's policy
  broker would not let it read `/proc/pressure/memory` (it needs a human, and
  this session runs dontAsk).

## 1. Point 1: one host queue, or an admission check before the hook

The answer is both, in one mechanism: the 11-05 option D. A check alone, with
no reservation, admits a herd: every waiter probes at the same instant and
sees "ok" (11-05 §4C). A queue alone, with no probe, cannot see the load
outside spinclass: the circus cascade, `deploy-relock-input`, a terminal
build. The ledger provides the reservation and the probe provides that
outside view.

Changes proposed to the 11-05 predicate (I):

- **Add a memory-pressure term beside `MemAvailable`.** Prefer PSI
  (`/proc/pressure/memory`, `some avg10`) where it exists (U on flic). Fall
  back to a swap-in-use ceiling read from `/proc/meminfo`. PSI also measures
  CPU contention more directly than `load1`, which counts tasks blocked on
  I/O.
- **Add an operator hold.** `sc queue-hold [reason]` /
  `sc queue-release` writes or removes a hold file in the ledger
  directory. While it exists, nothing new is admitted and running gates
  continue. This replaces the coordinator's chat HOLD broadcast with one
  command, and the reason it gives becomes every waiter's wait reason. Who
  may run it was ruled in Q4 (section 10).
- **Thresholds.** Who sets them and where is section 4. The defaults stay
  the practised lines: hold below 4 GB, resume above 6 GB. One more line
  holds at 6 GB with nothing running and per-CPU load over 1: the
  coordinator's gate rule of 2026-11-09 (O, chat).

## 2. Tracking heavy builds, and using it later (operator ask)

**What to record per gate (I).** Each gate already runs in its own scope
cgroup (R, section 0). Just before the gate ends, read that cgroup's
accounting:

- `memory.peak`;
- `cpu.stat` `usage_usec`;
- `io.stat` bytes, where available.

The wrapper that runs inside the scope can read them, as the
`scopeStartMarker` wrapper already does for the start mark. It appends them
to the marker file after the hook exits and before the scope empties. Also
record the host side:

- the minimum `MemAvailable` and maximum per-CPU load, sampled at the
  existing 30 s heartbeat;
- the wait reasons and the wait time;
- the repository, gate kind (merge, check, guarded, host-slot), outcome and
  wall time.

**Where nix builds land (I, U).** nix-daemon forks builds in its own cgroup,
not the caller's (#387 comment, reasoned, not tested). A gate's scope
therefore undercounts its real cost: evaluation and `go test` are in the
scope, the derivations built for it are not. Two cheap mitigations:

- read the delta of `nix-daemon.service`'s cgroup counters across the gate.
  This is attributable only while one gate runs at a time, which the queue
  itself makes the common case;
- later, check whether nix's per-build cgroups (`use-cgroups`, experimental)
  can be attributed to a client (U: not checked).

**Where to put it (I).**

- statsd, through the existing `[metrics]` client: timers and gauges
  `gate.<repo>.<kind>.{cpu_seconds,mem_peak_bytes,daemon_cpu_seconds,wall}`
  and `host.queue_wait.<reason>`. stats-me already holds the duration series
  to compare against.
- One NDJSON line per gate appended to
  `$XDG_STATE_HOME/spinclass/host-queue/history.ndjson`. This is the
  queue's own input and does not depend on statsd being enabled. Size it with
  a rotation rule.

**How it guides scheduling later (I), in order of increasing ambition:**

1. **Report.** `host-load` and `sc queue-status` show each running gate
   with its repository's typical cost (p50 and p90 of `mem_peak`, wall time).
   A waiter's message can then say "behind cutting-garden/x, typically
   20 min".
2. **Weight.** Replace the count cap (`max-gates`) with a budget. A gate's
   weight is its repository's p90 memory peak, the queue admits while the
   live weights plus the new one fit the budget, and repositories with no
   history get a default. This is 11-05 open question 3 answered from data
   rather than declarations, which also closes the under-declaring loophole
   (11-05 §4B).
3. **Order.** Expected cost becomes a scheduling input: shortest-expected
   first, with aging (section 3).
4. **Detect.** Flag a gate whose cost is far above its repository's history.
   A regression in build cost is itself a finding worth a line in the merge
   result.

Recommendation: ship tracking (step 1) before admission. It is low-risk,
changes no merge's timing, and gives the operator the numbers that the 11-05
defaults are guessing at.

## 3. Point 3: fairness, ordering, and short gates

- **Across repositories (I):** at each admission, pick the oldest waiter
  from the repository admitted least recently (round-robin by repository,
  FIFO within a repository). A burst from one repository cannot then starve
  the others. Within a repository the per-repo lock already serializes merges
  (R, FDR 0022). Concurrent checks are the exception: they take no repo lock
  (R, 11-05 §1).
- **Short gates:** a `targets=[]` merge is not a short gate (O, section 0).
  Two real signals exist:
  - **(a) Expected cost from history** (section 2): a repository whose p90
    gate is 3 minutes (clown, O) is short, whatever the diff. Ordering by
    expected cost with aging (a waiter's priority rises with time waited, so
    nothing waits past `max-wait` for priority reasons) gives short gates
    their lane without a declaration.
  - **(b) A gate that is actually skipped.** The real fix for docs-only
    merges is not a priority lane; it is making their gate light.
    - FDR 0031's exemption predicates are the existing route to "this diff
      needs no full gate" (R: FDR 0031 is proposed and pluggable). Whether a
      docs-only diff may skip `test-nix` is a gate-policy decision for the
      operator, not this queue's. Ruled in Q6: a separate decision, later, and
  out of this work.
    - An exempted or trivial gate should not take a slot. Proposed: a gate
      whose hook is empty or exempt takes no marker.
- **A reserved short lane:** an optional `short-lane = 1` slot that admits
  only gates whose expected wall time is under `short-lane-max`. It is off by
  default and worth turning on only once history exists.

## 4. Sweatfile configuration (operator ask)

Everything is in sweatfiles. The split follows how the existing trusted keys
work (R: `[health]`, `[confinement]` and `[session-entry].restart` are read
only from the global layer and layers above the repository):

**Host policy, trusted layers only** (the global sweatfile is per host, since
it lives in each host's own config dir; R). A repository must not relax these.
`[host-queue]` as in 11-05 §5, plus:

    [host-queue]
    disable = false
    max-gates = 1               # the count cap; superseded by budget once weights exist
    memory-floor = "4GiB"
    memory-resume = "6GiB"
    pressure-memory-avg10 = 10.0  # PSI, used where /proc/pressure exists (ruled Q2)
    swap-ceiling = "4GiB"         # the fallback where it does not
    disk-floor = "30GiB"        # #430
    load-threshold = 1.0        # per-CPU load1; host-load's default reads this key
    settle = "60s"
    poll = "10s"
    max-wait = "60m"
    idle-grace = "10m"          # section 7, "load that is not ours" (ruled Q5)
    budget = ""                 # e.g. "12GiB" once weighted admission is on
    short-lane = 0
    short-lane-max = "5m"
    participants = ["merge", "check"]   # add "host-slot" to admit wrapped recipes
    record-costs = true         # section 2; independent of admission

**Repository hints, read from the repository but only able to make things
stricter:**

    [hooks]
    gate-weight = "8GiB"        # used only when larger than the history-derived weight
    gate-class = "heavy"        # informational; never used to skip the queue

**Resolution:** a repository value can raise its own weight but never lower
it below history or the default; anything else in the repository layer is
ignored with a `sc validate` warning. That is the same "narrowing only from
untrusted layers" stance FDR 0035 takes for confinement (R).

## 5. Point 2: non-merge heavy builds (the cascade, `deploy-relock-input`)

In scope as participants, opt-in per recipe, in a second step. They need no
change in v1, because the probe already waits for their load (11-05 §5).

- **I:** `sc host-slot [--kind NAME] [--weight W] -- <command>` takes a
  marker exactly as a gate does. It waits with the same reasons, runs the
  command in its own systemd scope (so it is tracked as in section 2), and
  releases the marker on exit.
  - A justfile recipe opts in by prefixing its heavy line, e.g.
    `sc host-slot --kind cascade -- nix build …`.
  - circus's `circus update` cascade and `deploy-relock-input` would wrap
    their gate steps the same way. That is a circus change, for circus's
    session to decide; out of scope here.
- **I:** whether a wrapped command is admitted is controlled by
  `participants` (section 4). Until `host-slot` is listed, the wrapper only
  records costs and runs at once.
- **R:** the cascade runs from a terminal, outside spinclass sessions (11-05
  §2). An unwrapped cascade stays visible to the probe only. That is enough
  to stop gates piling on top of it, but not to stop it starting on top of
  gates.

## 6. Point 4: telling a waiter it is queued, and waking it on admit

No polling, and no new waking traffic in the common case:

- **The merge and check tool results** (async, and the sync form when it
  detaches): one sentence saying the gate waits for a host slot and that the
  job log names the reason. This is 11-05 §5 and stands. **R:** a sync
  `merge-this-session` detaches into a background job after 4 minutes, so a
  long admission wait never holds a client request.
- **The job journal:** heartbeats every 30 s to the job log, as FDR 0022
  does, plus one journal-only progress record on admission ("admitted after
  4m10s, waited for memory"). Readable with `job_status` and `job_read`, and
  never waking (R: ringmaster's progress records never wake).
- **The completion wake is unchanged.** An agent that ended its turn is woken
  once, when the merge lands or fails, which is today's contract (R,
  FDR 0010).
- **One waking notice, only on a long wait (I):** if a waiter passes half of
  `max-wait`, or on timeout, a single wake carries the reason. An unattended
  worker then learns its merge is stuck behind a hold before it times out.
- **For the coordinator and the operator:** `host-load` gains the `gates`
  section (11-05 §5), plus `sc queue-status`, both read from the ledger.
  - **R:** spinclass does not send chat; chat belongs to clown and troupe
    (FDR 0017; the grant-session-handle tool text says so). So the queue
    never DMs the coordinator.
  - **I:** if the coordinator wants pushes, its own watcher can read
    `sc queue-status`.

## 7. Point 5: failure modes

| Failure | What happens | Label |
|---|---|---|
| Gate process or serve crashes while holding a slot | The marker's flock is released on process death; the next reader reaps the marker (the `mergelock/queue.go` pattern); the scope stop reaps the gate's children | R (pattern), I (applied here) |
| Crash while holding the short ledger lock | flock releases on death; the admission decision restarts | R, I |
| Gate alive but wedged | `[hooks].inactivity-timeout` kills the hook, the marker goes with it; `session-health` reports busy-too-long; `sc queue-status` shows holder age. Proposed `max-hold` (trusted): past it, the holder is reported as overdue in every waiter's reason, but it is **not** evicted, since evicting a live nix build only adds load | R (timeout), I (rest) |
| A long, healthy holder (23 min cutting-garden gate) | Waiters wait; reasons name it with its typical cost (section 2) | O (duration), I |
| Load that is not ours (cascade, leak) never clears | `max-wait` times out with the reason, and the attestation is released, not consumed (11-05 Q4). Proposed `idle-grace`: if no spinclass marker is held and the probe has failed continuously for `idle-grace`, admit one gate with a warning, since waiting cannot help. Off when a hold file exists | I |
| Session closed while its merge is queued | U: whether `sc close` cancels the session's running job before teardown, or kills the session's `spinclass serve`. Grep of `internal/delta/close` finds no job cancel (R, weak). If serve dies, the marker frees itself (R pattern). If serve survives with a waiting job, the gate could later run for a removed session; proposed: close cancels the session's queued or running job first, and the waiter re-checks that its session still exists at admission | U, I |
| Herd after a hold is released | The settle window admits one, waits `settle`, re-probes (11-05 §5) | I |
| Reboot | Markers are dead and reaped on read; a hold file survives only if the ledger is in persistent state. Proposed: the ledger under `$XDG_RUNTIME_DIR` (tmpfs, cleared at boot), history and holds under `$XDG_STATE_HOME` | I |
| Ledger writable by a confined worker | A worker could unlink markers (over-admission) or plant them (denial). See section 8 | I |

## 8. Point 6: confined and unattended sessions

- **Gates need no new permission (R, I).**
  - **R:** a gate's wait happens inside the merge or check job that
    `spinclass serve` runs, not in an agent tool call. That holds for a
    confined session too: its guarded gate is run by serve in the build
    worktree (11-05 §1, #449).
  - **I:** a dontAsk or confined agent calls the same merge tool it calls
    today and is woken the same way. Nothing new prompts.
- **`host-load` (R):** the MCP tool refuses a confined caller
  (`cmd/spinclass/hostload_cmd.go`, 11-05 §1). The new `gates` section
  inherits that. A confined worker reads its wait reason from its own job
  log instead, which is enough.
- **The ledger must be out of a confined unit's reach (I).** Only serve and
  operator-run CLI commands write it. FDR 0035's unit spec should list the
  ledger directory as neither readable nor writable inside a unit. A
  `sc host-slot` inside a confined unit therefore cannot take a slot. Its
  heavy work is bounded by #387's unit budgets instead (still unbuilt, R),
  which are the right tool for confined workers anyway.
- **An MCP tool that leases a slot for a confined worker's recipe:** not
  proposed. A lease that spans tool calls is a resource a worker could hold
  forever. Ruled in Q7 (section 10): not now; recorded as the v2 direction.
- **`sc queue-hold`/`queue-release` are always-ask for an agent caller
  (ruled in Q4; the end state is a blessed profile right).** They change
  every session's timing, which puts them in the same class as
  `close-child-session force` (R: `perms.AlwaysAsk`). The coordinator's
  session may call them, always-ask.

## 9. Prior art for moxy and clown (operator note)

Moxy and clown are expected to grow serialization for heavy tool use (the
operator, 2026-11-10). If they build their own queues, three queues on one
host each admit "one heavy thing" and together oversubscribe it. Since there
is one host budget, there should be one ledger.

- **I:** make the ledger a documented on-disk contract rather than a
  spinclass internal:
  - directory layout;
  - marker contents, as JSON: holder, kind, weight, admit time, pid;
  - the ledger-lock and settle rules;
  - the hold file;
  - the history line format.

  Any process that follows it, in any language, shares the host's slots: a
  spinclass gate, a `sc host-slot` recipe, or a moxy tool marked heavy.
- **I:** the admission predicate stays a policy of whoever reads the ledger,
  configured from one place (the `[host-queue]` section, or a file the
  contract names). Two consumers with different thresholds would otherwise
  argue.
- **I, ownership:** the contract may belong in ringmaster, not spinclass.
  ringmaster is already the shared job platform linked by spinclass and clown
  (R: `jobwake`), and it already supplies the scope argv. That is a question
  for ringmaster's and clown's sessions to answer (per the research-ownership
  rule). Recommended: spinclass writes the contract as an RFC in its own
  repository for v1, then offers it to ringmaster, moxy and clown with a
  pointer, rather than designing their sides here.
- **What carries over as prior art either way (I):**
  - flock markers instead of a daemon;
  - a probe as the predicate with a reservation;
  - the settle window;
  - wait reasons surfaced in results;
  - accounting from the scope cgroup.

## 9b. v2: per-node gate history, fail-fast ordering, dynamic prerequisites (operator, 2026-11-10)

The operator's direction for v2: track which nodes of the justfile pre-merge
tree fail most often. Then either make them dynamic prerequisites for
`nothing-but-the-truth`, or run them automatically first in the pre-merge
gate.

**What exists (R).**

- The gate runs the repository's `[hooks].pre-merge` as one command (`just`
  here). spinclass sees one exit status and one log; it does not know which
  recipe in the tree failed.
- The tree itself is readable: the just-us plugin's `dump_justfile` (the
  equivalent of `just --dump --dump-format json`) lists every recipe with
  its dependencies.
- The default recipe of this repository is `build test`, per
  `eng-design_patterns-justfile(7)`.

**What it would take to know a failing node (I).**

1. **Parse the log.** `just` prints `error: recipe \`X\` failed on line N`
   (O, 2026-11-09: `test-nix` failed on line 139). That is cheap, gives the
   failing leaf only, and depends on just's message format.
2. **spinclass walks the tree.** It reads the dump, runs each node of the
   default recipe's closure in dependency order as its own command, and
   records a per-node outcome and duration, plus cgroup cost as in section 2
   (each node runs in the gate's scope).
   - This turns the gate from one opaque command into a schedule spinclass
     controls. It also needs care with recipes that are not idempotent, or
     that rely on just running their dependencies once.
   - Only for repositories whose pre-merge is a justfile and that opt in.

Recommended: 1 now, alongside section 2's tracking, because it is a parse of
output already captured. Then 2, once 1 shows the history is worth it.

**Uses, once per-node history exists (I).**

- **Fail-fast ordering:** run the node with the highest
  `P(fail) / expected duration` first. A gate that is going to fail fails in
  minutes, not after 20 minutes of passing nodes, and gives its host slot
  back early. That ties this directly to the queue: wasted slot-time is the
  cost being cut.
  - It needs option 2 (spinclass controls the order). With option 1 only,
    the history is advice, not control.
- **Dynamic prerequisites for `nothing-but-the-truth`:** the attestation
  could require evidence that the likeliest-to-fail nodes passed on the
  current diff before a merge is admitted to the queue, such as
  `just debug-go-test` for the touched packages, or a lint run.
  - This moves cheap failures out of the shared queue and into the session's
    own time.
  - It changes what the attestation gate demands (FDR 0007, FDR 0031), so it
    is gate policy for the operator. The list of nodes would come from
    trusted configuration seeded by history, never from the repository
    alone, so a repository cannot drop a node it fails on.
- **Pre-checks before queueing:** run the top-k likely-failing cheap nodes
  in the session's build worktree *before* taking a host slot. Only the
  survivors queue for the full gate. This is a mechanical variant of the
  attestation prerequisite, with no agent step.
- **Flake detection:** a node that fails and passes on retry at the same sha
  is a flake candidate. It is reported, and never silently retried; this
  builds on the 11-05 Q7 "annotate, do not retry".

**Configuration (I).** It stays in sweatfiles:

    [hooks]
    gate-history = "log-parse"   # off | log-parse | tree-walk (trusted-only for tree-walk)
    fail-fast = false            # tree-walk only: reorder by failure likelihood
    precheck-top = 0             # run this many likely-failing cheap nodes before queueing

A trusted `[attestation]` key would list dynamic prerequisites, derived from
history by a command the operator runs, not recomputed silently at merge
time. **Superseded by Q10 (section 10):** the prerequisites are computed at
merge time, with no operator-run refresh command. The target is for just-us
to emit per-recipe stats-me calls, so the log parse becomes a stopgap.

**Relation to moxy and clown (I).** The same per-node history is what a
heavy-tool queue in moxy or clown would want for its own scheduling: one
history format, keyed by repository, recipe and kind. It belongs in the
ledger contract of section 9.

## 10. Operator rulings on this note's questions (2026-11-10)

Asked by circus/eager-holly/clarabell with AskUserQuestion and relayed
verbatim (chat-a8342673). The 11-05 note's own §7 questions are not covered
by this relay. They stay open unless a ruling below settles one: Q9 here
settles 11-05 Q11 (the record type).

- **Q1. Tracking first: yes.** Tracking ships as its own merge, before any
  admission. It is the first thing built after the operator approves the
  design, not before.
- **Q2. Memory term:** PSI where available, with a swap-ceiling fallback.
- **Q3. Ledger location:** the live ledger under `$XDG_RUNTIME_DIR`; history
  and holds under `$XDG_STATE_HOME`.
- **Q4. Hold:** `sc queue-hold` is callable by the coordinator's session too,
  as an always-ask tool.
  - **End state (recorded, not built):** the right to hold is a blessed
    profile right (FDR 0034, the operator's "ajour / profile privilege"), not
    a hard-coded list of callers.
- **Q5. `idle-grace`:** wanted, default 10 minutes.
- **Q6. Lighter gate for docs-only diffs (FDR 0031 exemption):** a separate
  decision, later. It is kept out of this work.
- **Q7. Confined workers' heavy recipes:** #387 unit budgets only for now.
  - **v2 direction (recorded):** a dynamic slot-leasing tool will likely be
    needed eventually.
- **Q8. Ledger contract:** a spinclass RFC first, offered to ringmaster, moxy
  and clown.
- **Q9. Records:** a new FDR for the feature, plus an RFC for the on-disk
  ledger contract.
- **Q10. v2 per-node history:**
  - Start with failing-node log parsing, alongside the tracking step.
  - **Target (recorded):** just-us emits clean stats-me (statsd) calls per
    recipe, so spinclass need not parse logs. That is a request to just-us,
    to be raised with just-us's session or filed through the coordinator;
    not done from here.
  - The tree walk (spinclass running each recipe node) is acceptable in
    principle.
  - Dynamic `nothing-but-the-truth` prerequisites are computed at merge
    time, with no operator-run refresh command. Section 9b's sentence
    proposing an operator-run command is superseded.
- **Naming (separate operator instruction):** the commands are `sc queue-*`
  (`sc queue-status`, `sc queue-hold`, `sc queue-release`), not
  `sc host-queue …`. The note is renamed throughout. The sweatfile section
  stays `[host-queue]`, trusted layers only (ruled with 11-05 Q8, §10b).

## 10b. Operator rulings on the 11-05 note's §7 questions (2026-11-10)

Relayed by circus/eager-holly/clarabell from AskUserQuestion
(chat-e345c8ad).

- **11-05 Q1, scope:** merges **and** checks, with no operator escape (no
  `--no-host-queue`).
- **11-05 Q2, defaults:** `max-gates = 1`; memory holds below 4 GB and
  resumes above 6 GB.
  - Not ruled: `load1` versus `load5`, and the 60 s settle. The draft keeps
    `load1` and 60 s.
- **11-05 Q3, weights:** a count cap first; weights come from history once
  tracking data exists (consistent with Q1 here).
- **11-05 Q4, timeout:** fail with the reason and keep the attestation.
- **11-05 Q5, post-merge targets:** out of the queue in v1.
  - **v2 direction (recorded):** circus needs them in the queue, because its
    post-merge targets are heavy evaluations. That is specified per
    repository in sweatfiles.
- **11-05 Q6, `host-slot` wrapper:** later (v2).
- **11-05 Q7, load-induced failure:** annotate only to start.
  - **v2 direction (recorded):** retry logic.
  - **Required:** the queue emits stats-me (statsd) calls for visibility, as
    well as having the `sc queue-*` tools.
- **11-05 Q8, section name:** `[host-queue]`, trusted layers only. The
  commands stay `sc queue-*`.
- **11-05 Q9, #430:** fold the disk floor in: one probe, one wait reason, one
  wake shape.
- **11-05 Q10, `sc list`:** unchanged; point at the job log and `host-load`.
- **11-05 Q11, record type:** a new FDR, plus the ledger RFC (Q9 here).
- **just-us per-recipe stats-me emission (Q10 here):** the coordinator files
  it on linenisgreat/just-us through the issue-filer; no just-us session is
  contacted from here.
- **Design approval:** not given yet. No code until the operator approves the
  design.

## 11. Revised sequence (I)

0. Tracking only, as its own merge (ruled Q1): per-gate cgroup accounting
   through the scope wrapper, nix-daemon deltas, `history.ndjson`, statsd
   series, `record-costs`, and the failing-node log parse (ruled Q10). No
   timing change. It is built only after the operator approves the design.
1. to 6. As 11-05 §8 (probe, ledger, wait in `runHookPhase`, `[host-queue]`,
   `host-load` gates, metrics and docs), with the PSI or swap term, the hold
   file and repository-by-repository ordering added.
7. `sc host-slot` and `participants`.
8. Weighted budget and expected-cost ordering, from step 0's history.
9. The ledger RFC, offered across repositories.
10. v2 (section 9b): failing-node log parse (it can ride with step 0), then
    pre-checks before queueing, then the tree walk with fail-fast ordering,
    then dynamic attestation prerequisites. Each is its own ruling.

Every step is test-first, and every step stops for the operator's go before
building.

## 12. Sources

- The 11-05 note and its sources (R).
- `internal/bravo/hookrun/hookrun.go` (scope wrapper, `scopeStartMarker`),
  `internal/0/clown/clown.go` (`ScopeArgv`, `ScopeStop`) (R).
- `internal/delta/merge/merge.go:602-653` (the repo queue, heartbeats,
  `queue_depth`) and `internal/delta/merge/metrics.go` (R).
- `internal/0/mergelock/queue.go` (the marker pattern) (R).
- stats-me queries of 2026-11-10 (O): gate `ok.upper` max over 7 d per
  repository; summed `queue_depth` max over 7 d.
- Coordinator chats of 2026-11-09 and 2026-11-10 (O); issues #424, #387,
  #388, #366 and #430 (R).
