---
status: proposed
date: 2026-06-15
promotion-criteria: |
  proposed -> experimental: implementation lands behind an opt-in
  `[hooks].repair` knob (unset = today's behavior, no repair phase), with a
  real `merge-this-session` on a repo whose `[hooks].repair =
  "conformist --commit --amend"` where (1) an unformatted-but-committed tree
  is auto-formatted and the fix folded into the merged commit, (2) the
  default branch lands the formatted sha, (3) the session branch and the
  agent's working tree reflect the amended commit with no surprise dirty
  state, and (4) a repair command that fails for real (exit 1/2) aborts the
  merge with a legible diagnostic.
  experimental -> testing: ~1 week of repair-enabled merges across at least
  two repos with no lost agent work, no rebase divergence on the *next*
  merge of a repaired branch, and the GPG-signing failure mode (locked
  agent) surfacing as a clean "repair could not sign" verdict rather than a
  wedged merge.
---

# Pre-merge REPAIR phase (conformist --commit --amend)

> Cross-repo collaboration: the conformist side (`--commit --amend`, #24/#33)
> is built by `conformist/merry-pine`; this FDR is the spinclass side, drafted
> for that agent's review before implementation. The **Open questions** section
> collects the contract decisions that need merry-pine's sign-off.

## Problem Statement

The `[hooks].pre-merge` command is a **verify** gate: it runs the project's
checks (`just` = build + test + bats + analyzers) against the committed tree
and either passes or blocks the merge. When the only thing wrong is mechanical
— a file the agent forgot to run `just fmt` on, a stray import, a TOML key out
of order — the merge fails, the agent context-switches back to run the
formatter, amends, and re-merges. That round-trip is pure overhead: the fix is
deterministic and the tool to apply it (`conformist`) already exists.

conformist now ships a **repair** mode for exactly this loop. `conformist
--commit --amend` formats/repairs the tree in place and folds the run's fixes
into `HEAD` via `git commit --amend --no-edit`, committing exactly the files
the run changed (`cmd/format/commit.go`). Its exit-code contract is
purpose-built for a pre-merge hook (`cmd/check.go` `ExitCode`):

| exit | meaning |
|------|---------|
| 0 | tree was already conformant — nothing repaired |
| 3 | `ErrFixesCommitted` — fixes were applied and folded into HEAD |
| 1 | findings / other errors |
| 2 | `ErrCommitRefused` — declined (outside a worktree, dirty tree w/o `--allow-dirty`, HEAD already pushed, nothing to amend) |

conformist's own design note calls out the target: *"optimized for the
pre-merge-hook case, where the tree is clean by construction."* That clean
tree is precisely what spinclass's merge flow produces. What is missing is the
spinclass-side **phase** that runs repair at the right point in the merge
sequence, threads the amended commit into the thing actually merged, and maps
the `0`/`3`/`1`/`2` contract onto continue-vs-abort.

## Interface

A new opt-in sweatfile knob on `[hooks]`:

```toml
[hooks]
repair    = "conformist --commit --amend"   # the REPAIR phase command
pre-merge = "just"                            # the existing VERIFY gate
```

- `repair` (scalar, sweatfile-cascade override; nil/empty = **disabled**, the
  default — today's behavior is unchanged). When set, the merge runs the
  repair command as a distinct phase **before** the verify hook.
- Optional `disable-repair` (bool) for the same opt-out shape as
  `disable-merge` / `disable-nix-gc`, so a child sweatfile can suppress an
  inherited `repair` without clearing the string.

Phase ordering inside a merge becomes:

```
rebase → REPAIR (new) → re-pin → VERIFY hook → ff-only merge → push
```

REPAIR is a merge-only phase. It runs for `sc merge` /
`merge-this-session`(`-async`). It does **not** run for `sc check` /
`check-this-session` (check verifies, it does not commit) — see Open
questions. v1 scopes repair to **worktree sessions** (the rebase path);
implicit/main-checkout sessions are out of scope (their HEAD may be pushed,
which conformist's amend refuses by design) — mirroring the implicit-session
gap already noted for check (#132).

The repair phase emits its own ndjson-crap test point so the verdict stream
shows what happened:

- exit 0 → `ok  repair <branch> (already conformant)`
- exit 3 → `ok  repair <branch> (amended <shortsha>: N file(s))`
- exit 1/2 → `not ok  repair <branch>` with the command's stderr as the
  diagnostic, and the merge aborts.

## Design

### Where REPAIR runs, and why it is NOT in the build worktree

The obvious placement — run repair inside the detached **build worktree**
(FDR 0013) alongside the verify hook — is wrong, and the reason is the load-
bearing design decision here.

The build worktree is a detached `HEAD` pinned to `pinnedSha`, and
`FinishMerge` fast-forwards **that exact sha** (`git merge --ff-only
pinnedSha`). If repair amends inside the build worktree it produces a new
`repairedSha` that lives *only* in a directory we delete on cleanup; the merge
still lands the original unformatted `pinnedSha`, and the fix is thrown away.
Threading `repairedSha` out and merging it instead is possible — its parent is
the same default-branch tip, so ff-only still applies — but it strands the
**session branch** at the unformatted `pinnedSha`. For the dominant
spinclass-worker flow (`inSession`, branch kept across merges, FDR-per-CLAUDE.md)
the next merge then rebases the unformatted `pinnedSha` onto a default branch
that already contains its formatted equivalent → patch-id can't dedup
(different diffs) → **replay conflict**. Keeping the session branch in sync
would mean force-resetting the agent's working tree, which is exactly the
mutation FDR 0013 exists to avoid.

So REPAIR runs in the **session worktree**, inside the synchronous
`PrepareMerge` prefix, right after the rebase and **before** the pin:

1. `rebase branch onto defaultBranch` (existing) — leaves wtPath clean, so
   conformist's default refuse-on-dirty policy is satisfied with no
   `--allow-dirty`.
2. **REPAIR**: record `HEAD` (`sha0`), run the repair command in wtPath,
   record `HEAD` again (`sha1`).
   - `sha1 != sha0` → repair amended the branch HEAD in place. `git commit
     --amend` moves the branch ref **and** leaves the working tree clean
     together, so there is no divergence and no surprise dirty state.
   - `sha1 == sha0` → no-op (exit 0), continue unchanged.
3. **Pin** `sha1` as `pinnedSha` (existing pin step, now reading the
   post-repair HEAD).
4. VERIFY hook runs in the build worktree detached at `pinnedSha` (= the
   repaired sha) — so we verify exactly what we merge.
5. `git merge --ff-only pinnedSha` lands the repaired commit on the default
   branch.

Because `PrepareMerge` already mutates wtPath (the rebase) and already runs
synchronously before `merge-this-session-async` returns its job id, slotting
repair in here adds no new concurrency hazard: the agent has not regained
control, so nothing can race the amend. The freeze window FDR 0013 shrank from
"the whole hook" to "the rebase" grows to "the rebase + the format" — and
format is seconds, not the minutes a full build/test hook costs, which still
runs detached.

### Exit-code mapping (continue vs abort)

spinclass cannot treat the repair command like a generic pre-merge hook (any
nonzero = fail): conformist returns **3 on success-with-fixes**. The proposed
contract for the repair phase:

- `0` → ok, nothing repaired, continue.
- `3` → ok, repaired, continue (and the HEAD-sha delta confirms the amend).
- anything else → fail, abort the merge, surface stderr.

The HEAD-sha delta (step 2) is the tool-agnostic "did it change anything"
signal; the exit code only gates ok-vs-fail. Accepting `{0, 3}` as "ok"
documents a dependency on conformist's convention — see Open questions for
whether to hardcode it, make the success set configurable, or push a
plain-`0`-on-fix flag down into conformist.

### What a failed VERIFY leaves behind

A new, deliberate behavior change: because repair amends in `PrepareMerge`,
**if the later VERIFY hook fails, the session branch keeps the repaired
(amended) commit.** Previously a failed merge left the branch byte-for-byte
as the agent left it. Now a repair-enabled failed merge leaves the branch with
formatting folded into HEAD. This is judged a strict improvement (the fix is
real and persists; the agent addresses the genuine failure and re-merges onto
an already-formatted commit) and is cheap to reason about, but it MUST be
documented — an agent that `git reset`s expecting its exact original commit
will be surprised.

## Examples

```toml
# Opt in: auto-format-and-fold before every merge's verify gate.
[hooks]
repair    = "conformist --commit --amend"
pre-merge = "just"
```

```text
# merge verdict stream (repair found and folded a formatting fix)
ok   rebase prime-pine
ok   repair prime-pine (amended a1b2c3d: 2 file(s))
ok   pre-merge hook for prime-pine: `just`
ok   merge prime-pine
ok   push
```

```text
# repair declined to run — merge aborts before the expensive verify hook
ok       rebase prime-pine
not ok   repair prime-pine
  ---
  severity: fail
  message: "refusing to format and commit: HEAD is already pushed (origin/prime-pine)"
  exit_code: 2
  ...
```

## Limitations

- **GPG signing.** `git commit --amend` re-signs HEAD. Per repo policy commits
  are signed via the piggy/PIV agent; if it is locked the amend fails (exit 2,
  git's stderr), repair fails, and the merge aborts with a legible "repair
  could not sign" diagnostic rather than committing unsigned. Same constraint
  as any commit, but now on the merge path — must be surfaced clearly so the
  agent knows to ask the user to unlock the agent (per CLAUDE.md, never strip
  the signature).
- **Implicit / main-checkout sessions (FDR 0014).** Their HEAD is on the
  default branch and may already be pushed; conformist's amend refuses pushed
  history. v1 does not run repair for implicit sessions (out of scope, like
  check/#132). A future variant could use `--commit` (fresh
  `chore: conformist fmt+fix` commit) instead of `--amend` there.
- **Multi-commit branches.** `--amend` folds fixes into the **top** commit
  only. The formatting is correct tree-wide, but it lands as one chore-fold on
  HEAD rather than distributed per-commit — fine for the squash-style worker
  flow, slightly odd for a curated multi-commit branch.
- **Tool-agnostic in name, conformist-shaped in contract.** `[hooks].repair`
  is a generic command string, but the `{0,3}` success mapping encodes
  conformist's exit-code convention. A repair tool that signals differently
  would need the configurable-success-codes escape hatch (Open questions).
- **Not a verify replacement.** Repair only fixes what its tool can
  mechanically fix. The VERIFY hook still runs and still gates; repair just
  removes the mechanical-failure round-trip ahead of it.

## Open questions (for conformist/merry-pine review)

1. **Exit-code contract.** Hardcode `{0,3}` as the repair-phase "ok" set
   (couples spinclass to conformist's convention), add a
   `[hooks].repair-success-codes = [0, 3]` knob (flexible, fiddly), or have
   conformist grow a flag (`--exit-zero-on-fix`?) so spinclass can use plain
   "0 = ok" semantics and stay convention-free? The last pushes the bridging
   into the tool that owns the convention — likely cleanest cross-repo.
2. **Trailers / attribution.** conformist's `--commit` supports `--trailer`
   (#26). Should the spinclass repair phase inject a standard attribution
   trailer (e.g. a Clown sign-off) on the amend, or leave trailers to the
   sweatfile command string?
3. **`sc check` behavior.** Should `check` run repair at all? Options: (a)
   skip it (merge-only, this FDR's default); (b) run conformist in
   check-only mode (no `--commit`) so check *reports* would-be repairs
   without amending; (c) run the full amend (rewrites the agent's HEAD
   outside a merge — likely surprising). Leaning (a) or (b).
4. **`--allow-dirty`.** The post-rebase tree is clean by construction, so v1
   runs repair with conformist's default refuse-on-dirty. Is there a flow
   where we'd want `--allow-dirty` on the merge path? (Probably not — dirty
   wtPath would have failed the rebase first.)

## More Information

- Task origin: chat from `conformist/merry-pine`, 2026-06-15.
- conformist: `cmd/format/commit.go` (`RunCommit`, `commitPreflight`,
  `amendPreflight`), `cmd/check.go` (`ExitCode`), conformist #24 (`--commit`)
  and #33 (`--amend`).
- spinclass: `internal/merge/merge.go` (`PrepareMerge`/`FinishMerge` — repair
  slots into `PrepareMerge` after rebase, before the pin),
  `internal/check/check.go` (build-worktree lifecycle, untouched),
  `internal/sweatfile/sweatfile.go` (`Hooks` struct — new `repair` /
  `disable-repair` fields).
- Related FDRs: 0013 (isolated build worktree — why repair is NOT in it),
  0007 (pre-merge skill attestation), 0014 (implicit sessions — out of scope).
- `spinclass-sweatfile(5)` `[hooks]` § would gain `repair` / `disable-repair`.
