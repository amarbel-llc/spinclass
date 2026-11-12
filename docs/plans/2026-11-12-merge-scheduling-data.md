# Merge scheduling: what the 11-11/12 night's data says (#424 design input)

Status: design input, 2026-11-12. Docs only. Every code change proposed here
is a numbered question (§6) for the coordinator and operator, not a decision.

Companion to the #424 follow-up
(`docs/plans/2026-11-10-424-host-admission-queue-follow-up.md`, "the
follow-up" below) and FDR 0038. The coordinator (circus/eager-holly)
collected the overnight data and asked for: real timer percentiles, a read of
gate duration and failure causes, what a scheduler should key on, what the
metrics are missing, and the smallest metric additions.

## 1. Sources and their limits

- **stats-me (VictoriaMetrics on krone)**, queried 2026-11-12 ~09:30Z over a
  24 h window (`sum_over_time`/`max_over_time` of the statsd timer
  `.count`/`.sum`/`.upper` fields). Two families matter:
  - `spinclass.merge.<repo>.<phase>.<outcome>`: per-phase merge timers
    (`pre_merge_hook`, `queue_wait`, `total`, ...), emitted by every spinclass
    build since FDR 0022. This is the complete record of gates.
  - `spinclass.gate.<repo>.merge.*`: the #424 step-0 cost series (`wall.<outcome>`,
    `cpu_seconds`, `daemon_cpu_seconds`, gauge `mem_peak_bytes`, counter
    `failed_recipe.<recipe>`), emitted only by builds that contain b41d1ce.
