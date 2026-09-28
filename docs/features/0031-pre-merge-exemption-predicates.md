---
status: proposed
date: 2026-09-28
promotion-criteria: |
  exploring -> proposed: the operator accepts this draft's recommendation (or
  picks an alternative). As of 2026-09-28 the operator has accepted: the
  base-tree trust rule, terminal merges staying exempt, the late MCP refusal,
  leaving the Bash `sc merge` hole open, and fixing FDR 0007 drift alongside
  this work. What remains is the go-ahead to implement.
  proposed -> experimental: `[[pre-merge-exemptions]]` ships and circus's
  sweatfile declares its lock-only predicate. An agent-driven MCP merge of an
  input bump then lands with no attestation and an `exempt (lock-only)`
  verdict. Terminal merges also emit the informational bypass point.
  experimental -> accepted: several weeks of MCP input-bump merges with no
  false exemption. A hand-edited flake.nix bump via MCP must be refused, and
  no MCP merge may regress to needing attestation it previously did not.
---

# Pre-merge exemption predicates (pluggable attestation policy)

> **Proposed** — designed by pairing spinclass/cool-chestnut/bozo with
> circus/firm-banyan/krusty on 2026-09-28, then implemented in the same
> session. No sweatfile declares an exemption yet. Addresses spinclass#327. For
> #326 it makes the terminal bypass visible, but keeps it, per an operator
> decision the same day. Folds in #325 as an adjacent passthrough.
>
> Where the code lives:
> - `sweatfile.PreMergeExemption` / `ActivePreMergeExemptions`;
> - `merge.AttestationGate` (carried on `PostMergeOptions.Gate`; its zero
>   value is `GateTerminal`);
> - `merge.runAttestationPolicy` (`internal/merge/policy.go`);
> - `decideMergeGate` in the MCP handlers;
> - `validate.CheckPreMergeExemptions`.

## Problem Statement

FDR 0007's `[[pre-merge-skills]]` gate is MCP-only. `merge-this-session`
refuses until `nothing-but-the-truth` has recorded an attestation. The terminal
`sc merge` / `sc check` / `sc run` bypass it entirely, and they leave no record
that they did (#326).

Two consumers fall through that gap:

1. **Unattended automation.** circus's `circus update` host CLI (circus
   FDR-0029) runs as sasha from a terminal or a transient user unit, with no
   agent in the loop. Today it runs
   `sc run --no-merge --no-close` (a one-line script: bump inputs, commit)
   and then `sc merge --target <branch> --post-merge-targets <host>`. There is
   nobody to attest skills, and nothing to review beyond a lock diff that the
   gate build already exercises. The operator decided that such diffs should
   need no attestation, but spinclass must not hardcode "flake.lock-only" as a
   special case. The repo supplies the rule.
2. **Agent-driven trivial merges.** An agent merging a pure input bump through
   `merge-this-session` must still perform a skill ritual over a lock diff,
   which the exemption removes.

