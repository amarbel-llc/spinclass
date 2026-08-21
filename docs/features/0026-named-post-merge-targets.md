---
status: experimental
date: 2026-08-20
promotion-criteria: |
  experimental -> testing: circus wired end to end — its krone (and nikulin)
  self-deploy split from one inline `[hooks].post-merge` blob into named
  `[[post-merge]]` targets, each with `command` (the deploy trigger) and
  `verify` (the remote acceptance check, with the circus#132 deadman ack in
  verify's tail) — with (1) a merge whose `targets` selection deploys a subset
  and skips the rest, (2) one observed `verify-failed` verdict distinguished
  from a `command-failed` one in the merge result and the completion wake, and
  (3) a docs-only merge (`targets = []`) that lands and deploys nothing.
  testing -> accepted: ~2 weeks of real multi-target deploys with no case
  where a per-target verdict misreported which stage failed, and the deadman
  enrollment (circus#132) riding on the `verify` legs going green.
---

# Named `post-merge` targets, with a first-class `verify` stage

## Problem Statement

FDR 0023 gave a merge a single `[hooks].post-merge` command that fires once the
merge lands. One opaque command is all-or-nothing: every merge runs the whole
deploy blob, so a docs-only change redeploys every wired host, and adding a host
means editing one ever-growing script. It also collapses two distinct failures
— "the deploy trigger itself failed" and "the deploy ran but the remote never
came healthy" — into one nonzero exit, when the human acting on a failed merge
needs exactly that split ("fix the change" vs "investigate the probe/ack path").

The motivating consumer (circus, #273): circus deploys krone over a
forced-command SSH key and now has nikulin self-deploying too, with more hosts
coming. Its `post-merge` is one inline blob that triggers the deploy and polls
for health. circus#132's deadman-switch enrollment is gated on that health poll
becoming a first-class, individually-reported **verify** leg — a green
`verify` is the signal the deadman machinery keys on.

## Interface

A set of **named targets**, top-level in the sweatfile, each a `command` and an
optional `verify`:

```toml
[[post-merge]]
name    = "krone"
command = "just infra/hosts/trigger-redeploy-krone"
verify  = "kssh krone -- systemctl is-active krone.service && ./bin/deadman-ack krone"

[[post-merge]]
name    = "nikulin"
command = "just infra/hosts/trigger-redeploy-nikulin"
```

- **`verify` runs iff `command` exited zero.** Each target yields one of three
  verdicts, reported individually:

  | verdict | meaning | verify run? |
  |---|---|---|
  | `command-failed` | the deploy trigger itself exited nonzero | no |
  | `verify-failed` | the trigger succeeded but the acceptance check exited nonzero | yes |
  | `ok` | trigger succeeded, and verify (if any) passed | if present |

  A target with no `verify` is opaque-command: `command` zero ⇒ `ok`, nonzero ⇒
  `command-failed`. This is the graceful-degradation path — a target inlines its
  whole chain in `command` until it is split, so repos migrate stanza by stanza
  with no flag-day.

- **Deliberately no third `confirm` stage.** The acceptance/ack invocation is the
  tail of the `verify` script (its failure fails verify), so a third stage buys
  no verdict fidelity (#273 amendment).

- **Merge-time selection** on `merge-this-session` / `merge-this-session-async`
  via a `targets` parameter:

  | `targets` | effect |
  |---|---|
  | absent / null | **all** active targets run (the default) |
  | `["krone"]` | only the named subset runs |
  | `[]` (empty) | **none** — a docs-only merge lands and deploys nothing |

  Selecting a name no target declares **fails the merge before it lands** — the
  one place a post-merge concern can still be fatal, because nothing has shipped
  yet, and a silent skip of the deploy the operator asked for is the worse
  outcome. `sc merge` takes `--post-merge-targets a,b` and `--no-post-merge` for
  the CLI equivalents.

- **Each target's failure flips the reported job outcome without failing the
  merge.** Like FDR 0023's single hook, a target failure is non-fatal — the
  merge already landed, nothing rolls back. Each failing target renders a
  `✗ post-merge <name> …` line (`severity=warn`, verdict, and output in the
  diagnostic); the async completion wake lifts **every** such line into its
  one-line summary (extending spinclass#259), so a multi-target run that
  half-succeeds is actionable at a glance rather than reading as a clean
  landing.

- **Legacy `[hooks].post-merge` string is retained and superseded.** When any
  active `[[post-merge]]` target exists, the single string does **not** run —
  the named targets are the phase. A repo migrates by adding stanzas (the blob
  goes dormant automatically) and deleting the string at leisure. With no named
  targets, the string behaves exactly as FDR 0023 shipped it.

- Everything else from FDR 0023 is unchanged: the phase runs under the per-repo
  landing lock as the merge's last stage; the `SPINCLASS_MERGED_*` env facts;
  the working directory (session worktree, else the main checkout); applies to
  worktree and implicit sessions; never run by `sc check`. `disable-post-merge`
  suppresses the whole phase. `post-merge-timeout` now bounds the **whole phase**
  (all targets and their verifies share one wall-clock deadline), so multi-target
  fan-out can never hold the lock past the cap.

## Design

### Config shape: a top-level `[[post-merge]]` array, not `[[hooks.post-merge]]`

#273 sketched `[[hooks.post-merge]]`. That shape is unreachable without a
flag-day: TOML cannot make the key `hooks.post-merge` both a string (the FDR
0023 form) and an array-of-tables, and the tommy codec generates exactly one
handler per struct field per key — there is no union decode. Retiring the string
in favour of the array would break every repo (circus included) that ships a
`[hooks].post-merge` string the moment spinclass bumps, which #273 explicitly
forbids ("repos migrate stanza by stanza, no flag-day").

So named targets live in a **top-level `[[post-merge]]` array** — the exact
`[[mcps]]` / `[[remotes]]` / `[[start-commands]]` idiom (all of which are
top-level, dedup-by-name arrays; none nest an array-of-tables under a
sub-table). The legacy `[hooks].post-merge` string stays a `[hooks]` scalar. The
two keys never collide, both can be present during a migration, and the
supersede rule (below) makes the transition a no-op to reason about.

This also reads *better* as the "configurable pipeline phases" direction the
operator asked the feature to pave toward (#273 amendment, Ask 2): a
phase-named top-level array that a future `[[pre-merge]]` would mirror, rather
than deploy plumbing buried inside `[hooks]`.

### Substrate independence and the growing contract

The operator's Ask 2 is to shape this as the first instance of a general
configurable-pipeline-phases direction, and to keep spinclass's VCS substrate
(today, git) from leaking deeper into the phase machinery.

- **The target carries no git.** `PostMergeTarget{Name, Command, Verify}` is
  phase-neutral — nothing in it names git, a sha, or a diff. The git-shaped
  facts (`SPINCLASS_MERGED_SHA`, …) are computed by the *phase* and handed to
  the target's command as environment, exactly as FDR 0023 already did. A target
  is "run this command, optionally verify it"; the substrate that produced the
  landing is invisible to it. The recorded nice-to-have — per-target `paths`
  filters — is the one feature that *would* bake a `git diff` assumption into
  selection, which is a second reason (beyond scope) it stays deferred here.
- **One phase wired, the type ready to generalize.** The struct is named
  `PostMergeTarget` because post-merge is its only consumer today (YAGNI — no
  second phase asks for targets yet). But its fields are phase-agnostic, so
  promoting it to a shared `PhaseTarget` reused by a future `[[pre-merge]]`
  targets array is a rename-and-reuse, not a redesign. The config surface is
  additive — a new top-level array — so it composes with #256's schema-version
  indicator without a schema break; coordinate there if the version marker lands.

### Per-target verdicts, and where they can be fatal

The verdict machinery reuses FDR 0023's non-fatal reporting: each target emits
one `crap.TestStream` point, a failure is `NotOk` + `severity=warn` (there is no
warn verdict in the stream), and `runPostMergePhase` still returns nil — the
merge landed. The three-way verdict rides in the diagnostic (`verdict`, `stage`)
so a consumer can machine-read it, and the `post-merge <name> …` label prefix is
preserved so #259's wake-surfacing (matched on `✗ post-merge`) catches every
failed target for free.

Selection validation is the deliberate exception: an unknown target name in
`targets` is checked in `PrepareMerge`, **before** anything lands, and fails the
merge like any other pre-merge gate. Post-landing everything is non-fatal because
nothing can be rolled back; pre-landing a caller typo that would silently drop a
deploy is worth a hard stop.

### Whole-phase timeout

FDR 0023's `post-merge-timeout` bounds one hook. With N targets each running a
command and maybe a verify, a per-invocation cap would let the phase hold the
landing lock for up to N × cap — reintroducing the "a slow hook wedges the repo's
queue" hazard (#246) that the cap exists to bound. So the named-target phase
derives one deadline from `PostMergeTimeoutValue()` and runs every target and
verify under it; when it fires, the in-flight target is killed (`command-failed`
/ `verify-failed`) and the remainder are marked skipped. The legacy
single-string path keeps FDR 0023's per-hook cap unchanged (one hook = one
target, identical behaviour).

### Concurrent execution as reporter Phase nodes (spinclass#276)

Targets run **concurrently**, each as its own execution-family **Phase node** on
the reporter — the same crap muxing model the pre-merge hook uses. A node has a
unique id; its live output streams as `Output` records tagged with that id, and
its verdict rides on the `node_end`. crap's ndjson writer serializes the wire, so
concurrent output neither races nor tears and the viewport demuxes each target's
output under its own node — **no hand-rolled line-prefixing into a raw sink**.
The nodes' `node_start` records are emitted up front in **declaration order**
(a deterministic ladder, and the reporter's unsynchronized id counter is never
raced); the goroutines then only stream output and close their own node.

One shared wall-clock deadline (`post-merge-timeout`) bounds the phase, so
lock-hold is now **max**(commands + verifies) rather than their sum — the
exclusive region every sibling merge queues behind shrinks to the slowest single
target. A target killed by the shared deadline reports `verdict=timeout`. A
failed node carries `severity=warn` and closes with a non-zero exit, but the
merge still returns success (the phase is non-fatal — the merge already landed);
the `post-merge ` label prefix is load-bearing so `#259`'s wake-surfacing lifts
every `✗ post-merge <name>` line into the async completion summary.

This adopts crap's muxing strategy in place of the first cut's hand-rolled
`sharedActivity`/`prefixedLineWriter` prefixer. It is a **representation change**
inside the phase — verdicts now render as execution nodes (`✓/✗ post-merge
<name>`) rather than result-family test points — but the operator-facing contract
(one verdict per target, the same `verdict`/`stage`/`severity` fields, selection,
and non-fatality) is unchanged; consumer stanzas are untouched. It also **streams
each target's output live to the viewport**, where the test-point cut only
surfaced output on failure as a diagnostic field.

The remaining coordination is minimal and structural: `crap.Reporter`'s id
counter and sticky-error field are not goroutine-guarded (only the underlying
ndjson writer is), so the nodes are allocated up front single-threaded and a
single shared mutex guards each goroutine's node-output/close writes (and the raw
job-log tee). Each target still snapshots the deadline/cancel state the instant
its command returns, so a sibling's later deadline kill can't mislabel a genuine
command/verify failure as a timeout. Targets are independent by construction
(they observe no ordering between each other), which is what makes the fan-out
safe; sequential remains the fallback should a future target ever need to observe
another's side effects.

The legacy single-string `[hooks].post-merge` path is left as a result-family
test point — it is the superseded, single-command shim (no muxing concern) on its
way out; only the live named-targets contract adopts the node model.

### Rejected

- **`[[hooks.post-merge]]` as written** — unreachable without a flag-day; see
  Config shape.
- **Named targets and the legacy string both running when both are set** —
  "each configured thing runs" risks a double-deploy during migration, and the
  parent-string/child-target case would be surprising. Supersede is the
  least-magic rule: the moment a repo declares a target, it owns the phase.
- **A per-target `paths` filter now** — the recorded nice-to-have; deferred
  both to keep the first slice small and to keep targets substrate-neutral
  (a `paths` filter is a git-diff assumption).
- **A third `confirm` stage** — the ack is the tail of `verify`; a third stage
  adds no verdict fidelity (#273 amendment).
- **Per-invocation timeout** — lets N targets hold the lock for N × cap; the
  phase-level deadline preserves the FDR 0023 lock-hold invariant.

## Examples

Deploy a subset, verify each, land the rest untouched:

```toml
[[post-merge]]
name    = "krone"
command = 'kssh krone -- systemctl start deploy@"$SPINCLASS_MERGED_SHA"'
verify  = "kssh krone -- systemctl is-active krone.service && ./bin/deadman-ack krone"

[[post-merge]]
name    = "nikulin"
command = "just infra/hosts/trigger-redeploy-nikulin"
```

    merge-this-session { "targets": ["krone"] }

    ✓ merge bright-olive
    ✓ push
    ✓ post-merge krone (a1b2c3d4e5f6)

A verify that fails is a different verdict from a command that fails:

    ✓ merge bright-olive
    ✓ push
    ✗ post-merge krone (a1b2c3d4e5f6)
      severity: warn
      verdict: verify-failed
      stage: verify
      message: post-merge target "krone" deployed but verify failed: exit
               status 1 (the merge already landed — nothing was rolled back)

and the async wake surfaces it: `merge succeeded; ✗ post-merge krone …`.

A docs-only merge deploys nothing:

    merge-this-session { "targets": [] }

    ✓ merge bright-olive
    ✓ push

## Limitations

- **Inherits every FDR 0023 limitation**: the phase extends the exclusive
  region (a slow target delays sibling merges), fires once per landing with no
  delivery guarantee, and cancelling frees the lock without killing the deploy's
  process tree.
- **The whole-phase cap is shared across concurrently-running targets.** Targets
  run in parallel (spinclass#276), so the phase holds the merge lock for
  **max**(all selected targets), not their sum. `post-merge-timeout` must be
  sized for the *slowest single* target's command + verify; the shared deadline
  kills every still-running target at once, each reporting `verdict=timeout`.
- **Supersede is silent.** A child that adds one `[[post-merge]]` target
  silently dormants an inherited `[hooks].post-merge` string. Documented, and
  the natural migration, but a repo relying on the inherited string will notice
  it stop.
- **No `paths` auto-selection.** Selection is caller-specified; a target does
  not yet deploy automatically only when the diff touches its paths (#273's
  recorded nice-to-have, deferred).
- **`verify` shares the target's working directory and env** with `command`;
  there is no separate verify env beyond the `SPINCLASS_MERGED_*` facts.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| config key | top-level `[[post-merge]]` array | the only backward-compatible shape (no TOML union on `hooks.post-merge`, no flag-day); matches the `[[mcps]]` idiom and the phases direction | a schema-version bump (#256) making a nested/typed shape safe to introduce |
| string vs targets | named targets supersede the legacy string | least-magic migration; a target-declaring repo owns the phase | operators wanting both to run (they would split into two targets instead) |
| verify stages | exactly two (`command`, `verify`) | the ack rides verify's tail; a third stage adds no verdict fidelity | a consumer needing a verdict split verify cannot express |
| unknown-target selection | fatal pre-landing | a typo silently skipping a deploy is worse than a hard stop before anything ships | selection becoming dynamic enough that an unknown name is routine |
| timeout scope | whole phase | N targets under one cap keeps the FDR 0023 lock-hold bound | per-target isolation mattering more than the aggregate bound |
| execution order | concurrent (max lock-hold), emit in declaration order (spinclass#276) | targets are independent hosts, so max() lock-hold beats sum() and changes no contract; ordered emit keeps the ladder deterministic | a future target needing to observe another's side effects (sequential stays the fallback) |

## More Information

- Issue: #273 (+ 2026-08-20 amendment, from the circus#132 deadman ratification).
  Motivating consumer: circus krone/nikulin self-deploy.
- Builds directly on [FDR-0023](0023-post-merge-hook.md) (the single-command
  post-merge hook, the lock placement, the non-fatal reporting, the env facts).
- Related: [FDR-0022](0022-per-repo-merge-queue.md) (the lock the phase runs
  under), spinclass#259 (the wake-surfacing this extends), spinclass#256
  (schema-version indicator — coordinate on config-shape changes).
- Code: `internal/sweatfile/sweatfile.go` (`PostMergeTarget`, `PostMerge` field,
  `ActivePostMergeTargets`, `PostMergePhaseActive`), `internal/sweatfile/apply.go`
  (`PostMergeTarget.Run`), `internal/sweatfile/hierarchy.go` (dedup-by-name
  merge), `internal/merge/merge.go` (`runPostMergePhase`, the `targets` thread
  through `PrepareMerge`/`FinishMerge`/`MergeImplicit`), `internal/validate`
  (`CheckPostMergeTargets`), `internal/job/runner.go` (`postMergeFailureLine`
  surfacing all targets), `cmd/spinclass/commands_mcp_only.go` (the `targets`
  tool param).
- `spinclass-sweatfile(5)` `[[post-merge]]` and `[hooks]` §§ post-merge.