- **No series carries a host.** flic and nikulin gates are mixed in every
  number below. (clown's own `clown.tool_result.<host>.*` series do carry the
  host; spinclass's do not.)
- statsd percentiles are per flush interval, so a true 24 h p90 cannot be
  rebuilt from the stored `upper_90` fields. I report the mean
  (`Σsum/Σcount`) and the max (`max(upper)`) instead. With n ≤ 27 per repo,
  the max is the more useful tail number anyway.
- **The host-queue series (`host.queue_wait.*`, `host.wait_reason.*`,
  `host.gates_running`, `host.queue_grace`) have no data and will not get
  any yet.** They are emitted only when a gate goes through admission, and
  admission is opt-in (`[host-queue] enable`, off by operator ruling). The
  09:08Z deploy of fcc569f shipped the code, but `stats-me series` finds no
  `spinclass.host.*` name. Data starts only when an operator enables the
  queue on a host.
- `$XDG_STATE_HOME/spinclass/host-queue/history.ndjson` (the step-0 per-gate
  history, with session and timestamp per line) would answer several
  questions below. I could not read it: the policy broker denies that path to
  this session. An operator can read it.

## 2. Gate duration by repo (24 h, all hosts)

`pre_merge_hook` is the gate (the repo's `just`). n counts gates. Times are
seconds.

| repo | ok n | ok mean | ok max | failed n (mean) | notes |
|---|---:|---:|---:|---|---|
| troupe | 26 | 31 | 77 | 1 (12) | the busiest repo, and cheap |
| moxy | 15 | 299 | 467 | 4 (247); 1 cancelled | the most failures |
| clown | 12 | 97 | 148 | 0 | |
| circus | 11 | 512 | 771 | 0 | |
| spinclass | 6 | 636 | 687 | 3 (291) | the 3 failures are my #424 attempts |
| cutting-garden | 5 | 275 | 739 | 1 (536) | |
| juggler | 4 | 50 | 88 | 0 | |
| papi | 3 | 107 | 108 | 0 | |
| ringmaster | 3 | 106 | 111 | 0 | |
| roustabout | 3 | 45 | 63 | 0 | |
| smith | 3 | 499 | 530 | 0 | |
| madder | 1 | 1114 | 1114 | 0 | the single longest gate |
| conformist | 1 | 344 | 344 | 0 | |
| maneater | 1 | 312 | 312 | 0 | |
| igloo | 1 | 271 | 271 | 0 | |
| posh | 1 | 234 | 234 | 0 | |
| pigsty | 1 | 177 | 177 | 0 | |
| hyphence | 1 | 110 | 110 | 0 | |
| galapagos | 1 | 100 | 100 | 0 | |

- **Gates fall into three duration classes.**
  - Under 2 minutes: troupe, juggler, roustabout, clown, papi, ringmaster,
    hyphence, galapagos.
  - 3 to 9 minutes: moxy, cutting-garden, circus, smith, conformist, maneater,
    igloo, posh, pigsty.
  - 10 minutes or more: spinclass, madder.
  
  The spread within one repo is small next to the spread between repos, so a
  per-repo running mean is a usable estimate. That supports the follow-up's
  §3 short-gate ordering.
- **Total demand** was 6.9 gate-hours over 24 h. Rolling-hour demand (mean
  concurrent gates, booked at completion) peaked at about 1.5 between
  22:00Z and 23:36Z, the cascade and #424 window. It sat at 0.3 to 0.6 the
  rest of the active day. With flic held to one gate at a time, those peak
  hours could not all run on flic. nikulin absorbed the rest, at the cost
  in §4.
- **Per-repo merge queues never contended.** `queue_wait` had mean 0 and max
  0 in every repo, and `queue_depth` stayed 0. All contention was between
  repos on one host, which no existing series sees. That is the gap #424's
  host queue fills.
- Merge `total` minus `pre_merge_hook` (fetch, rebase, repair, land, push)
  is 2 to 25 s for most repos. The exceptions are roustabout (about 60 s
  mean), circus (about 63 s, which may include its post-merge deploy
  targets) and cutting-garden (about 81 s); those are worth a separate look.
  Otherwise the gate is nearly the whole cost.

### Step-0 cost series (subset of gates; see §5 for the coverage gap)

| repo | gates | wall mean | gate cpu-s mean | nix-daemon cpu-s mean | mem_peak max |
|---|---:|---:|---:|---:|---:|
| circus | 9 | 534 | 329 | 283 | 9.86 GB |
| smith | 3 | 499 | 626 | 2134 | 4.63 GB |
| cutting-garden | 5 | 287 | 66 | 693 | 1.88 GB |
| galapagos | 1 | 100 | 271 | 20 | 1.47 GB |
| spinclass | 1 | 687 | 42 | 1471 | 1.34 GB |
| moxy | 19 | 299 | 36 | 726 | 1.25 GB |
| pigsty | 1 | 177 | 117 | 433 | 1.24 GB |
| maneater | 1 | 312 | 529 | 270 | 1.12 GB |
| madder | 1 | 1114 | 245 | 1647 | 1.03 GB |
| papi | 3 | 107 | 45 | 166 | 0.96 GB |
| clown | 10 | 92 | 29 | 198 | 0.89 GB |
| troupe | 27 | 31 | 10 | 99 | 0.77 GB |
| others | 1 to 4 | | | | 0.47 to 0.73 GB |

- **For most repos, nix-daemon CPU dwarfs the gate scope's own CPU.** For
  smith, moxy, cutting-garden, spinclass and madder it is 3 to 35 times
  larger. The follow-up §2's warning holds: a scope-only cost undercounts by
  an order of magnitude. The daemon delta is attributable only while one
  gate runs per host, and §4 shows two at once was common on nikulin, so
  read these numbers as upper bounds there.
- **`mem_peak_bytes` is the scope cgroup's `memory.peak`**, which counts page
  cache as well as anonymous memory. circus's 9.86 GB is more than flic ever
  had available overnight (4.5 to 6.4 GB), so it either ran on nikulin or is
  mostly reclaimable cache. With no host label I cannot tell which. Until
  anon and file are split (§6 Q4), mem_peak is not a "needs this much free
  memory" number. Do not set a per-repo memory floor from it.

## 3. Failures: real bugs vs infrastructure

The counters give the repo and, sometimes, the recipe. The cause comes from
chat and job logs.

| repo / gate | count | lane (source) | class |
|---|---:|---|---|
| spinclass #424 attempt 1 | 1 | lint-golangci (job log) | real: a lint finding in a test helper |
| spinclass #424 attempt 2 | 1 | test-nix (job log) | real: a race in the waiter marker, fixed in 2add2ec |
| spinclass #424 attempt 3 | 1 | bats-default via test-nix (job log) | real: a test bug (forced `--format tap`) |
| moxy | 4 | test-flake-check (counter) | unknown from the metrics; needs the moxy logs |
| cutting-garden | 1 | build-nix-check (counter) | real: a lint finding (coordinator) |
| troupe | 1 | check (counter), 12 s | probably fast and real; unconfirmed |
| nikulin gates | 2 | conformist eval-cache lock timeout (coordinator) | infrastructure: load 19 to 25 on 8 CPUs |

- Every failure classified so far is a real code or test bug, except the two
  nikulin lock timeouts. That supports the follow-up §9b fail-fast ordering:
  run lint and short unit lanes before long nix lanes, and stop a gate early.
  My #424 attempts cost about 1, 7 and 7 minutes of gate time (34 s, 419 s,
  419 s). A local lint pass, the touched packages' tests and bats-default
  would have caught all three before the gate. The [auth] gate, which
  followed a full local bats-default run, landed on the first try.
- **The two infrastructure failures are exactly what host admission
  prevents.** A gate admitted at per-CPU load 2.4 to 3.1 starved
  conformist's eval-cache lock. With `[host-queue]` enabled on nikulin at
  `load-threshold = 1`, those gates would have waited instead of failing.
  Each failure cost a full re-gate, roughly 5 to 10 minutes.

## 4. What a scheduler should key on

Measured against the defaults the operator ruled (`DefaultConfig`: max-gates
1, memory-floor 4 GiB, memory-resume 6 GiB, load-threshold 1.0 per CPU):

1. **One gate per host (`max-gates = 1`).** The coordinator enforced this on
   flic by hand all night without a failure from contention there. nikulin,
   which ran several gates at once, had both infrastructure failures.
   Recommendation: keep `max-gates = 1` on both hosts. The data does not
   support more on 8 CPUs.
2. **The memory floor and resume, in GiB.** flic's available memory moved
   between 4.5 and 6.4 GB (4.2 to 6.0 GiB) all night. The coordinator's hand
   rule was "start above 5 GB" (4.66 GiB).
   - **Finding:** the default `memory-resume = 6 GiB` (6.44 GB) is above
     everything flic reached overnight. Once a gate is held for memory on
     flic, it would very probably not resume, and would wait out `max-wait`
     (60 min) and fail.
   - Recommendation for flic's trusted sweatfile when the queue is enabled:
     `memory-floor = "4GiB"` and `memory-resume = "4.75GiB"`, close to the
     hand rule that worked.
   - The 6 GiB default may still suit a host with more RAM. Q1 asks whether
     to lower the shipped default.
3. **Per-CPU load, as a guard against other work rather than a gate
   predicate.**
   - At my five go's last night, per-CPU load1 read 0.54, 0.61, 1.50, 1.34
     and 0.53. Between gates it reached 1.76.
   - For the two over 1.0, the coordinator went ahead, because about one core
     was the nix-cache uploader (`nix copy`). Both gates ran clean of
     infrastructure trouble.
   - At `load-threshold = 1.0`, flic would have held those two for load
     alone.
   - The uploader is a constant background load of about 1/8 per CPU, and
     interactive agents add bursts.
   - Recommendation for flic: `load-threshold = 1.5` (the coordinator's
     de facto bar).
   - nikulin's failures came at 2.4 to 3.1 per CPU, so 1.5 still protects
     there.
   - Q2 asks whether the default should move. It also asks whether the
     unruled 11-05 Q2 (load1 vs load5) should now be ruled load5: the
     22:00 to 23:36 load1 swings were large and short.
4. **Per-host limits for nikulin.** Same policy, separate ledger (the ledger
   is per host by construction, under `$XDG_RUNTIME_DIR`). Nothing
   cross-host is needed for the failures seen. The coordinator's cascade
   ordering across hosts is a separate, higher-level scheduler.
5. **Order by expected duration once gates queue.** §2's per-repo means are
   stable enough (troupe 31 s against circus 512 s) for shortest-expected-first
   to cut mean wait. The follow-up §3 already proposes this, and the data
   supports it.

## 5. What is missing from the metrics

1. **A host dimension.** It matters most. Without it, none of §2 to §4 can
   be split between flic and nikulin.
2. **Cost coverage depends on each session's serve build, and fails
   silently.** Gate cost is recorded by the serve process that runs the
   merge.
   - This session's serve is 8a31d68 (`spinclass version` reports it STALE
     against fcc569f on PATH), which predates b41d1ce.
   - My five gates last night (three failed, two landed) are in
     `merge.spinclass.pre_merge_hook`, but none is in `gate.spinclass.*`.
     The one `gate.spinclass` record is at 21:24Z, from another session.
   - Other repos show the same gap: circus 9 of 11 gates, clown 10 of 12.
   - A stale serve emits nothing and says nothing.
