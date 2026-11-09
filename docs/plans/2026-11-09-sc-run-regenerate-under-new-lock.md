# `sc run`: regenerate, format and commit under the NEW lock, and what the freeze shrinks to (design, 2026-11-09)

Status: **proposal only.** Nothing here changes the freeze (#459), the lock
policy (#449) or any security default. Each change below waits for the
operator's go. Written for the operator comments of 2026-11-09 relayed by
circus/eager-holly: (1) skipping the formatter on lock drift (S0) is not the
intent; `sc run` should regenerate, format and commit what the new dagnabit,
purse-first, tommy and the like generate, seamlessly and structurally; (2) the
freeze's threat model looks overblown and premature, it was meant to help
confinement, has not, and has mostly caused issues.

Companions: `2026-11-04-459-frozen-pre-commit-hook.md` (what was built),
FDR 0019 (the 2026-11-04 amendment), FDR 0020 (`sc run`), FDR 0018 (pre-merge
REPAIR), FDR 0035 and the guarded gate (#449), circus#423, spinclass#459,
#460, #464. The 2026-11-09 note on the hook versus lock bumps is another
session's and is not in this worktree; this plan does not rely on its text.

Labels: **R** read in code or docs, **I** inferred or proposed, **U**
unverified.

## 1. The problem, restated (R)

A lock-bumping `sc run` step (eng's `update-nix-repos` cascade) changes
`flake.lock`, so the generators the repo's devShell provides change too
(dagnabit, tommy, purse-first, conformist's codegen checks). Two things then
act on the commit:

- The per-commit dispatcher runs the formatter **frozen from the landed base**
  (`frozenhook.go`), i.e. the OLD generators, and restages with them. On a
  lock-bump commit that puts old-generator output into the commit. A lock
  drift only prints a warning (FDR 0019, 2026-11-04 amendment).
- The merge gate then runs the repo's pre-merge on the commit (the NEW devShell
  if unconfined, the trusted `confined-pre-merge` if confined). Drift between
  old-generator output in the commit and new-generator expectations fails the
  check, or, with `[hooks].repair`, is amended at the top commit only
  (FDR 0018).

So the frozen hook is correct for its threat model and wrong for the one
workflow `sc run` exists for. S0 ("on drift, skip the formatter") would stop
the wrong restamp but leave the regenerated output unformatted and
uncommitted, which is the opposite of the operator's wish.

## 2. What the freeze protects, honestly (R, I)

The #459 exposure is narrow and was demonstrated once (the #449 canary): the
commit-time `nix develop` of a branch's flake fetched an unlocked `path:`
input into the world-readable store. Boundaries of that exposure:

- It needs a flake that declares an input the lock does not hold. A lock bump
  produced by `nix flake update` holds every input it declares.
- A confined worker never reaches the hook: its `grit_commit` runs with
  `core.hooksPath=/dev/null` and `--no-verify` (R,
  `internal/alfa/store/git/confined.go`). The freeze therefore did nothing for
  confinement, which matches the operator's recollection.
- The remaining population is unconfined sessions and the operator's own
  commits, whose code already runs with the committing user's rights through
  direnv's devShell load at attach.

What the freeze costs: every session start, spawn, resume and restart pays a
base devShell evaluation; sessions can carry a stale formatter; and a lock
bump (the `sc run` case) is wrong by construction.

The part of the threat that survives scrutiny is **"evaluate a flake whose
inputs the lock does not pin"**. That is exactly what #449's lock policy
already refuses at the gate. The proposal below uses that one rule as the
admission test everywhere, instead of freezing the whole hook.

## 3. Design: a structural regenerate phase in `sc run` (I)

`sc run` already owns the sequence start, step, merge, teardown (FDR 0020). Add
one phase between the step and the merge, run by `sc run` itself (spinclass
code, not the repo's hook and not the step script):

```
start -> step -> [regenerate] -> merge -> teardown
```

**Trigger (cheap, structural).** After the step exits 0, if `flake.lock` in the
worktree differs from the landed base's (`git diff --quiet <base> -- flake.lock`,
no evaluation), the lock changed and the phase runs. No lock change: the phase
is a no-op and today's behaviour is byte-for-byte unchanged.

**Admission (the one security check).** The new lock is admitted by the #449
lock policy, the same code the guarded gate runs (`merge/guarded.go`): no
local-path or file-scheme input, every declared input pinned by the lock,
evaluation with `--no-update-lock-file --no-write-lock-file`. A refused lock
fails the phase with the policy's reason and leaves the worktree for
inspection (the failure path of FDR 0020). Nothing is evaluated before
admission, so the canary's fetch cannot happen.

**Resolution (the new generators).** With the lock admitted, resolve the
formatter and repair commands against the worktree's own flake at HEAD
(`git+file://<worktree>?rev=<head>`, the shape `frozenhook.go` already builds
for the base) and run them. That is `resolveFrozenHook` pointed at HEAD
instead of the base; no new evaluator.

**Run.** In the worktree, with the resolved NEW tools: the repo's repair
command (`[hooks].repair`, canonically `conformist-repair`, or the pre-commit
formatter over the whole tree when no repair is configured) so codegen checks
rewrite their generated files, then format the changed set.

**Commit shape.** Operator wording is "format and commit". Two shapes, with a
recommendation:

- *Separate commit* (recommended): one commit, subject
  `chore: regenerate and format under updated flake.lock`, authored by the
  session identity, containing only the phase's changes. It keeps the step's
  commit as the script wrote it, makes the generated churn reviewable and
  revertable on its own, and cannot hide a step bug. Nothing to amend.
- *Amend the top commit* (what REPAIR does at the gate): smaller history, but
  only correct when the lock bump is the top commit, and it rewrites the
  step's commit.

If the phase produces no changes, no commit is made (clean success).

**Order versus the hook.** The step's own commits still go through the
dispatcher (so they are formatted by the frozen OLD formatter, or by whatever
section 4 leaves). The regenerate phase runs after, so its output is the final
word on the tree and the merge gate sees new-generator output.

**Confined sessions.** The phase does not run for a confined session (same
rule as REPAIR: the guarded gate runs no repair; the trusted
`confined-pre-merge` stands). U: whether a confined `sc run` exists today;
none is assumed.

**Failure.** Fail closed on admission and on command failure (nonzero exit
propagates, session left intact). It is a deliberate `sc run` phase, not a
commit hook, so fail-open no longer applies: a generator that cannot run
should stop the cascade before it merges unformatted output.

**Surface.** Default on in `sc run`, `--no-regenerate` to skip, and a
`[run].regenerate = false` sweatfile scalar for a repo that does not want it
(U: key name and home, to decide with the operator). `sc check` and
`merge-this-session` are untouched; an agent session that bumps a lock by hand
still has REPAIR at the gate and may run the phase itself later (section 6).

## 4. What the freeze shrinks to (options, I)

The regenerate phase fixes the lock-bump workflow without touching the hook.
The hook can then be left alone or reduced. Options, mildest first:

- **F0, leave it.** Keep the freeze as built; the phase supersedes its output
  on lock bumps. Cost: the operator's stated frustrations (start-time
  evaluation, stale formatter, #460/#464 follow-ups) remain.
- **F1, freeze only what needs it (recommended).** Keep exactly one rule at
  commit time: *the dispatcher never runs `nix develop` against an unadmitted
  flake*. Concretely: on a lock drift the dispatcher may re-resolve the
  formatter from the worktree, but only after the same lock-policy admission
  as section 3, and only with `--no-update-lock-file`; admission refused, or
  nix absent, falls back to the frozen path (or the bare name) with the
  existing warning. Drop the start-time base evaluation for **interactive and
  operator sessions** (they get the name-based resolution of FDR 0019's
  original model plus the policy-gated drift re-eval); keep the frozen,
  base-resolved path only for **unattended/unconfined spawns** (`sc spawn`,
  `sc run`'s own session), where no human is present to see the banner. Net:
  one admission rule shared by the hook and the gate (the "they agree"
  property of #459 §2, kept), the freeze's cost paid only where its remaining
  benefit is, and old-generator restamps gone because the re-eval happens
  under the new lock.
- **F2, retire the freeze.** Revert the dispatcher to pre-#459 (name-based,
  `nix develop` on drift) but put the section 3 admission in front of that
  `nix develop`. Smallest code, closest to the operator's instinct. Cost: the
  hook once more evaluates a branch's flake at commit time for unattended
  spawns; the admission rule is then the only defence, and the #464 review of
  how thorough that admission is becomes a hard dependency.

Recommendation: **ship section 3 first (it alone satisfies the operator's
first comment), then F1.** F2 only if the operator would rather have no
frozen state at all; it trades a reviewed restriction for one that is only as
strong as the admission check.

## 5. Open questions for the operator

1. Commit shape: separate regenerate commit (recommended) or amend the top
   commit?
2. Default on for every `sc run`, or opt-in per repo until it has run a full
   cascade?
3. F0, F1 or F2 for the hook, and whether the freeze should survive for
   unattended spawns (F1) or go entirely (F2)?
4. Admission strength for the phase and the hook: the guarded gate's full
   declared-inputs evaluation (slower, the #449 rule) or only the cheap
   local-input check on the lock (what the frozen base resolution does today,
   #464)? The phase evaluates a branch's flake, so I lean to the full rule
   for it, unlike the base case.
5. May the phase also run for an agent session's explicit `merge-this-session`
   (a lock bump without `sc run`), or stay `sc run`-only?

## 6. Test and rollout sketch (I)

- Unit: trigger fires only on a lock diff; admission refusal fails with the
  policy reason and no store write (stub `nix` never reached); a clean
  regenerate makes no commit; a changed one makes exactly one commit with only
  the generated paths.
- Integration (the missing piece for a bump): a fixture repo whose lock bump
  changes a generator's output; `sc run` ends with the new-generator output
  committed and the gate green, where today it ends with old-generator output.
- Canary: the #459 probe replayed against the phase (a lock-changing commit
  declaring an unlocked `path:` input writes no store path and the phase
  refuses).
- Rollout: phase default-off behind the flag for one cascade run on a real
  repo, then default-on; F1 or F2 as a separate change after that, each with
  its own FDR 0019 amendment.

## 7. Not decided here

No change to the freeze, the lock policy, the guarded gate, REPAIR or any
default. This note proposes; the operator rules.

## 8. Operator rulings (2026-11-09, relayed by circus/eager-holly)

1. **Commit shape:** a separate regenerate commit.
2. **Default:** ON now, not opt-in; `--no-regenerate` stays.
3. **Freeze:** F1, as a separate change after the phase. The freeze is not
   changed yet.
4. **Admission:** the full #449 declared-inputs admission, for the phase.
5. **Merge path:** YES (answered later the same day): the phase also runs for
   an agent's `merge-this-session` lock bump, not only `sc run`. The trigger
   and admission must work at the merge path too, still skipped for confined
   sessions.

Implementation was approved on 2026-11-09 for R0a-R0e plus the merge-path
step R0f below; R1 (F1) stays later and separate, and the freeze is not
touched. The plan doc is the tracker; no issues are filed.

R0b rulings pending (operator): the phase's admission needs a systemd scope
tier (no unscoped fallback in the #449 policy) and an empty forbidden-dirs
list for unconfined sessions.

## 9. Implementation breakdown (I; not started, not filed as issues)

R1 is a separate change after R0.

- **R0a, trigger and plumbing.** In `internal/run`, after a successful step and
  before the merge: `git diff --quiet <landing base> -- flake.lock` decides;
  no diff means skip with no output change. Add `--no-regenerate` to
  `run.ParseArgs` and the usage text; a skipped phase reports a SKIP point.
  Tests: trigger on and off, flag parsing.
- **R0b, admission.** Call the guarded gate's declared-inputs lock policy
  (`merge/guarded.go`) from the phase against the worktree HEAD; refusal fails
  with the policy's reason and nothing is evaluated. Needs the policy exposed
  as a reusable function (U: how tangled it is with the gate's build worktree).
  Tests: unlocked `path:` input refused, no store write.
- **R0c, resolve and run.** `resolveFrozenHook` generalised to take a ref
  (HEAD of the worktree instead of the base), resolving `[hooks].repair` or
  the pre-commit command; run it in the worktree with the identity env of
  `sessionexec`. Tests: stub-nix resolution at HEAD; a failing generator
  exits nonzero and leaves the session intact.
- **R0d, commit.** Stage only the paths the run changed; one commit with the
  agreed subject, session identity, hooks bypassed for this commit (the
  dispatcher would re-format with the frozen formatter); no commit when
  nothing changed. Tests: exactly one commit with only generated paths; clean
  tree makes none.
- **R0e, integration and docs.** The fixture-repo bump test of section 6 and
  the replayed #459 probe; FDR 0020 amendment, `spinclass-run(1)` flag, a
  note in FDR 0019. Confined sessions skip (R).
- **R0f, the merge path (Q5).** The same phase function behind a second
  caller in `merge.PrepareMerge`, before the pin, skipped when
  `guardedMerge()` (confined). Needs the phase to be callable with only
  (worktree, repo, reporter), which is why the trigger lives in
  `internal/bravo/regenerate` (below both callers; dagnabit places it at
  bravo, its imports being alfa and 0 only).
- **R1 (later, F1).** Policy-gated drift re-eval in the dispatcher and
  freeze-only-for-unattended; its own plan, tests and FDR 0019 amendment.
- **Order and size.** R0a to R0e in sequence, each landable on its own;
  default-on only once R0e is green. Risk: default-on changes every
  `sc run` that bumps a lock; the first real cascade run is the check.
  Estimate left to the first build (U).
