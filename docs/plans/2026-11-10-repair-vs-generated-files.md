# Repair must not rewrite generated files: the spinclass piece (2026-11-10)

**Status:** approved 2026-11-10 (§6); being built. This is spinclass's part of ringmaster's
`docs/plans/2026-11-10-repair-and-generated-files.md` ("the ringmaster
note"), which ringmaster/cool-cypress wrote on 2026-11-10 (ea77433 /
f9758a9). The ringmaster note §4 assigns spinclass the "detection" row; this
proposal goes further, to protection, because the operator asked for the fix
to be "fleet-wide, effortless and self-healing" (relayed by
circus/eager-holly). The conformist piece (`skip-generated` in the fleet
repair config) and the ringmaster/purse-first piece (the generator formats
its own output) are other repositories' work; the coordinator routes them.

Labels: **O** observed this session, **R** read in code or docs, **I**
inferred or proposed, **U** unverified.

## 1. What goes wrong (R, from the ringmaster note, and spinclass code)

- **R, ringmaster note §1 (its own O):** in the 2026-11-10 cascade, the
  regenerate phase committed a dagnabit facade that had been reformatted by
  gofumpt: adjacent `const` lines were regrouped into `const (...)` blocks.
  ringmaster's `facade-drift` check byte-compares the facade against raw
  dagnabit output, so the gate failed.
- **R, ringmaster note §1 (its I, by elimination):** the formatter was the
  `[hooks].repair` ringmaster inherits from the fleet sweatfile.
- **R, spinclass `internal/charlie/regenphase/run.go`:** each round runs
  `[hooks].pre-commit` (staged, which is where the facade lane regenerates),
  then `[hooks].repair` over the whole tree. Rounds stop at the first round
  that changes nothing, with at most 3 (`maxRounds`).
  - **I, corrected 2026-11-10:** when the generator and the formatter
    disagree, the rounds do not settle.
    - In ringmaster's cascade, the formatter's bytes were committed
      (ringmaster note, its O).
    - In a stub test built with the guard (`allow` mode), round 2 instead
      failed with git's "amend … would make it empty" and the phase put the
      tree back (O, subagent run).
    - Which happens likely depends on how the real repair amends (U).
    - Either way, the result is not the generator's bytes.
- **R, `internal/delta/merge/merge.go:421-431`:** on a merge, the regenerate
  phase runs in `PrepareMerge` immediately before the pre-merge REPAIR phase
  (FDR 0018). REPAIR runs the same `[hooks].repair` again
  (`hookrun.Repair`, `merge.go:1777`).
  - **I:** a fix confined to the regenerate phase would be undone by REPAIR
    a moment later. Any repo with an inherited formatting repair and a
    generated file is exposed on every merge, not only on lock bumps.
  - **U:** whether that has already caused gate failures outside lock bumps.
    I have not checked ringmaster's history before the stopgap.
- **R, `grep` of spinclass:** nothing in spinclass recognizes
  generated-code markers today. The matches are in the codec files that
  carry the marker themselves, plus one in `basebranch`.

## 1b. The stages, and what each may overwrite (R unless marked)

The operator asked for a clear description of how the stages build on each
other toward a stable result. Every stage that writes is listed, in the
order a merge runs them. The order of 2-5 is `PrepareMergeWith`
(`merge.go:306-331`); 6-8 is `FinishMerge` (`merge.go:602-792`).