3. **Failure classification misses script recipes.**
   - `gatecost.LastFailedRecipe` matches only just's "failed on line N"
     form.
   - just reports a failed shebang or script recipe as "error: recipe `X`
     failed with exit code N", with no line. That form is my attempt-1 lint
     failure (`lint-golangci`).
   - `gatecost_test.go` pins that form as a "lookalike" that must not
     match, so this is a design choice that dropped real failures, not a
     regression.
4. **No failure cause class.** A recipe name says where a gate failed, not
   why. "lint-golangci found an issue" and "conformist's eval-cache lock
   timed out" can come from the same recipe. A coarse class (code, test,
   timeout, oom, lock, network, cancelled) would make §3 a query instead of
   a chat archaeology exercise.
5. **No host state at gate start.** Available memory, per-CPU load and swap
   when the gate began are exactly the inputs the admission predicate reads.
   Without them, the metrics cannot show whether a failure followed a
   bad-host start.
6. **mem_peak conflates anonymous memory and page cache** (§2).

## 6. Smallest metric additions (each a numbered question; none implemented)

- **Q1: memory-resume default.** Lower the shipped `memory-resume` default
  from 6 GiB to 5 GiB, or keep it and set flic's own trusted value
  (`4.75GiB`) when the queue is enabled? I recommend the per-host value. The
  default ships to every host and should not chase flic.
