# The frozen pre-commit hook and automated lock bumps (design note, 2026-11-09)

A design note for circus/eager-holly. Nothing here is built. It weighs ways to
let an automated lock bump (the circus cascade, circus#423) commit correct
generated files while keeping what spinclass#459 bought: a branch cannot
change what formats its own commits, and the hook never evaluates the branch's
flake.

Labels: **O** observed in a run, **R** read in code or docs, **I** inferred
or proposed, **U** unknown, **C** reported by the coordinator and not
re-checked here.

## 1. What happens today

- **R.** At install, `resolveFrozenHook` (`internal/bravo/apply/frozenhook.go`)
  resolves the formatter's leading word to the store path the **landed default
  branch's** devShell provides, evaluated as `git+file://<repo>?rev=<base>`.
  The dispatcher (`precommit.go`, `dispatcherTemplateText`) runs that path on
  every commit and never evaluates the worktree flake.
- **R.** The drift check compares `git hash-object flake.lock` in the worktree
  with `lockHash`, which `installPreCommitHook` takes from the **worktree's**
  `flake.lock` at install (`flakeLockHash(worktreePath)`), not from the base the
  formatter was frozen from. On drift it prints `DriftMsg` and **still runs** the
  frozen formatter.
- **C.** The cascade's `bump-repo.bash` (circus) runs `nix flake update`, then
  `just codemod-generate`, stages what changed, and runs `git commit` in the
  repo's session worktree as the operator's user.
- **C.** In ringmaster the conformist `dewey-facade-export` lane has includes
  `["**/*.go","flake.lock"]` and `restage-repair-outputs = true`.
- **C, O by the coordinator in ringmaster/free-pine.** After
  `just codemod-generate` and `git add -A -- pkgs`, one file is staged; after
  `git hook run pre-commit`, nothing is staged and the tree is clean. The hook
  printed the drift line, a conformist eval-cache `timeout` warning (**U**
  whether it matters), and "reformatted and restaged 1 staged file(s)".
- **I.** Mechanism: the frozen conformist carries the base's dagnabit, so its
  repair of `pkgs/jobwake/main.go` writes the old stamp, which equals HEAD, so
  the restage empties the change. The bump commit holds only `flake.lock` and
  the pre-merge facade-drift check fails. The steps inside conformist were not
  traced (**U**).

### Two consequences the issue does not name

1. **I. Agents hit it too.** Any session that bumps its own lock and runs
   `codemod-generate` gets the same revert. It is not cascade-specific; the
   cascade is the first caller to do it routinely.
2. **R + I. The drift warning can go silent while the formatter is stale.**
   Because `lockHash` is the worktree's lock at install, a session resumed
   while its branch carries an unlanded lock bump bakes the *bumped* hash. The
   frozen formatter is still the base's, but no drift is reported, and every
   later `.go` commit re-stamps generated files with the old generator. The
   cascade's retry path (gate fails, session resumed, fix committed) is this
   case. The baseline the formatter was frozen from is `<base>:flake.lock`
   (`git rev-parse <BaseSha>:flake.lock` gives its blob hash, the same value
   `git hash-object` gives for identical content).

### Why lowering the lane's triggers does not help

**I.** Dropping `flake.lock` from the lane's includes leaves `**/*.go`, and the
bump commit stages `pkgs/jobwake/main.go`, so the lane still fires with the old
generator. The same holds for any later `.go` commit in a session whose lock has
drifted. **U**: whether the lane list is baked into the frozen conformist or
read from the worktree. Either way the stale generator is what runs.

## 2. The threat model this must keep

- **R (FDR 0035 F9).** In a confined unit `<worktree>/.spinclass/hooks` is
  mounted read-only, and `grit_commit` runs with `core.hooksPath=/dev/null` and
  `--no-verify`. The dispatcher never runs inside a unit.
- **I.** For an unconfined session the worktree, its `.spinclass/hooks`
  dispatcher included, is writable by the agent's uid. The freeze is therefore
  not a boundary against a hostile unconfined agent, which could rewrite the
  dispatcher. What it stops is **implicit** evaluation: a commit that someone
  else makes (the operator at the terminal, a merge-host script) evaluating a
  flake an agent wrote, with the committer's authority (#449 canary, #460
  store copies). Worth confirming with the operator (**U** whether that framing
  is the one intended).
