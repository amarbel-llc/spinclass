# #300: post-merge reads the landed commit's sweatfile, not the live worktree

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to
> implement this plan task-by-task. Tasks 2 and 3 both depend on Task 1 and are
> independent of each other; Task 4 (docs) goes last.

**Goal:** A merge's post-merge phase is a pure function of the commit that
landed plus the sweatfile layers that are not in the repo. The phase's inputs
are the `[[post-merge]]` targets, the legacy `[hooks].post-merge` string,
`post-merge-timeout` and `disable-post-merge`. Today they are resolved at
dispatch time from the session worktree's `sweatfile` on disk. An edit made
while an async merge's gate runs therefore deploys and verifies a commit it
was never part of. That is circus, 2026-09-15: a verify that called a recipe
the landed justfile did not have.

**Build/test:** scoped runs only, `just debug-go-test <dir> [run-regex]`
(`git add -N` new files first; godyn only sees tracked paths). Do NOT run full
`just`: the merge gate runs it.

**Rollback:** none (no knob). This is a bug fix that restores the documented pin
contract ("the merge lands exactly the commit it pinned; edits made meanwhile
are left for the next merge"). Revert the commits if needed.

---

## Decision

**(a) Which snapshot: the LANDED sha** (`landedSha` in `runPostMergePhase`: the
landing sha on the queued path, which is the pin rebased onto a moved tip when
the queue rebased it; the pin itself on the unqueued path).

- *Not the merge base.* FDR 0031 reads exemption predicates from the base
  because they *judge* the branch, and a branch must not vouch for itself.
  Post-merge does not judge the branch. It deploys and verifies what landed. A
  base-tree read would bring back #300's skew in the other direction: a change
  that edits a justfile recipe and the `verify` that calls it would run the
  OLD verify against the NEW code. There is also no trust gain. Once the
  commit has landed it is the default branch's own content, and the next merge
  would run it anyway. The branch already controls the gate's code and the
  code being deployed.
- *Not the pinned sha.* After a queue rebase (FDR 0022) the landing tree
  includes siblings' commits. Those commits may include sibling sweatfile
  edits, and the phase already runs *in* the landing worktree at the landing
  sha. A pinned-sha read would mix code from one tree with hook config from
  another, which is the exact skew #300 reports. "The code that landed is what
  the operator reviewed" means the landed tree.
- This is "the same trust class as FDR 0031" (roadmap) in the sense that
  matters: **a merge-scoped hook's definition comes from a pinned commit in the
  object store, never from a mutable worktree.** The two differ only in which
  commit.

**(b) How to read it: `git ls-tree` + `git cat-file blob <landedSha>:sweatfile`
as the worktree layer. `LoadHierarchy(home, repoPath)` supplies the global,
parent-dir and root-checkout layers, which stay live.** That is the same layer
shape FDR 0031 uses (it swaps only the worktree layer, for a base checkout).

- Rejected: *load from the `.land-*` worktree.* It exists only on the queued
  path when gitSync is set or a rebase happened. It does not exist on the
  unqueued path or on a local-only merge that was not rebased. The object-store
  read works on every path and needs no checkout.
- Rejected: *capture the hierarchy at pin time and thread it to FinishMerge.*
  That changes the `PrepareMerge`/`FinishMerge` signatures, the async closure
  and the queued-merge re-prepare, and it captures the wrong sha (the pin, not
  the landing). It is also still unsafe: `loadAndGate` reads the working-tree
  file, which can carry uncommitted edits (for example under
  `rebase.autoStash`).
- Result: **no signature changes** to `PrepareMerge`, `FinishMerge`,
  `ResolvedContext` or the async job meta (`.spinclass/job.json`). The only
  internal signature change is that `loadAndGate` loses its `postMergeTargets`
  parameter (Task 3).

**(c) Paths.** All land paths already funnel into `runPostMergePhase(…,
landedSha …)`: queued (`FinishMerge`), unqueued (`finishMergeUnqueued`,
`disable-merge-queue`), sync, async and stacked (FDR 0025 re-runs Prepare and
Finish). `sc run` (via `merge.Resolved`) and out-of-session `sc merge <target>`
are covered too, so one change fixes every surface. **Incidental fix:** when
teardown removed the session worktree (`sc run`, `sc merge <target>`), the
current code loads a *missing* worktree `sweatfile`, so the committed repo layer
silently vanished from post-merge. It now loads from the landed commit. `sc run
--post-merge` dynamic hooks come from the CLI, not the sweatfile, and are out of
scope.

**(d) Target validation** (`loadAndGate`: an unknown requested target fails
before anything lands) moves to just after `pinHead` in `PrepareMerge`. It
validates against the snapshot **at the pinned sha**, because the landing sha
does not exist yet. The remaining gap is a sibling landing between pin and
landing that removes a target. That is already handled: `runNamedPostMergeTargets`
re-selects against the landed snapshot and emits a warning node. Validation
still happens before landing. It now runs after the rebase and repair, which is
harmless: both touch only the session's own branch.

**(e) Out of scope: the pre-merge hook command.** The `[hooks].pre-merge` string,
`inactivity-timeout`, `disable-merge-build-worktree`, and FinishMerge's
`disable-merge-queue` and `runAttestationPolicy` gate-liveness reads all still
come from the live session hierarchy. The skew class is the same, but the fix
is not the same one-liner. With `disable-merge-build-worktree` the hook runs in
the session worktree at whatever it has checked out, so a snapshot read would
mismatch in the other direction. `sc check` has no pin at all. Task 4 files a
follow-up issue.

**Unreadable snapshot:** if the landed sweatfile, or any live layer, fails to
parse, the phase emits a `severity=warn` not-ok point labeled `post-merge
sweatfile (<shortsha>)`. Today it returns silently. The `post-merge ` prefix
means the async wake surfaces it (#259). The phase **never** falls back to the
live worktree.

## Non-goals

- Snapshotting the root-checkout layer (`<repoPath>/sweatfile`, the main
  checkout's working copy). Every hierarchy load reads it live today, FDR 0031
  included. It is not the session's tree, and changing the layer shape would
  break every `writeRepoSweatfile` fixture. Documented as a limitation instead:
  a stale root checkout can still contribute a target that the landed commit
  deleted without a name-only sentinel.
- The pre-merge hook, merge-queue knob and policy liveness reads (see (e)).
- Any new sweatfile knob, MCP parameter or rollback switch.

---

### Task 1: snapshot loader (`git.FileAtRev`, `sweatfileio.LoadHierarchyWithLayer`, `merge.loadCommitHierarchy`)

**Context:** spinclass (Go, module `code.linenisgreat.com/spinclass`) merges a
session worktree branch and then runs a post-merge phase configured by the
sweatfile hierarchy. Issue #300: that phase must read the repo `sweatfile` as
committed at the landed sha, not the live worktree file. This task adds only the
building blocks. Nothing calls them yet (Tasks 2 and 3 do). The hierarchy
today: `sweatfileio.LoadHierarchy(home, repoRoot)` (global
`$HOME/.config/spinclass/sweatfile` → parent dirs → `<repoRoot>/sweatfile`), and
`LoadWorktreeHierarchy(home, repoRoot, worktreeDir)` appends
`<worktreeDir>/sweatfile` as the highest-priority layer
(`internal/sweatfileio/hierarchy.go`).

**Files:**
- Modify: `internal/git/git.go` (add `FileAtRev` next to `CommitExists`, ~line 288)
- Test: `internal/git/git_test.go` (reuse `newRepo`, `mustRun`)
- Modify: `internal/sweatfileio/hierarchy.go` (add `LoadHierarchyWithLayer`; refactor `LoadWorktreeHierarchy` to delegate)
- Create: `internal/sweatfileio/hierarchy_test.go`
- Create: `internal/merge/snapshot.go` (`loadCommitHierarchy`)
- Create: `internal/merge/snapshot_test.go`

**Behaviour:**
1. `func FileAtRev(repoPath, rev, path string) (data []byte, found bool, err error)`:
   run `git ls-tree --name-only <rev> -- <path>` via `Run`. An error means a bad
   rev, so return it. Empty output means the path is absent at rev: return
   `nil, false, nil`. Otherwise run `git cat-file blob <rev>:<path>` via
   `RunStdin(repoPath, "", …)` (untrimmed bytes) and return `data, true, nil`.
   Doc comment: reads the committed blob only, never the working tree.
2. `func LoadHierarchyWithLayer(home, mainRepoRoot, label string, data []byte, found bool) (sweatfile.Hierarchy, error)`:
   `LoadHierarchy(home, mainRepoRoot)`, then `Parse(data)`. A parse error is
   returned only when `found`; when `!found` the layer is inert, like a missing
   file today. Append
   `sweatfile.LoadSource{Path: label, Found: found, File: sf}`, and
   `Merged = Merged.MergeWith(sf)` iff found. Refactor `LoadWorktreeHierarchy`
   to `os.ReadFile(<worktreeDir>/sweatfile)` (`fs.ErrNotExist` ⇒ `found=false`,
   nil data; any other read error is returned) and delegate with
   `label = <worktreeDir>/sweatfile`. Its signature and behaviour are
   unchanged.
3. In `internal/merge/snapshot.go`:
   `func loadCommitHierarchy(home, repoPath, sha string) (sweatfile.Hierarchy, error)`
   calls `git.FileAtRev(repoPath, sha, "sweatfile")` (wrap the error as
   `read sweatfile at <shortSha>: …`) and then
   `sweatfileio.LoadHierarchyWithLayer(home, repoPath, shortSha(sha)+":sweatfile", data, found)`.
   Doc comment: #300. The worktree layer is the committed blob at sha. The
   global, parent and root-checkout layers are live (the same shape as FDR
   0031's base-tree read). It never reads any worktree on disk.

**Failing tests first:**
- `TestFileAtRev` (git_test.go). In `newRepo`, write and commit `sweatfile`
  = "committed", then overwrite the working copy with "edited" (uncommitted).
  Assert `FileAtRev(repo,"HEAD","sweatfile")` = ("committed", true, nil).
  Assert `FileAtRev(repo,"HEAD","nope")` = (nil, false, nil). Assert
  `FileAtRev(repo,"no-such-rev","sweatfile")` returns an error.
- `TestLoadHierarchyWithLayerOverridesRepoLayer` (hierarchy_test.go). Set
  `HOME` to a tempdir and create repoDir under it. `<repoDir>/sweatfile`
  declares `[[post-merge]] name="krone" command="echo root"`. The layer bytes
  declare `name="krone" command="echo layer"`, found=true, label
  `"abc1234:sweatfile"`. Assert that
  `Merged.ActivePostMergeTargets()[0].Command == "echo layer"` and that the
  last `Sources` entry has `Path=="abc1234:sweatfile"` and `Found`.
- `TestLoadHierarchyWithLayerNotFoundIsInert`: same setup with nil data and
  found=false. Assert the command is `"echo root"` and the last source has
  `Found==false`.
- `TestLoadHierarchyWithLayerParseErrorWhenFound`: data `"[[post-merge]\nbroken"`
  with found=true returns a non-nil error.
- `TestLoadCommitHierarchyIgnoresUncommittedEdits` (snapshot_test.go). Use
  `setupPostMergeRepo(t,"feature")`, then
  `commitSweatfile(t, wtPath, "[[post-merge]]\nname = \"krone\"\ncommand = \"echo COMMITTED\"\n")`
  (helper in policy_test.go). Record `head := runGit(t, wtPath, "rev-parse", "HEAD")`,
  then overwrite `<wtPath>/sweatfile` with the same stanza saying `echo EDITED`.
  `loadCommitHierarchy(os.Getenv("HOME"), repoDir, head)` must yield command
  `echo COMMITTED`. For the parent of head (`head~1`, which has no sweatfile),
  it must yield no targets and no error.

**Implement**, then prove with:
`just debug-go-test internal/git TestFileAtRev`,
`just debug-go-test internal/sweatfileio`,
`just debug-go-test internal/merge TestLoadCommitHierarchy`.

---

### Task 2: post-merge phase reads the landed snapshot (depends on Task 1)

**Context:** spinclass (Go, `code.linenisgreat.com/spinclass`). Issue #300:
`runPostMergePhase` in `internal/merge/merge.go` (~line 1011) loads
`sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath)`, which is the LIVE
session worktree file at dispatch time. An edit made while an async merge's
gate runs is then executed by that merge's post-merge phase against the
earlier landed code. Task 1 added
`loadCommitHierarchy(home, repoPath, sha)` (internal/merge/snapshot.go), which
reads the repo sweatfile as committed at sha, with the global, parent and
root-checkout layers live. `runPostMergePhase` already receives `landedSha`:
the landing sha on the queued path (post-rebase), the pin on the unqueued
path.

**Files:**
- Modify: `internal/merge/merge.go` (`runPostMergePhase` only; update its doc comment)
- Modify: `internal/merge/post_merge_phase_test.go` (helper refactor)
- Create: `internal/merge/post_merge_snapshot_test.go`

**Behaviour:** In `runPostMergePhase`, replace the
`LoadWorktreeHierarchy(home, repoPath, wtPath)` call with
`loadCommitHierarchy(home, repoPath, landedSha)`.
- `home == ""` still returns silently.
- On a load error, emit `ts.NotOk("post-merge sweatfile ("+shortSha(landedSha)+")",
  map[string]any{"severity":"warn","message": fmt.Sprintf("post-merge skipped: sweatfile at %s unreadable: %v (the merge already landed — nothing was rolled back)", shortSha(landedSha), err)})`
  and return. Never fall back to the live worktree.
- A loaded hierarchy with the phase inactive returns silently, as today.
- Everything downstream is unchanged: `EffectiveTimeout(hierarchy.Merged)`,
  `ActivePostMergeTargets`, the legacy `hookrun.PostMergeWithCap`, `runDir`
  selection, and env.
- In the doc comment, state that the phase config is a pure function of
  landedSha plus the unversioned layers (#300), and why it uses the landed sha
  rather than the pin or the base.

**Test helpers (add first):**
- In post_merge_phase_test.go add `runFinishWithMidEdit(t, repoDir, wtPath,
  branch string, gitSync bool, pm PostMergeOptions, between func())
  ([]ndjsoncrap.Record, error)`, a copy of `runFinishOpts` that calls
  `between()` (when non-nil) after `PrepareMerge` succeeds and before
  `FinishMerge`. Make `runFinishOpts` delegate to it with `nil`.
- In post_merge_snapshot_test.go add `writeGlobalSweatfile(t, content)`, which
  writes `$HOME/.config/spinclass/sweatfile` (the shape of `writeGlobalSkills`
  in policy_test.go; `setupRepo` points HOME at the test root).
- Important fixture rule: when the session COMMITS a `sweatfile`, do NOT also
  `writeRepoSweatfile` (an untracked root `sweatfile` blocks the local
  fast-forward landing). Put non-snapshot knobs in the global layer instead.

**Failing tests first** (all in post_merge_snapshot_test.go; reuse
`setupPostMergeRepo`, `setupRepo`, `setupWorktree`, `commitSweatfile`,
`runGit`, `findNode`, `nodeNames`, `findTest`, `testRecords`, `testDescs`,
`diagString`, `decodeRecords`, `prepareRacedMerge`, `mockExecutor`):
1. `TestPostMergeNamedTargetReadsLandedSweatfileNotLiveEdit`. Setup:
   `setupPostMergeRepo(t,"feature")`; `commitSweatfile(t, wtPath,` a `krone`
   target with `command = "echo COMMITTED"`). Call `runFinishWithMidEdit(…,
   false, PostMergeOptions{}, between)`, where `between` overwrites
   `<wtPath>/sweatfile` with `echo EDITED`. This is the async-merge window.
   Assert `err == nil`, and that the `findNode(recs,"post-merge krone")`
   output contains `COMMITTED` and not `EDITED`.
2. `TestPostMergeLegacyHookAndTimeoutReadLandedSweatfile`. The committed
   sweatfile is `[hooks]` with
   `post-merge = 'echo COMMITTED $SPINCLASS_POST_MERGE_TIMEOUT > <file>'` and
   `post-merge-timeout = "7m"`. The mid-edit changes those to `EDITED` / `"3m"`.
   Build it with `fmt.Sprintf` and a `t.TempDir()` path. Use TOML literal
   (single-quoted) strings so `$` survives. Assert the file reads exactly
   `COMMITTED 7m0s` and the `post-merge feature` test point is ok.
3. `TestPostMergeUnqueuedPathReadsLandedSweatfile`: as test 1, plus
   `writeGlobalSweatfile(t, "[hooks]\ndisable-merge-queue = true\n")`. Assert
   the output is COMMITTED, and assert there is no
   `findTest(…,"merge queue")` / landing-fetch point (sanity that the unqueued
   path ran; mirror `TestPostMergeRunsOnUnqueuedPath`).
4. `TestPostMergeRebasedLandingReadsLandingShaSweatfile`. This test
   distinguishes landing from pin. `repoDir := setupRepo(t)`;
   `commitSweatfile(t, repoDir, krone "echo BASE")`;
   `wtPath := setupWorktree(t, repoDir, "feature-race")`; then
   `prepareRacedMerge(t, repoDir, wtPath, "feature-race", "a.txt", "a",
   "sweatfile", <krone "echo RACED">)`, so the race commit on main rewrites the
   sweatfile after the pin. Then call `FinishMerge(ctx, &mockExecutor{}, rep,
   ts, repoDir, wtPath, "feature-race", "main", pinnedSha, false, true, nil,
   PostMergeOptions{})`, `ts.Finish()`, and
   `recs := decodeRecords(t, buf.Bytes())`. Assert the krone node output
   contains `RACED` and not `BASE`. Both the pinned tree and the live worktree
   say BASE, so only a landing-sha read passes.
5. `TestPostMergeUnreadableLandedSweatfileWarns`. The session commits sweatfile
   `"[[post-merge]\nbroken"`. Run `runFinish(t, repoDir, wtPath, "feature",
   false)`. Assert `err == nil` (the merge lands; `landed(t, repoDir)`). Assert
   there is a NOT-ok test point with prefix `post-merge sweatfile` whose
   `severity` is `warn`. The pre-fix code returns silently, so there is no
   point.

Run tests 1, 2, 4 and 5 before implementing and confirm they fail. Test 3 fails
for the same reason. Then implement.

**Prove:** `just debug-go-test internal/merge 'TestPostMerge|TestNamed'`, then
`just debug-go-test internal/merge` to confirm that the existing `writeRepoSweatfile`
fixtures (root-checkout layer, still live) are unaffected.

---

### Task 3: validate the target selection against the pinned snapshot (depends on Task 1)

**Context:** spinclass (Go, `code.linenisgreat.com/spinclass`). FDR 0026: a
merge caller may name post-merge targets (`PostMergeOptions.Targets`: nil = all,
non-nil = exactly those names). An unknown name must fail the merge *before it
lands*. Today `loadAndGate` in `internal/merge/merge.go` (~line 250) does this
against the LIVE session worktree hierarchy, before the rebase. Issue #300
makes the post-merge phase read the landed commit's sweatfile. Validation
should therefore read a commit too. The landing sha does not exist yet at
validation time, so the pinned sha is used. Task 1 added
`loadCommitHierarchy(home, repoPath, sha)` (internal/merge/snapshot.go).

**Files:**
- Modify: `internal/merge/merge.go` (`PrepareMerge`, `loadAndGate`)
- Test: `internal/merge/named_targets_phase_test.go`

**Behaviour:**
- Remove the `postMergeTargets` parameter and its validation block from
  `loadAndGate`. It keeps the co-active point, the hierarchy load for repair,
  and the disable-merge gate. Update its doc comment.
- Add `validatePostMergeSelection(ts *crap.TestStream, repoPath, branch,
  pinnedSha string, requested []string) error`. A nil `requested` returns nil.
  Otherwise build `active` from
  `loadCommitHierarchy(home, repoPath, pinnedSha).Merged.ActivePostMergeTargets()`.
  An unresolvable home or a load error leaves `active` nil, the same degrade
  as today, so any non-empty selection fails. Run
  `selectPostMergeTargets(active, requested)`, and on error return
  `failStep(ts, "post-merge selection "+branch, selErr, "")`.
- At the end of `PrepareMerge`, replace `return pinHead(ts, wtPath, branch)`
  with: pin; on error return it; `if err := validatePostMergeSelection(ts,
  repoPath, branch, pinned, pm.Targets); err != nil { return "", err }`; then
  `return pinned, nil`. Comment: validated against the pinned commit (#300).
  A sibling landing that later removes the target is caught by
  `runNamedPostMergeTargets`'s defensive re-select (a warning node).

**Failing test first:** `TestPostMergeSelectionValidatesAgainstPinnedSweatfile`
in named_targets_phase_test.go. Set up with `repoDir := setupRepo(t)` and
`commitSweatfile(t, repoDir, "[[post-merge]]\nname = \"krone\"\ncommand = \"echo krone\"\n")`
(policy_test.go helper). Then `wtPath := setupWorktree(t, repoDir, "feature")`,
and write, add and commit `a.txt` in wtPath. Then, on main, rename the target:
`commitSweatfile(t, repoDir, "[[post-merge]]\nname = \"nikulin\"\ncommand = \"echo nikulin\"\n")`.
Record `mainBefore := runGit(t, repoDir, "rev-parse", "main")`. Call
`runFinishTargets(t, repoDir, wtPath, "feature", false, []string{"krone"})`.
Assert:
- `err != nil`;
- main is unmoved (`== mainBefore`);
- there is a failing `findTest(testRecords(recs), "post-merge selection")`
  point whose `diagString(tr.Diagnostic,"message")` contains `krone` and
  `nikulin` (the declared set).

Why it is red today: the live, pre-rebase worktree still declares `krone`, so
validation passes and the merge lands. The pinned (post-rebase) commit declares
only `nikulin`. `TestPostMergeSelectionUnknownFailsPreLanding` must keep
passing. It asserts by prefix, so the point moving after `rebase` is fine.

**Prove:** `just debug-go-test internal/merge 'TestPostMergeSelection'`, then
`just debug-go-test internal/merge` to check the repair-phase and co-active
tests that also go through `loadAndGate`.

---

### Task 4: docs, and a follow-up issue (after Tasks 2–3)

**Context:** spinclass (Go, `code.linenisgreat.com/spinclass`). Tasks 1–3 made
the post-merge phase read its config from the landed commit's `sweatfile`
(`merge.loadCommitHierarchy`: committed blob as the worktree layer, with the
global, parent-dir and root-checkout layers live). Target-selection validation
now reads the pinned commit. Issue #300. No behaviour changes in this task.

**Steps:**
0. File a follow-up issue with the `eng:file-issue` skill (dedupe first). Title:
   "pre-merge gate config (hook string, inactivity-timeout,
   disable-merge-build-worktree, disable-merge-queue, policy liveness) still
   read from the live session worktree". Body: the same skew class as #300.
   Name the live reads: `runPreMergeHookContext`, the `sessionH` load in
   `FinishMerge`, and `loadAndGate`'s disable-merge gate. Note why it was not
   the same one-liner: with `disable-merge-build-worktree` the hook runs in the
   session worktree at whatever it has checked out, and `sc check` has no pin.
   Record the issue number for steps 2–3.
1. `AGENTS.md` (CLAUDE.md is a symlink to it). **Hard cap 40000 bytes. It sits
   at ~39989.** In the **`post-merge` hook** bullet, after "All land paths fire
   it; `sc check` never does; `disable-post-merge` opts out.", add:
   "Its config is read at the LANDED sha (#300, `merge.loadCommitHierarchy`:
   the committed `sweatfile` is the worktree layer, while global, parent and
   root-checkout layers stay live), never from the session worktree; target
   selection validates at the pin." To pay for it, delete the sentence in the
   **Named post-merge targets + verify** bullet that begins "Code:
   `sweatfile.PostMergeTarget`/`ActivePostMergeTargets`/" and ends
   "`validate.CheckPostMergeTargets`." (~290 bytes; the symbols are
   greppable). Confirm the final size is ≤ 40000 with `folio_ls` flags `-la`.
2. `doc/spinclass-sweatfile.5.scd`. In the `*post-merge*` entry (~line 248–265)
   and the `## [[post-merge]]` section (~line 1013–1082), add a short
   *Snapshot:* paragraph. The phase reads `[hooks].post-merge`,
   `post-merge-timeout`, `disable-post-merge` and `[[post-merge]]` from the
   repo `sweatfile` *as committed at the landed sha*, merged over the live
   global, parent-dir and main-checkout layers. Uncommitted edits in the
   session worktree, and commits made after the merge pinned, never affect a
   merge already in flight; they apply to the next merge. An unreadable
   snapshot emits a warn point and skips the phase. `*--post-merge-targets*`
   is validated against the pinned commit. Keep the scdoc syntax consistent
   with the neighbouring entries.
3. FDRs. `docs/features/0023-post-merge-hook.md` → `## Limitations`: add a
   bullet saying the hook config is the landed commit's (#300). The bullet
   should also cover the remaining live layer: the main checkout's working
   copy can still contribute a stale entry, so a target *deleted* in the
   landed commit (rather than removed with a name-only sentinel) survives if
   the main checkout still has it. It should also say that pre-merge gate
   config is still live, citing the step-0 issue.
   `docs/features/0026-named-post-merge-targets.md` → `## Limitations`: add a
   bullet on selection validation. It validates at the pin, and a sibling
   landing that removes a selected target between pin and landing surfaces
   only as a warning node ("post-merge selection"), not a pre-landing failure.
   Update neither FDR's status.

**Verify:** no code changes. Confirm the AGENTS.md size and that the manpage
still renders (the merge gate's build runs scdoc). Do not run `just`.