- **Q2: load threshold and window.** Set flic's `load-threshold = 1.5` when
  enabling, and rule the 11-05 Q2 as load5? I recommend 1.5 per host and
  load5. load1 swung widely during the cascade.
- **Q3: host segment.** Add `<host>` to spinclass's statsd names
  (`spinclass.<host>.merge...`, `spinclass.<host>.gate...`), the same shape
  clown uses (`clown.tool_result.<host>...`)? It is the smallest change with
  the largest effect: one prefix built from `os.Hostname()` at
  `statsd.Resolve`. It renames every series, so dashboards and these queries
  must change. Alternatively, add it only to the `gate.*` and `host.*`
  families, which are new and have no consumers yet. I recommend that
  narrower form.
- **Q4: gate-start snapshot and memory split.** Emit three gauges per gate
  at admission or start: `gate.<repo>.<kind>.start_mem_available_bytes`,
  `.start_load_per_cpu` and `.start_swap_used_bytes`, all from the
  `hostload.Snapshot` admission already takes, so no new probe is needed.
  Also record `memory.stat` anon (or `memory.peak` minus file) next to
  mem_peak. These are cheap, since the code already reads both cgroup and
  `/proc`.
- **Q5: failure class.** Add a `failed_class.<class>` counter next to
  `failed_recipe`, classified from the captured output by a small fixed
  table (lock timeout, OOM-killed, cancelled, a nix "builder failed" under a
  lint or test recipe counted as code, else unknown), and widen
  `LastFailedRecipe` to also accept just's "failed with exit code N" form?
  The second part is a one-line regex change plus flipping one test case.
