# Repair must not rewrite generated files: the spinclass piece (2026-11-10)

**Status:** proposal, no code. This is spinclass's part of ringmaster's
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
  - **I:** whenever the generator and the formatter disagree on a file's
    bytes, every round changes that file, so the loop runs out at 3 with
    repair's bytes in the commit. Repair always has the last word.
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
- **Layers:** `protect` and `fail` only narrow what repair may do, so the key
  is read from every layer (I: the narrowing-only stance used elsewhere,
  e.g. FDR 0035). Whether `allow` should be accepted from a repo layer is
  Q2.

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
  or files marked only by path). An optional
  `[hooks].generated-paths = [...]` glob list could extend it later; Q3.

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

## 6. Open questions for the operator

- **Q1.** Default `protect` (recommended: self-healing, fleet-wide, no
  per-repo edit), or `fail` (the ringmaster note's "detection" only)?
- **Q2.** May a repository layer set `allow` (widening its repair back to
  today's behaviour), or is `allow` trusted-layers-only?
- **Q3.** v1 recognizes the Go marker only. Is a `generated-paths` glob list
  wanted for non-Go generated files now, or later?
- **Q4.** When repair is the only hook, should the guard still protect
  generated files (that would break repair-driven codegen), or stand aside as
  proposed?
- **Q5.** Record type: a short amendment to FDR 0018 (REPAIR) plus a line in
  the regenerate-phase plan, recommended since it changes both repair
  callers; or a new FDR?

## 7. Sources

- ringmaster `docs/plans/2026-11-10-repair-and-generated-files.md` (R).
- `internal/charlie/regenphase/run.go`, `phase.go` (R).
- `internal/delta/merge/merge.go:421-431`, `:1777` (R).
- `docs/plans/2026-11-09-sc-run-regenerate-under-new-lock.md` and
  `docs/plans/2026-11-09-frozen-hook-vs-lock-bumps.md` (R, earlier spinclass
  notes on the phase and on #459).
- Coordinator brief chat-8d08daca (O).
