# Per-commit repair hook — design

**Date:** 2026-06-16
**Status:** approved (brainstorm); implementation pending
**Supersedes (in spinclass's own usage):** the merge-time REPAIR phase (FDR 0018)

## Problem

The pre-merge REPAIR phase (FDR 0018) runs `conformist --commit --amend
--exit-zero-on-fix` once, at merge time, against the session worktree's `HEAD`.
`--amend` only ever touches the top commit, so in a sequence A→B→C, drift
introduced in A is detected at merge and folded into **C** — the only commit
amended. The merged *tree* ends up conformant, but:

- intermediate commits A and B remain non-conformant in history (bad for
  per-commit CI and bisectability), and
- C accumulates formatting churn that does not belong to it.

## Goal

Both of:

1. **Catch drift at authoring time** — never create a non-conformant commit in
   the first place; repair happens as each commit is made, not deferred to
   merge.
2. **Clean per-commit history** — every commit in the merged sequence is
   individually conformant; A's drift fix lands on A.

A per-session **pre-commit hook** delivers both with one mechanism, because a
commit that is formatted as it is authored is conformant both at authoring time
and in history.

## Approach (chosen: hook only)

Considered three shapes:

- **Pre-commit hook (chosen).** spinclass installs a `pre-commit` hook into the
  session worktree that formats staged content before each commit. Delivers #1
  and #2 for in-session commits.
- **Merge-time per-commit `rebase --exec`.** Generalize REPAIR to repair every
  commit in the range at merge. Delivers #2 but not #1 (agent still authors
  dirty commits). Rejected as primary — does not meet the authoring-time goal.
- **spinclass `commit` tool replacing `grit commit`.** Only works if the agent
  uses it; never covers human/IDE/raw-git commits. Strictly dominated by the
  hook on coverage. Rejected.

`grit` is not a Go library (it is bash-wrapper MCP tools in moxy), so "spinclass
consumes grit" was a non-starter for this problem.

The hook-only choice accepts that commits made with `--no-verify`, or imported
via rebase/cherry-pick, can slip past the hook. There is **no merge-time
backstop** in this design (REPAIR is retired). This is a deliberate tradeoff:
authoring-time, best-effort repair over a belt-and-suspenders guarantee.

## Mechanism

### Installation & isolation

`shop.Create()` (which runs on both `sc start` and `sc resume`, idempotently)
writes a generated `pre-commit` script to
`<worktree>/.spinclass/hooks/pre-commit` and points git at it scoped to the
session worktree only:

```
git config extensions.worktreeConfig true        # once per repo, idempotent
git config --worktree core.hooksPath <wt>/.spinclass/hooks
```

Worktrees share `$GIT_COMMON_DIR/hooks` by default, so without `--worktree`
isolation the hook would be installed into the main checkout and every sibling
worktree. The `extensions.worktreeConfig` extension is what makes
`core.hooksPath` per-worktree.

`.spinclass/` is already git-excluded and already holds worktree-local state
(`state-<rand>.json`, `job.json`), so the hook script fits there and is torn
down with the worktree on `sc close` — no explicit teardown.

> **Feasibility spike (first implementation step, not yet settled).** Before
> writing the installer, a scratch-repo spike must confirm:
> 1. per-worktree `core.hooksPath` truly isolates the hook to one worktree;
> 2. enabling `extensions.worktreeConfig` does not disturb the main checkout —
>    in particular when `core.worktree` / `core.bare` live in the *common*
>    config (a documented git footgun);
> 3. the per-worktree config is cleaned up on `git worktree remove`.
>
> If the spike fails, the isolation mechanism is revisited before the installer
> is built.

### What the hook runs

Canonical command (a sweatfile default, not hardcoded — see below):

```
conformist --staged --exit-zero-on-fix
```

`--staged` formats only the staged blobs and restages them, so the in-flight
commit lands conformant with **no extra commit and no amend**. The generated
wrapper script:

1. guards on `command -v <first-token>`; if the formatter is absent, exits 0
   (silent no-op — a missing tool never blocks commits);
