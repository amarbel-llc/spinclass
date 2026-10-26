# #402: REPAIR folds a concurrent edit to a committed path into the merge

2026-10-26, spinclass/firm-mulberry. Options for a decision; no fix landed.
Evidence labels: V verified, R reported, I inferred.

## Where it stands

- Already guarded (V, merge.go `AddedOrRemovedPaths`): an amend that adds or
  removes a path is undone and refused, so a file another writer creates
  during repair is no longer folded in.
- Still open (V, red test, 2026-10-26):
  `TestPrepareMergeDoesNotAmendAConcurrentEditToACommittedPath` (skipped,
  internal/delta/merge/repair_phase_test.go). Another writer edits a path the
  commit already has while repair runs. A repair that stages its run's changes
  folds that edit into the pinned commit, which the gate then tests and lands.
  The repair stub stages everything and amends, mirroring what conformist
  `--commit --amend` was observed to do on 2026-10-22 (R, the issue).
  conformist's own selection rule is conformist's lane.

The root cause is that repair runs in the live session worktree, where
anything else may be writing.

## Options

1. **Run repair in an isolated worktree.** Check out the pinned commit in a
   detached, private worktree, as the pre-merge gate already does (FDR 0013),
   run the repair command there, and take its amended commit as the new pin.
   Then move the session branch to it with a compare-and-swap (update-ref from
   the old pin). The session worktree is not touched by repair, so a
   concurrent writer's edits stay uncommitted where they were.
   Cost: the session worktree's files lag the branch by the repair's changes.
   The worktree then needs a careful update: `git read-tree -m -u` from the
   old pin to the new one updates only the files repair changed, and refuses
   if those same files have local edits. Changes FDR 0018 ("repair runs in the
   session worktree").
2. **Snapshot and compare.** Hash every tracked file before repair. After it,
   accept the amend only for paths whose worktree content changed when the
   repair command's own process wrote them. Not knowable from outside:
   spinclass cannot tell its child's writes from another process's. Rejected.
3. **Ask conformist to commit only what it formatted, from a snapshot it took
   at the start.** That closes it for conformist only, and it is conformist's
   lane.
4. **Document it as a limitation.** Keep FDR 0018's "the repair run is a
   window" note, and say that work should not be written into the session
   worktree while a merge's prefix runs. The status quo, made explicit.

## Recommendation

Option 1. It removes the window instead of narrowing it, and it reuses the
detached-worktree machinery the gate already has. The `read-tree -m -u` step
leaves a writer's edits alone and refuses rather than overwrite. Size: medium
(PrepareMerge's repair path, the worktree update, FDR 0018 text, and un-skip
the red test).