A third gap, agents shelling out to `sc merge` via Bash, stays open by
operator decision (see #326 below).

## Interface

### Schema

A new top-level sweatfile array of tables. It uses the `[[post-merge]]` /
`[[mcps]]` idiom: dedup-by-name across the hierarchy, and a name-only entry is
a removal sentinel.

```toml
[[pre-merge-exemptions]]
name = "lock-only"
command = 'bash -euo pipefail infra/hosts/home/circus-cli/lock-only-diff.bash "$SPINCLASS_MERGE_BASE" "$SPINCLASS_LANDING_SHA"'
```

- `command` is run via `sh -c`, the same as `[hooks]` / `[[post-merge]]`.
- **Exit 0** means *this diff is exempt from every `[[pre-merge-skills]]` entry*.
- **Nonzero** means *not exempt*. That is a normal verdict, not an error.
- If the command fails to spawn or hits the timeout, the verdict is also
  "not exempt", reported with its own verdict. Every failure fails closed.
- Predicates are tried in declaration order, and the first exit 0 wins.
  Declaration order is resolved-hierarchy order, as for `[[post-merge]]`.
- **v1 scope: all-or-nothing.** An exemption covers all skills. A later
  optional `skills = ["review", …]` field could narrow it without breaking
  this schema. That is deferred until a consumer needs it.
- The gate is only live when both conditions hold:
  - `ActivePreMergeSkills()` is non-empty. Exemptions are meaningless
    without a gate.
  - `ActivePreMergeExemptions()` is non-empty.

`validate.CheckPreMergeExemptions` warns in three cases:

- a missing name;
- a duplicate within one file;
- exemptions declared with no `[[pre-merge-skills]]`, which makes them dead
  config.

### Evaluation point

The predicate runs **inside `merge.FinishMerge`**, after the landing sha is
known and **before the pre-merge hook**. On the queued path (FDR 0022) that
means it runs under the landing lock, after any landing rebase in the `.land-*`
worktree. It therefore judges exactly the commits that will land. The
predicate is cheap and the hook is expensive, so a refusal costs one predicate
run, not a build.

The evaluation point cannot be earlier:

- The MCP handler boundary sees only pre-rebase `HEAD`.
- Async queued merges re-run `PrepareMerge` at dequeue, so there is no stable
  sha to judge until then.
- A predicate evaluated at the handler could be invalidated by a concurrent
  commit before the pin.

### Environment

The predicate inherits the merging user's environment (no devshell), plus
`WORKTREE` (the base worktree) and the following. As built, it does NOT get the
`SPINCLASS_SESSION_ID` / `_WORKTREE` identity env: the merge layer has no
session state in hand. It gets the post-merge-style merge facts instead.

| Variable | Meaning |
|---|---|
| `SPINCLASS_MERGE_BASE` | `git merge-base <landing target> <landing sha>`: the default-branch tip the landing sits on (the remote-tracking ref, #315, as observed under the lock). |
| `SPINCLASS_MERGED_BRANCH` / `_DEFAULT_BRANCH` / `_REPO_PATH` | As for `post-merge`. |
| `SPINCLASS_LANDING_SHA` | **The head to judge.** Equals the pinned sha unless the queue rebased the landing. |
| `SPINCLASS_PINNED_SHA` | The pre-landing pin (same meaning as for `post-merge`). Informational: after a queue rebase it is *not* a descendant of `MERGE_BASE`. |
| `SPINCLASS_EXEMPTION` | The predicate's `name`. |

A predicate SHOULD judge `git diff "$SPINCLASS_MERGE_BASE" "$SPINCLASS_LANDING_SHA"`
from git objects only, and MUST NOT depend on a working tree containing head.

### Trust rule: the predicate comes from the base, not the head

This is the load-bearing decision. A predicate read from the branch being merged
can vouch for itself: an agent edits the recipe to `exit 0`, and its own diff is
then exempt. So:

1. **Definition from base.** The exemptions are resolved from
   `sweatfileio.LoadWorktreeHierarchy(home, repoPath, <base worktree>)`. That
   is the normal cascade (global → parent dirs → the main checkout's
   sweatfile), with the *worktree* layer being the detached checkout at
   `MERGE_BASE` instead of the session worktree. Only
   `ActivePreMergeExemptions()` is read from it. None of those layers is the
   branch being merged:
   - the main checkout's working-tree sweatfile is not where a session
     edits;
   - the out-of-repo layers are not branch-controlled at all.

   If an exemption exists only in the head's sweatfile, it does not apply to
   its own merge (`TestPolicyBranchCannotVouchForItself`).
2. **Code from base.** The command runs with cwd in a transient detached
   worktree checked out **at `MERGE_BASE`**,
   `.exempt-<branch>-<shortsha>-<pid>`. It is created and torn down around the
   policy stage, and reaped by `sc clean` like `.merge-*` / `.land-*` if
   orphaned. So any script or justfile the command references is
   already-landed code.
3. **No head-controlled devshell.** The command runs **without** `direnv exec`.
   The devshell is itself head-controlled: a bumped `flake.lock` can swap the
   `just`/`bash` the predicate would run. PATH is the merging user's
   inherited environment. Predicates should therefore need only POSIX + git.
   For this reason circus will call its script with `bash` directly rather
   than through `just`.

The cost is one extra detached checkout. It is paid only on an MCP merge with
the gate live and no attestation supplied.

### Policy decision table

The gate is live: skills are declared and exemptions may be declared.

| Path | Attestation present | Exemptions declared | Outcome |
|---|---|---|---|
| MCP | yes | any | Consume as today (#265 ordering unchanged). Predicates are **not run**, and the verdict reads `attested`. |
| MCP | no | no | Fast refuse at the handler, exactly as today. |
| MCP | no | yes | Handler **Peeks and does not consume**, then proceeds. `FinishMerge` runs the predicates. If one exempts, the merge continues. If none do, the merge fails **before the hook, nothing lands**, with the FDR 0007 attestation directive plus a line naming the predicates that declined. |
| CLI (`sc merge`, `sc run`) | n/a | any | **Always lands, as today.** Predicates are **not run**. The only change is an informational record (below). |

`sc check` / `check-this-session` are unchanged. A check lands nothing, so
there is nothing to exempt. `check-this-session` keeps consuming as today.

### #326: terminal merges stay exempt, visibly

**Operator decision (2026-09-28, relayed via circus/firm-banyan/krusty):**
"terminal sc-merges are always exempt from attestation for now, plugins cannot
change that". Therefore:

- The terminal path has **no gate, no knob, and no `--skip-attestation` flag**.
  `[[pre-merge-exemptions]]` is an MCP-path mechanism only. It can widen what an
  un-attested MCP merge may land. It can never make a terminal merge refuse or
  change its outcome.
- The predicates are **not run** on the terminal path, not even for recording.
  A predicate that is never consulted cannot change the outcome, and the merge
  pays for no extra checkout.
- #326's minimum ask is met by one **informational** reporter point, emitted
  where the MCP path would emit its policy verdict:
  `# SKIP attestation bypassed (terminal)`. It is emitted only when the gate
  is live (skills declared) and is never `severity=fail`. It replaces today's
  *silent* bypass and changes no one's workflow.

Consequences for circus. `circus update` is a terminal merge, so it gets no
enforcement from spinclass. circus keeps its own lock-only pre-check inside
`circus update` as circus policy. Its `lock-only` predicate is still useful in
circus's sweatfile, but for **agent-driven** MCP merges of input bumps, which it
lets land without attestation.

Rejected in light of that decision (recorded for a future revisit):

- A `[hooks].cli-attestation = "record" | "enforce"` knob;
- a `--skip-attestation=<reason>` two-halves flag.

Both would also have closed the "agent shells out to `sc merge` via Bash" hole.
That hole stays open by operator decision (2026-09-28), as a known and accepted
gap. It is low-risk in practice: fleet agents run with Bash disabled in favour
of `develop-run` and just recipes. A workaround still exists, but agents have
proven averse to taking it. Any future mitigation belongs to permission tiers /
PreToolUse, not to this gate.

### Verdicts

These are one reporter test point (phase node) per merge, emitted before the
pre-merge hook node. They land in the ndjson stream, the job log and the async
wake (#259 wake-surfacing lifts the `✗` and SKIP lines):

As built, the labels are:

- `✓ pre-merge policy: attested`
- `✓ pre-merge policy: exempt (lock-only) base=<short> landing=<short>`. The
  facts ride in the label, because crap's `Ok` carries no diagnostic.
- `✗ pre-merge policy`, whose message wraps `ErrAttestationNotExempt` and names
  each declined predicate with its verdict (`exit N`, `timeout`,
  `spawn-failed: …`) plus the tail of its output
- `# SKIP attestation bypassed (terminal): …` (terminal path, informational
  only)
- a dormant gate emits nothing, as today

No commit trailer: the landing commits are already pinned and signed, and
rewriting them to add one is out of the question. The record lives in the merge
output. A statsd counter per verdict is deferred.

### Privilege

The predicate runs as **the merging user**. That is the same identity and trust
as the pre-merge hook, never elevated. In the fleet's account split (dev work as
non-wheel `sasha`, `--local` host operations as wheel `daddy`), merges are
always `sasha`. `daddy` only runs `circus update --local --yes` (a local
`nixos-rebuild switch`), which involves no spinclass. The predicate adds no new
privilege boundary.

Base-tree evaluation matters more than privilege here. The predicate is
repo-supplied code executed by the merger, so the protection is that the code
is *landed* code, not the branch's.

### Adjacent: #325

`sc run` gains `--post-merge-targets <a,b>` / `--no-post-merge`, passed
straight into `merge.PostMergeOptions.Targets` with `sc merge`'s semantics:

- omitted means all targets;
- `--no-post-merge` or an empty list means none;
- an unknown name fails in `loadAndGate` before landing.

This lets circus collapse `sc run --no-merge --no-close` + `sc merge` into one
`sc run`. The docs must disambiguate these flags from `sc run`'s existing
`--post-merge H` (dynamic shell hooks). The requested machine-readable session
handle is left for a separate issue.

## Options considered

1. **Hardcoded lock-only rule in spinclass.** Rejected by the operator. It is
   repo policy, not spinclass policy.
2. **Predicate at the MCP handler, judging pre-rebase `HEAD`.** It is simpler
   and keeps the fast-fail, but it judges a sha that may not be what lands.
   A concurrent commit, a repair amend or a queue rebase can each change it,
   and the queued async path has no pin at enqueue.
3. **Predicate read from head (the build worktree at the pinned sha)**, as the
   issue first sketched. Rejected because it can vouch for itself (see the trust
   rule). It is safe for circus's particular predicate, whose permitted diff
   cannot edit the script, but not in general.
4. **Per-skill `exempt-if` on each `[[pre-merge-skills]]` entry.** More
   expressive, but it duplicates the predicate per skill, and no consumer needs
   partial exemption yet. The chosen schema can grow `skills = […]` later.
5. **CLI gating** (a `record` → `enforce` knob, `--skip-attestation=<reason>`,
   or a CLI attestation file). The first pairing draft recommended the knob.
   The operator ruled it out: terminal merges stay exempt, and plugins cannot
   change that.
6. **Chosen:** named base-tree predicates evaluated in `FinishMerge` on the MCP
   path only, plus an informational bypass point on the terminal path.

## Limitations and open questions

- **PATH trust.** Without a devshell, the predicate's `bash`/`git` come from the
  merging user's PATH. On the fleet that is the home-manager profile, which is
  outside the branch's control. On an arbitrary host it is whatever is there.
- **Base-tree sweatfile read.** There is no second load mode: the normal
  `LoadWorktreeHierarchy` runs with the base checkout as the worktree layer
  (see the trust rule). A `sweatfile` that fails to parse at the base
  therefore yields no exemptions (fail closed), not an error.
- **Predicate cap.** Each predicate runs under a fixed 5m timeout
  (`exemptionTimeout`). It is not configurable, and under the queue that
  time is lock time.
- **Late refusal on the MCP path.** A non-exempt, un-attested MCP merge now
  fails after fetch, rebase and lock acquisition, instead of at the handler.
  The cost is small because the refusal precedes the hook. Nothing lands,
  and a plain re-merge after attesting works.
- **`disable-merge-queue` / `disable-merge-build-worktree` paths.** The
  evaluation point moves with `FinishMerge`. `MERGE_BASE` is then the
  fetched default tip at gate time, and `LANDING_SHA` equals the pinned sha.
  These paths still need a base worktree.
- **Implicit sessions** cannot merge (#317), so they are out of scope.
- **FDR 0007 drift** noticed while reading. Its status is still `proposed`
  although the gate ships and spinclass's own sweatfile uses it. Its State
  section also says the attestation is cleared *after* the hook, but the code
  consumes it before `PrepareMerge` (sync) or at dispatch/enqueue (async).
  The operator decided (2026-09-28) to fix both in the same change that
  implements this FDR. Done (#328): the State text is corrected and 0007 is
  promoted to `experimental`.