2. runs the configured command;
3. on exit 0 (conformant, or fixed-and-restaged via `--exit-zero-on-fix`), the
   commit proceeds with the restaged content;
4. on any nonzero exit (refusal — including partial-stage — or operational
   error), logs a one-line warning to stderr and **exits 0 anyway**. The hook is
   best-effort and never blocks a commit.

> **Verify:** that `--exit-zero-on-fix` collapses `--staged`'s restage exit (3)
> to 0. If it does not, the wrapper maps 3→0 itself. (Either way step 4 makes
> the hook non-blocking, so this only affects whether the "fixed" case is logged
> as a warning.)

GPG note: because the hook only *restages* and the commit itself signs once
normally, this sidesteps REPAIR's GPG limitation entirely — no amend, no
re-sign, no pushed-HEAD refusal.

### Config surface

New sweatfile fields under `[hooks]`, mirroring `[hooks].repair`:

- `pre-commit` (`*string`) — the command; tool-agnostic, cascade-merged
  (`global → parent → repo`), scalar override.
- `disable-pre-commit` (`*bool`) — scalar override to suppress an inherited
  command without clearing the string.

Accessors mirror `RepairHookCommand` / `RepairDisabled` / `RepairActive`.
spinclass's own sweatfile sets `pre-commit = "conformist --staged
--exit-zero-on-fix"`; every other repo opts in. `sc validate` flags an empty /
malformed entry.

### Scope

Real `sc start` worktree sessions only. Implicit / main-checkout sessions
(FDR 0014) are **out of scope** — same call REPAIR made: their HEAD may be
pushed, and we should not install hooks into a user's main checkout. Tracked as
a followup.

## Rollback strategy

Dual-architecture transition, not a hard cut:

- **Coexist:** leave `merge.runRepairPhase` and `[hooks].repair` in the codebase.
  Stop setting `repair` in spinclass's sweatfile; start setting `pre-commit`.
  Both mechanisms can run simultaneously without harm.
- **Promotion:** after ~7 days of the hook in use with no per-commit drift
  escaping to merge, delete the REPAIR phase code (a separate change).
- **Rollback:** set `[hooks].disable-pre-commit = true` and re-set
  `[hooks].repair` — a one-line sweatfile change, no code revert.

## Tuning levers

- **Canonical pre-commit command** (`conformist --staged --exit-zero-on-fix`).
  Current value chosen for non-blocking restage semantics. Change signal: a
  better invocation emerges, or the exit-code mapping proves wrong.
- **Non-blocking (soft-skip all nonzero) behavior.** Current value: never block
  a commit. Could become configurable (`block-on-error`) if best-effort proves
  too lax. Change signal: drift escaping to merge frequently enough to matter.
- **Hook script location** (`.spinclass/hooks/`). Unlikely to change; listed for
  completeness.

## Known limitations

- **Partial-stage refusal.** `conformist --staged` refuses a partially-staged
  file (`git add -p` then commit with leftover unstaged changes). The hook
  soft-skips this, so the commit proceeds **unformatted** — drift passes
  through. A cross-linked conformist issue is filed to handle partial stages
  gracefully; until then this is the main gap in the #2 guarantee.
- **`--no-verify` bypasses the hook.** A deliberate git escape hatch; such
  commits are not repaired.
- **Imported commits** (rebase / cherry-pick into the session) are not
  reformatted — the hook only fires on commits authored in the session, and
  there is no merge backstop.
- **Implicit/main-checkout sessions** are out of scope (see Scope).

## Deliverables

1. Scratch-repo spike validating the isolation mechanism (gate).
2. `[hooks].pre-commit` + `[hooks].disable-pre-commit` sweatfile fields,
   accessors, codec regen, `sc validate` coverage.
3. Generated hook-script writer + per-worktree `core.hooksPath` installer wired
   into `shop.Create()`.
4. spinclass's own sweatfile switched from `repair` to `pre-commit`.
5. bats integration coverage: commit with drift gets formatted; main checkout
   hooks untouched; `--no-verify` bypasses; missing-tool no-op.
6. Cross-linked conformist issue for partial-stage handling.
7. (Post-promotion, separate change) delete the REPAIR phase code.