- **I.** So the property to keep is: **nothing in the worktree may make the
  hook evaluate a flake, or run a binary, that the landed base did not
  provide.** A lock-only diff does not satisfy this on its own. The lock picks
  the formatter's store path, and that binary runs unsandboxed as whoever
  commits. A lock that names `github:someone/conformist` at a chosen rev, with
  a correct narHash, contains no `path:` input and still chooses the code.
- **R (EPIC 0001 principle 2).** Rights live in the signed grant chain, "not in
  a profile document, a config file, a hook, an env var or prose". A scheme
  where an env var or a worktree file *widens* what the hook does conflicts
  with this. A scheme that only *narrows* what the hook does (it does less) is
  not an authority question.

## 3. Options

### S0. The frozen formatter stands down on drift (recommended floor)

The dispatcher already knows when it is stale. Make it do less instead of
running stale:

- Baseline: bake the hash of `<BaseSha>:flake.lock` (the lock the formatter was
  frozen from), not the worktree's lock. When the hook is unfrozen, keep
  today's worktree hash.
- When the live `flake.lock` (worktree or index; both are cheap to hash)
  differs from that baseline, **skip the formatter** and print one line: the
  lock differs from the one the formatter was frozen from, formatting is
  deferred to the pre-merge gate, and `sc resume` after landing re-freezes.
  Native hooks still run and keep their exit codes.
- No evaluation, no new input, no identity.

Threat model: strictly narrower than today (I). A forged condition, such as an
agent editing the lock to suppress formatting, costs only formatting, which the
gate checks.

Cascade: the bump commit carries what `codemod-generate` wrote (I; to confirm
by replaying the free-pine repro against a patched dispatcher). Agents that
bump locks get the same fix. The silent-staleness case in section 1 is closed.

Cost: one spinclass change and a re-install on next resume. Nothing per repo.
Commits in a session whose lock has drifted go unformatted until landing plus
resume. The gate's lint catches formatting. **U**: whether any of the 20+ repos
relies on the hook to *produce* content the gate then demands (a lane whose
output is only written by the pre-commit repair and never by a recipe). Such a
repo would fail its gate on the next lock-drifted commit instead of silently
restamping. That is louder and arguably correct, but it changes behaviour. A
survey of `restage-repair-outputs` lanes against each repo's `codemod-*`
recipes would answer it. That is a question for each repo's own session.

### S1. The blessed caller formats with the authority it already has (recommended with S0)

The cascade already trusts the post-bump lock: it ran `nix flake update` and
then `codemod-generate`, which evaluates that lock and runs its tools as the
operator (C, I). It needs no right from spinclass to run the formatter under the
same lock before it commits. Two forms:

- Run the repo's own formatter recipe after `codemod-generate` (whatever the
  eng convention names, e.g. `codemod-fmt`; **U** whether every repo has one).
- Or `nix develop -c conformist-pre-commit` explicitly, then `git add`, then
  commit. With S0 the hook stands down, so nothing reverts.

Threat model: no change to spinclass. The authority is the caller's own
process, exercised explicitly, which fits EPIC 0001 principle 2. Cost: one
change in circus's `bump-repo.bash`. That is circus's lane, so it goes to the
circus session by way of the coordinator.

### S2. A per-invocation lock-only re-evaluation (not recommended)

`SPINCLASS_PRECOMMIT=lock-only` (or `git -c spinclass.precommit=lock-only`) on
the commit's own command line. The hook then checks that the diff from the base
is lock-only: `flake.nix` byte-identical to `<base>:flake.nix`, and the lock
passing the **guarded gate's** lock policy (`internal/charlie/check`), not just
`lockNamesLocalInput`. It builds a synthetic commit object (`git commit-tree`
of the base tree with the staged `flake.lock`, reachable from no ref),
evaluates `git+file://<repo>?rev=<synthetic>`, and runs that formatter for
this one commit only.

- Why it is not an escalation (I): the env var is set by the process that runs
  `git commit`, which already holds the committer's authority. Nothing
  persists.
- Why it is still not recommended: it widens what the hook does on the say-so
  of an env var (EPIC 0001 principle 2). It brings back commit-time evaluation,
  with its cost and its #460 store copies (bounded by the lock policy, but no
  longer zero). It needs the #464 review extended to unlanded locks. And S0
  plus S1 gives the cascade the same result with no new code path in the hook.
  The `git -c` form also leaks into persistent config if someone writes it
  there, and the hook cannot tell the difference.