- **Q6: stale serve visibility.** When `serve` is stale against the
  `spinclass` on PATH, have the merge result say "gate cost not recorded:
  this session's serve is stale (spinclass#399)", or emit a
  `gate.serve_stale` counter? This makes §5.2's silent gap visible. The
  check already exists (`spinclass version` prints STALE).
- **Q7: operator read of history.ndjson.** Would the operator read flic's
  `~/.local/state/spinclass/host-queue/history.ndjson` (or allow it in the
  `[folio]` stanza for spinclass sessions) so the next note can use
  per-gate session and timestamp data rather than flush-interval
  aggregates?

## 7. Sources

- stats-me queries as of 2026-11-12 ~09:30Z: series names
  `stats.timers.spinclass.merge.*`, `stats.timers.spinclass.gate.*`,
  `stats.gauges.spinclass.gate.*.mem_peak_bytes`,
  `stats.spinclass.gate.*.failed_recipe.*`; export of
  `stats.timers.spinclass.merge.spinclass.pre_merge_hook.failed.count` (my
  three failures at 22:06Z, 22:19Z and 22:31Z) and
  `stats.timers.spinclass.gate.spinclass.merge.wall.ok.count` (one record, at
  21:24Z).
- The coordinator's overnight notes (circus/eager-holly): wall times, memory
  range, the nikulin load and lock timeouts, the cascade halt.
- Code: `internal/charlie/check/gatecost.go` (recording gated on the trusted
  `record-costs`, default on), `internal/0/gatecost/gatecost.go`
  (`justRecipeFailed`), `internal/alfa/hostqueue/predicate.go`
  (`DefaultConfig`), `internal/alfa/hostqueue/config.go`.
- My gate jobs: merge-57127ff9, merge-f5070da5, merge-bf72e3ce (failed);
  merge-8d7596b3, merge-6e59390d (landed).

## Appendix A: a flaky #424 test, and why it was a flake and not a race

This note's own first gate (merge-ad2718df, 2026-11-12 09:43Z) failed in
`charlie/check` `TestHostQueueWaiterIsVisibleWhileWaiting`: "waiting while
waiting = [], want one waiter naming load".

- **The test as it was.** It ran a wait with `max-wait = 200ms` against a
  probe that always refuses. A goroutine slept 60 ms, then read the waiters
  once. It passed only if that single read fell between the waiter's marker
  being written (first refusal) and removed (timeout at 200 ms).
- **The symptom points at timing.** The read saw no marker at all. That is
  what happens when the sampler wakes before the first refusal, or (under a
  loaded sandbox, where a 60 ms sleep can overrun badly) after the 200 ms
  timeout has removed the marker. It is not the symptom of the real race
  fixed in 2add2ec, which showed an "(unreadable marker)" entry, a marker
  seen mid-rewrite.
- **The record.** The test passed in the gate lanes of merge-bf72e3ce and
  merge-8d7596b3, each of which runs it in both the plain and the
  race-detector test lanes. It also passed in a local bats-default build
  (whose checkPhase runs the Go suite) and in two local `debug-go-test`
  runs of the package. It failed once, in merge-ad2718df. No run repeated
  the test in a loop, so this is a pass/fail record, not a measured flake
  rate.
- **The fix (fa76ac5).** It changes no product code. The test now polls
  every 5 ms for up to 4 s until the waiter appears, then cancels the wait
  through its context, and checks the marker is removed after the wait
  ends. Its result no longer depends on how long anything takes. It still
  fails if the waiter is never listed, or is listed without its reason, or
  outlives the wait.