| # | Stage | When | Command | Role | Sees | May overwrite |
|---|---|---|---|---|---|---|
| 1 | Per-commit hook (FDR 0019, #459) | every `git commit` in the session | frozen `conformist-pre-commit`: the formatter resolved from the **landed** default branch's devShell at session start | format, plus codegen lanes the config triggers on staged files | staged files | the staged files it formats or regenerates, restaged into that commit |
| 2 | Rebase | each merge, `PrepareMerge` | `git rebase` onto the landing target | integrate | branch commits | nothing by content (conflicts refuse) |
| 3 | Regenerate phase (2026-11-09 plan) | only when `flake.lock` changed against the landed branch | (a) `[hooks].pre-commit` in staged mode over the whole branch diff; (b) `[hooks].repair` over the whole tree; rounds (a)→(b) until one changes nothing, at most 3 | (a) **generates** under the new lock; (b) **formats** | (a) branch diff staged; (b) whole tree | any tracked file, as one separate regenerate commit |
| 4 | REPAIR (FDR 0018) | every merge with `[hooks].repair` active | `[hooks].repair`, e.g. `conformist --commit --amend --exit-zero-on-fix` | **formats and fixes** (a conformist repair also runs codegen lanes) | whole tree | any tracked file, amended into HEAD |
| 5 | Pin | | `HEAD` becomes the pinned sha | freeze what is verified | | nothing |
| 6 | Landing rebase (FDR 0022, 0029) | only when the default branch moved during the queue wait | replay the pinned commits in a `.land-*` worktree | integrate again | pinned commits | nothing by content; **no stage 3 or 4 re-runs here** |
| 7 | VERIFY gate (FDR 0013) | each merge, under the repo lock | `[hooks].pre-merge` (`just`) on the landing sha, in a detached build worktree | **verify**, including drift checks such as ringmaster's `facade-drift` | the landing tree | nothing that lands (I: edits in the build worktree are not committed) |
| 8 | ff-only, push, post-merge | | | land, deploy | | nothing in the tree |

**How they are meant to compose (I, from the table):**

- Stage 1 keeps each commit formatted, as the landed branch formats.
- Stage 3 re-derives generated files when the toolchain itself changed.
- Stage 4 is the last writer before the pin, so whatever it leaves is what
  stage 7 verifies.
- Stage 7 writes nothing. It only says whether the result of 1-4 is
  stable: regenerating or reformatting would leave it byte-identical.

**The bug in these terms (I):**

- Stage 3(b) and stage 4 are both formatters, and both run after the last
  generator, 3(a).
- Stage 7's drift check compares generated files against the **generator's**
  bytes.
- So any formatter that disagrees with the generator makes 1-4 converge on
  bytes stage 7 rejects. The guard in §3 makes generator output immune to
  the formatting stages (3b and 4), which leaves the generator as the last
  writer of generated files.

**A gap the table shows, not in this proposal's scope (I):** stage 6 replays
pinned commits onto a moved tip without re-running 3 or 4. A generated file
made against the old tip can then be stale against sources that landed in
between. Stage 7 catches that as drift, a refusal rather than a bad landing.
Recorded here; the plain fix is a re-merge.

## 2. The invariant spinclass enforces (I)

> A file that carries the generated-code marker is never changed by
> `[hooks].repair` when another configured hook can regenerate it. Its bytes
> are whatever the generator wrote. If repair edits one, spinclass puts the
> generator's bytes back and says so.

This is the ringmaster note's §2 invariant, enforced at the point where
spinclass runs repair. It holds whatever the repair command is: conformist
with or without `skip-generated`, a bare gofumpt, or something else. That
matters because the ringmaster note leaves the exact inherited repair
unverified (its U, §1). Option A (conformist `skip-generated`) remains the
real fix. This guard is what makes the fleet safe before option A lands, and
in any repo whose repair is not conformist.

## 3. The proposal (I)

**A shared helper used by both callers of repair**: the regenerate phase's
`repairStep` and the pre-merge REPAIR phase.

1. **Snapshot.** Before running repair, record the tree it starts from
   (HEAD, since both callers run repair on a clean tree; R: `run` refuses a
   dirty tree, and REPAIR refuses one per #345).
2. **Run repair** exactly as today.
3. **Find generated files repair changed.** Diff the snapshot against the
   result. That covers repair's own amend, staged edits and unstaged edits,
   all of which the callers already fold.
   - A changed file is "generated" when its **pre-repair** contents carry the
     marker.
   - v1 marker: Go's convention, a line matching
     `^// Code generated .* DO NOT EDIT\.$` before the first non-comment,
     non-blank text. **U:** the exact wording of that rule is recalled, not
     read this session. It is the same marker conformist's `skip-generated`
     keys on (ringmaster note §2, A), and dagnabit facades and tommy codecs
     both carry it (R: the ringmaster note, and spinclass's own
     `sweatfile_tommy.go`).
4. **Put the generator's bytes back.** For each such file, restore the
   snapshot's version into the commit, using the same fold machinery the
   callers already have: a new commit, or an amend of the regenerate commit.
   - Emit one test-point line:
     `repair reformatted generated file(s) <paths>; kept the generator's
     bytes (set skip-generated in the repair's conformist config to silence
     this)`. The line is `ok` with a diagnostic, so it fails nothing.
5. **Stop the oscillation.** With step 4, a round in which only repair
   touched generated files changes nothing, so the rounds reach their
   fixpoint and the loop ends normally (I).

**When repair is the generator.** If `[hooks].pre-commit` is not configured
(or not active), repair is the only hook that can regenerate anything. A
conformist repair, for example, runs codegen lanes (R: `conformist(1)`'s
`codegen-repair`). The guard then stands aside and repair's edits to
generated files are kept. The rule is "protect generated files from repair
when another configured hook owns regeneration", not "never let repair touch
generated files".
- **I:** this keeps a repo that relies on repair for codegen working, at the
  cost of not protecting it from its own formatter. The open question below
  asks whether that is the right default.

**Configuration (sweatfile, scalar in `[hooks]`):**

    [hooks]
    repair-generated = "protect"   # protect (default) | fail | allow

- `protect`: as above. It is the default, so the fix is fleet-wide with no
  per-repo edit.
- `fail`: refuse the phase or REPAIR with the same message and put the tree
  back. This is the ringmaster note §4's "detection" behaviour, for a repo
  that wants the conflict to stop the merge.
- `allow`: today's behaviour.
- **Layers (ruled in Q2, §6):** `protect` and `fail` only narrow what
  repair may do, so they are read from every layer. `allow` is honoured
  only from trusted layers (the global layer and layers above the repo).

**Self-healing.** With the guard on, ringmaster's stopgap `repair = ""` can
be dropped, so ringmaster inherits the fleet repair again.
- Its hand-written Go gets formatted. The ringmaster note §3 suggests it has
  existing gofumpt drift (its O).
- Its facade keeps dagnabit's bytes, and `facade-drift` passes (I; U until a
  cascade or merge runs).
- Correcting the existing drift is then one ordinary repair commit in
  ringmaster, by ringmaster's session.

## 4. What this does not do

- It does not touch the per-commit hook (FDR 0019, #459). That hook runs the
  frozen `conformist-pre-commit`, not `[hooks].repair` (R: CLAUDE.md). The
  #459 F1 restamp that dropped the codemod output from the bump commit is
  its own fix, and the ringmaster note §4 already points at it.
- It does not decide what the gate's drift checks compare against. That is
  option B's question for ringmaster and purse-first.
- It does not cover generated files without the Go marker (other languages,
  or files marked only by path). Ruled in Q3: the later answer is that
  generators declare their outputs, integrated with ajour and profiles, not
  a detection glob list.

## 5. Tests (I, for when it is approved)

- **A regenerate-phase table test.** A stub pre-commit writes a "generated"
  file with the marker; a stub repair reformats it. Check:
  - `protect`: the commit holds the stub generator's bytes, there is one
    diagnostic line, and the rounds end at round 1 or 2, not 3;
  - `fail`: the phase fails and the tree is put back;
  - `allow`: today's result.
- **The same stubs through `PrepareMerge`'s REPAIR**, so the second caller is
  covered.
- **Repair as the only hook:** a generated file edited by repair is kept.
- **Marker detection:** marker before the package clause, marker after it
  (not generated), a wrong case, a `/* */` comment.

## 6. Operator rulings (2026-11-10) and the remaining questions

Relayed by circus/eager-holly/clarabell from AskUserQuestion
(chat-1d0e3b70).

- **Q1. Default: `protect`.**
  - **End state (recorded, not built):** ajour prevents this structurally.
    The profile and stencil layer forbids a formatter from touching
    generated files, so no runtime guard is needed. The §3 guard is the
    runtime stand-in until then.
- **Q2. `allow`: trusted layers only, for now.** The operator expects to
  need to relax the guard temporarily to unblock a repair, so that escape
  hatch stays at operator level.
  - **End state (recorded):** a repository layer may set it, through
    profiles and ajour.
  - This changes §3: `protect` and `fail` are read from every layer;
    `allow` is honoured only from the global layer or layers above the
    repository, and ignored with a `sc validate` warning elsewhere.
- **Q3. `generated-paths`: later, and not as detection.**
  - **Direction (recorded):** generators (dagnabit, tommy, purse-first)
    declare the files they generate, declaratively, and those declarations
    integrate with ajour and profiles.
  - The Go marker in v1 is a stopgap for that declaration, not a pattern to
    extend.
- **Q4. Repair as the only hook: ruled (chat-e345c8ad): (a) stand aside
  now, with (c) as the end state.** The re-ask it was decided from:
  - **Re-asked Q4.** In §1b's terms, a repo whose only hook is repair runs
    no stage 3(a); its only generator, if any, is a codegen lane inside
    repair itself (stages 3b and 4). Options:
    - **(a) Stand aside (proposed).** Repair's edits to generated files are
      kept, since repair may be their generator. Cost: such a repo is not
      protected from its own formatter.
    - **(b) Always protect.** Repair can never change a marked file, so a
      repair-driven codegen lane can never update its output. Cost: that
      repo's generated files go stale until the generator runs elsewhere.
    - **(c) Protect, except files a lane inside repair declares it
      generates.** This is exact, but it needs the Q3 declarations, which
      do not exist yet. Until they do, it falls back to (a).

    Recommended: (a) now and (c) as the end state, which matches the Q3
    direction.
- **Q5. Record type: ruled:** an amendment to FDR 0018 (REPAIR), plus a line
  in the regenerate-phase plan.
- **Fleet audit (operator, same relay):** an issue for a fleet-wide audit
  and convergence to the conformist profiles (consistent, self-healing). The
  coordinator files it on circus.
- **A1 scope (chat-dc1645ce):** approved for both callers, the regenerate
  phase and pre-merge REPAIR.
- **A4 landing-rebase gap:** a spinclass issue, filed by the coordinator.
- **Design approval (C):** approved. The guard is built first, test-first,
  as its own merge, before step 0 of the host queue.

## 7. Sources

- ringmaster `docs/plans/2026-11-10-repair-and-generated-files.md` (R).
- `internal/charlie/regenphase/run.go`, `phase.go` (R).
- `internal/delta/merge/merge.go:421-431`, `:1777` (R).
- `docs/plans/2026-11-09-sc-run-regenerate-under-new-lock.md` and
  `docs/plans/2026-11-09-frozen-hook-vs-lock-bumps.md` (R, earlier spinclass
  notes on the phase and on #459).
- Coordinator brief chat-8d08daca (O).