### S3. A persistent re-freeze from a lock-only branch, behind a right (sketch, not now)

`sc rebuild --refreeze-from-branch-lock <session>`: the same lock-only checks
and synthetic-commit evaluation as S2, but the result is written as the
session's frozen hook and recorded in the **frontend** record (FDR 0035; the
worktree sidecar is display-only, R).

- It is an escalation if the session itself can reach it (I). An agent's lock
  would choose the binary the operator's later commits run. It must therefore
  be a FDR 0032 D13 object right (a new `refreeze`, off by default, never in
  the default grant, not held by the session over itself) or, later, an EPIC
  0001 signed grant, and never an MCP tool the session's own catalog carries
  (as with `--allow-stale-base`, FDR 0024).
- Value: a long-lived session that bumped its lock and wants formatting before
  landing. No caller needs this today. It is the same "re-provisioned by the
  parent" shape as #459 §2.3, so it fits if a need appears.

### Lower-tech comparisons

| Option | Fixes cascade bump | Fixes agent bump | Fixes silent staleness | New evaluation | Who changes | Notes |
|---|---|---|---|---|---|---|
| S0 stand down on drift | yes (I) | yes (I) | yes | none | spinclass | narrows only |
| S1 caller formats itself | with S0 | n/a | n/a | caller's own | circus | fits EPIC principle 2 |
| L1 cascade `git commit --no-verify` | yes (I) | no | no | none | circus | also skips native hooks; a stopgap until S0 lands |
| L2 drop `flake.lock` from the lane | no (I, `**/*.go` still matches) | no | no | none | each repo | per-repo churn for no fix |
| L3 `sc resume` before commit | no: base is still the old lock until landing | no | makes it worse (bakes the bumped hash, section 1) | base only | none | |
| L4 auto re-freeze for lock-only diffs | yes | yes | yes | per drift | spinclass | agent-reachable, so escalates to operator commits (section 2); rejected |
| S2 env opt-in | yes | if used | no | per commit | spinclass + circus | see above |
| S3 right-gated re-freeze | yes | if granted | yes | per grant | spinclass + FDR 0032 | not now |

## 4. Interactions

- **Guarded gate (#449, FDR 0035).** S0 and S1 leave it unchanged. It remains
  the check that the bump commit's generated files match the new lock. S2 and S3
  should reuse its lock policy verbatim, not a second allow-list (EPIC 0001
  principle 2, "a second place that decides what is allowed is a bug").
- **#464.** S0 adds no evaluation, so #464's question is unchanged. S2 and S3
  would add an unlanded-lock case to it.
- **#460.** S0 and S1 add no store copies from spinclass. S1's copies are the
  caller's own, made by `codemod-generate` already.
- **light-cedar's #459/#460 follow-ups and the direnv-less pilot.** Not
  touched. S0 edits the same dispatcher branch, so it should be sequenced with
  that session.

## 5. Recommendation

1. **S0** in spinclass: bake the base's lock hash, and stand the formatter
   down on drift. It is small, narrows behaviour, needs no identity, and fixes
   both the cascade and agent cases plus the silent-staleness gap. Verify by
   replaying the free-pine repro against the patched dispatcher (staged set
   unchanged after `git hook run pre-commit`) and by the existing #459 drift
   test rewritten for skip.
2. **S1** in circus (its session's call): format under the new lock
   explicitly in `bump-repo.bash` before committing. **L1** (`--no-verify`) is
   an acceptable stopgap until S0 is deployed, with the cost that native hooks
   are skipped on bump commits.
3. Do not build S2. Keep S3 as the shape to reach for if a session ever needs
   formatting from an unlanded lock, gated by a D13 right or an EPIC 0001 grant.

## 6. Open questions for the operator

1. Is the threat model in section 2 the intended one: the freeze guards
   against implicit evaluation in someone else's commit, not against a hostile
   unconfined agent that can rewrite the dispatcher?
2. Is "unformatted until landing" acceptable for lock-drifted commits, or
   should S0 run the formatter in a non-restaging, report-only mode? That mode
   depends on conformist offering one (**U**; conformist's lane).
3. Do any repos rely on the pre-commit repair as the *only* writer of
   gate-checked content? This is the S0 survey, one question per owning
   session.
4. The conformist eval-cache `timeout` warning in the repro: is it worth its
   own issue? S0 makes it irrelevant for bump commits, but not elsewhere (**U**).