- **What is still covered.** The real race's guard is
  `hostqueue.TestLedgerWaiterReadDuringUpdates`: 2,000 reason updates
  against continuous reaping reads. It failed on the pre-2add2ec code with
  the gate's original symptom. It is unchanged.

## Appendix B: enabling the host queue on flic and nikulin (proposal)

Operator ruling relayed 2026-11-12: propose the change; the circus side is
a separate item routed to a circus worker; nothing is enabled until the
operator deploys it. This appendix is that proposal. It edits nothing.

### Where it lives

The key must be in a **trusted** layer: the global sweatfile or a
parent-directory sweatfile. A repo's own `[host-queue]` is ignored with a
warning (the bats test `host_queue_ignores_a_repo_level_enable` covers this).
On flic and nikulin the global sweatfile, `~/.config/spinclass/sweatfile`,
is generated by home-manager from circus
`infra/hosts/home/spinclass-workstation.nix`. That file already gates
per-host sections on `hostname` (`selfCloseHosts`, `postureWideningHosts`,
`folioHosts`), so the queue follows the same pattern.

**Proposed circus change** (shape only; the circus worker owns the exact
edit):

```nix
  # Host admission queue (spinclass#424, FDR 0038; operator ruling
  # 2026-11-12). Opt-in per host: every merge, check and guarded gate on the
  # host waits for a slot. Read only from this trusted layer, at each gate (no
  # session restart needed). Turn a host off by removing it here, or pause
  # every waiter with an operator hold (see spinclass-sweatfile(5)).
  hostQueueByHost = {
    flic = {
      enable = true;
      max-gates = 1;
      memory-floor = "4GiB";
      memory-resume = "4.75GiB";
      load-threshold = 1.5;
    };
    nikulin = {
      enable = true;
      max-gates = 1;
      memory-floor = "4GiB";
      memory-resume = "6GiB";
      load-threshold = 1.5;
    };
  };

  hostQueue = lib.optionalAttrs (hostQueueByHost ? ${hostname}) {
    host-queue = hostQueueByHost.${hostname};
  };
```

…and merge it with the existing sections:
`confinement // permissions // folio // hostQueue // { ... }`. No other
section produces a `host-queue` key, so the shallow `//` is safe.

That generates, on flic:

```toml
[host-queue]
enable = true
load-threshold = 1.5
max-gates = 1
memory-floor = "4GiB"
memory-resume = "4.75GiB"
```

### What each key does (spinclass-sweatfile(5), [host-queue])

- `enable = true`: admission on. Default off.
- `max-gates = 1`: at most one gate admitted on the host at a time. The
  default, written out so the intent is visible.
- `memory-floor = "4GiB"`: hold a gate while MemAvailable is under 4 GiB
  (4.29 GB). This is the shipped default, written out.
- `memory-resume = "4.75GiB"` (flic): once a waiter is held for memory, it
  resumes only above 4.75 GiB (5.10 GB). The shipped 6 GiB (6.44 GB) is
  above anything flic reached overnight (§4.2), so a memory hold could only
  end by timing out. 4.75 GiB is close to the coordinator's hand rule
  ("start above 5 GB") that worked all night. `ParseSize` accepts decimals;
  `"4864MiB"` is the same value written as an integer.
- `load-threshold = 1.5`: hold while per-CPU load1 is above 1.5, which on 8
  CPUs is a load average of 12. It sits above the nix-cache uploader's
  constant core and the coordinator's de facto overnight bar (§4.3). It is
  below nikulin's failure range (2.4 to 3.1 per CPU).
- Left at their defaults, deliberately: `settle = "60s"` (a just-admitted
  gate shows its load before the next decision), `poll = "10s"`,
  `max-wait = "60m"` (then the gate fails with `host queue timeout` and its
  reason), `idle-grace = "10m"` (after 10 minutes of failing probe terms
  with no gate running, admit anyway; never past a hold or `max-gates`),
  `pressure-memory-avg10 = 10.0`, `swap-ceiling = "4GiB"` (used only where
  PSI is absent), `disk-floor = "30GiB"` and `record-costs = true`.

### nikulin

nikulin ran 8 workers and gates at once overnight, at load averages of 19 to
25 on 8 CPUs (2.4 to 3.1 per CPU). Two gates failed on conformist's
eval-cache lock timeout.

- `max-gates = 1` and `load-threshold = 1.5`: the same as flic. The aim is to
  start a gate only when the host is below about 12 load, which is under
  the range where both lock timeouts happened.
- **The risk.** If nikulin's background load from 8 agent workers alone sits
  above 1.5 per CPU, every gate would hold for load until `idle-grace`
  admits it. That is at most a 10-minute delay per gate when no gate is
  running, not a wedge. It would show as `host.wait_reason.load` and as
  "host queue wait ... (load)" lines in gate output. If that happens often,
  raise nikulin's threshold to 2.0 (still under 2.4) rather than drop the
  queue.
- **Memory.** I do not know nikulin's RAM or its overnight MemAvailable; no
  series reports host memory. I propose the shipped `memory-floor` and
  `memory-resume` (4 and 6 GiB) until someone reads `MemAvailable` on
  nikulin during a busy hour. If its typical availability sits under 6 GiB,
  lower the resume line as for flic.
- **Order.** Enable on flic first and watch one busy evening. nikulin after,
  since its load profile is the one the queue most needs to change.

### Verifying it on the first gate

1. **Before any gate**, with the generated file deployed:
   - `sc validate` should report nothing for `host-queue`. A bad value shows
     as a `host-queue valid` line marked not ok, and a repo-level table as
     a warning.
   - `spinclass host-load` should judge per-CPU load against the trusted
     `load-threshold` (its header reads `threshold 1.5`), and, while gates
     run or wait, print the queue section: running gates, waiters with
     their reasons, and any hold.
   - No session restart is needed: the config is read from the hierarchy at
     each gate (`hostQueueConfig` in `check/hostadmit.go`), not at serve
     start.
2. **On the first gate**:
   - While it runs, `spinclass host-load` lists it as the one running gate,
     with its session and how long it has run.
   - A second gate started during it should wait. Its job log gets
     `host queue: 1 of 1 gate slots taken (<session>); waited ...` lines
     every 30 s, and when it is admitted its output has
     `host queue wait <time> (gates)`. Expect a further `settle` wait of up
     to 60 s after the first gate's admission.
   - An admission that never waited prints nothing; that is expected.
3. **In stats-me**, after the gate: `host.queue_wait.ok` gets a count (the
   series exists only once the queue is on), `host.gates_running` gets a
   value, and a wait adds `host.wait_reason.<reason>`. With no host label
   yet (§6 Q3), flic's and nikulin's values mix once both are on.
4. **If something is wrong**, a gate never wedges on the queue itself. A
   ledger or probe that cannot be read, or an invalid value, makes the gate
   run unqueued with a warning in its output. Look for that warning first.

### Turning it off

- **Pause every waiter on one host, at once, with no deploy.** Write a
  reason into the operator hold file,
  `$XDG_STATE_HOME/spinclass/host-queue/hold` (`~/.local/state/...`).
  Every waiter holds with that reason. Gates already running continue.
  Remove the file to release. A hold stops gates, so it is for "nothing
  new may start", not for "queue off".
- **Turn the queue off on a host.** Remove the host from `hostQueueByHost`
  (or set its `enable = false`) and redeploy home-manager. The next gate
  reads the new global file and runs unqueued, with no restart. Gates
  already waiting keep their config until they are admitted or time out,
  because the config is read once per gate.
- **Emergency, without a deploy.** A parent-directory sweatfile with
  `[host-queue] enable = false` above the repos (for example
  `~/eng/sweatfile`) overrides the global `true`. The nearer trusted layer
  wins per key. Remove it once the deploy that turns the queue off is in.
