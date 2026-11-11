# The frozen pre-commit hook and a branch that changes its own lane config (design note, 2026-11-11)

A design note for circus/eager-holly. Nothing here is built. The operator ruled
option (c) for ringmaster/cool-cypress's chicken-and-egg (the freeze takes the
lane config from the branch, not from master), "or an equivalent that lets a
change to the lane config be committed through the hook it changes". The
property to keep: a worker cannot weaken, without review, the hook that checks
its own commits. Follows `2026-11-09-frozen-hook-vs-lock-bumps.md` (S0 to S3),
whose S0 was not built.

**Status (operator, 2026-11-11, via circus/eager-holly):** neither C0 nor C1
is to be built yet.

- C0 is the **interim candidate**.
- The **intended replacement** is the igloo/godyn derivation-space direction:
  extend godyn so that dagnabit and `tommy-generate` output is generated at
  build time instead of committed. That would remove committed facades and the
  stale-generator-stamp class of problem altogether.
- The property in §2 is **relaxed** until there is a more thorough design. It
  is not binding.
- §5 Q3 ("unformatted until landing") is undecided.

Labels: **O** observed, **R** read in code or docs, **I** inferred or proposed,
**U** unknown, **C** reported by another session and not re-checked here.

## 1. Why the hook cannot see the branch's config today

- **R.** `conformist-pre-commit` is the devShell's
  `<eval>.config.build.preCommit`, a store-pinned wrapper around
  `conformist --staged --exit-zero-on-fix` with the generated config baked in
  as a store path (conformist `nix/module-options.nix`,
  `nix/mk-toolchain-hooks.nix`). The lane list, includes,
  `restage-repair-outputs`, and every tool binary (dagnabit among them) are
  part of that store path.
- **R.** The freeze (`resolveFrozenHook`) resolves that wrapper by evaluating
  the landed default branch's devShell at its sha. So the frozen hook carries
  master's lanes and tools until the session is re-frozen after master moves.
- **C (cool-cypress, chat-d26def50 via the coordinator).** Ringmaster's option B
  changes the `dewey-facade-export` lane's conformistConfig in `flake.nix`. A
  commit that carries it runs master's lane, which restages the raw facade.
  Option B's own facade-drift gate then refuses that commit.
- **I.** No flag or file in the worktree can change this, because the config is
  not read from the worktree. Taking the branch's config means evaluating the
  branch's flake, which is the evaluation #459 removed.

## 2. What the property actually protects

- **R (FDR 0019, #459; 11-09 note §2).** The freeze stops one thing: a commit
  (made by the operator at the terminal, or a merge-host script) running a
  binary or evaluating a flake that an agent's branch chose, with the
  committer's authority. Commit-time evaluation fetched the branch's inputs
  (the #449 canary leak, the #460 store copies).
- **R.** The pre-commit hook is not the check that lands code. The pre-merge
  gate is: an unconfined session's gate runs the repo's `just` in the build
  worktree under the **branch's** devShell, so it already uses the branch's
  conformist config (lint, codegen-drift checks). A confined session's gate is
  the trusted `[hooks].confined-pre-merge` (#449). The branch's own config is
  therefore already what judges an unconfined branch at landing.
- **I.** "A worker weakens its own hook" splits into two different harms:
  1. **Escalation.** The hook runs code the branch chose in someone else's
     commit. This is the real harm and must stay impossible without review.
  2. **Less formatting.** The hook formats less, or not at all. The gate's lint
     and drift checks still judge the result, so this costs convenience, not
     safety. The hook can already be skipped today with `--no-verify`, and
     `grit_commit` in a confined unit runs with hooks off (FDR 0035 F9).

So I recommend stating the property as: **the per-commit hook never runs code or
evaluates a flake that the landed base did not provide, unless the operator has
reviewed and approved the branch's flake inputs for this session. It may do
less on its own.**

## 3. Options

### C0. Stand down while the branch changes the flake (recommended, equivalent to (c) for this case)

The dispatcher bakes the freeze base sha. Before running the frozen formatter
it checks whether the commit being made changes anything the frozen formatter
was built from:

    git diff --cached --quiet <BaseSha> -- flake.nix flake.lock '*.nix'

(the index against the base, so both committed and staged changes count; git
only, no nix). If anything differs, it **skips the frozen formatter** and prints
one line: this branch changes the flake the hook was frozen from, so formatting
is deferred to the pre-merge gate, and a resume or restart after landing
re-freezes. Native hooks still run with their exit codes. This replaces today's
lock-hash drift warning, and the baseline becomes the base, not the worktree at
install (closing the 11-09 silent-staleness gap, §1 there).

- For cool-cypress: the option B commit carries `flake.nix`, so the hook
  stands down. The commit keeps the formatted export that `just facade-export`
  wrote. The gate runs option B's own lane, so its facade-drift check passes
  (I; to confirm by replaying cool-cypress's sequence against a patched
  dispatcher).
- Threat model: strictly narrower than today (it runs nothing new and evaluates
  nothing). Forging the condition (touching a `.nix` file) costs only
  formatting, which the gate checks.
- Cost: every later commit on that branch also goes unformatted until it lands
  and the session is re-frozen. The repair phase (FDR 0018) still formats at
  merge time under the branch's devShell, with #424's repair guard keeping
  generated files intact (I; this needs confirming per repo, the open question
  of the 11-09 note §6 Q3).
- `'*.nix'` rather than `flake.nix` alone, because a lane config can live in an
  imported file (spinclass's own `conformist.nix`). It over-triggers on any
  `.nix` edit. I think that is acceptable, since it only defers formatting.

### C1. Operator-reviewed re-freeze from the branch (literal (c), opt-in)

`sc rebuild --refreeze-from-branch SESSION`: CLI only, TTY required, never an
MCP tool (as with `sc restart --posture`, #473, and `--allow-stale-base`,
FDR 0024). It:

1. requires a clean tree and freezes from the session's **committed** HEAD
   (`git+file://<repo>?rev=<HEAD>`), never the working tree;
2. applies the #449 guarded-gate lock policy to HEAD's `flake.lock` (the same
   allow-list, not a second one; EPIC 0001 principle 2);
3. shows the operator `git diff <base>..HEAD -- flake.nix flake.lock '*.nix'`,
   which is everything the evaluation reads that differs from the reviewed
   base, and asks y/N;
4. evaluates, writes the hook, and records in the **frontend** record
   `BaseSha=HEAD`, `BaseLabel="<branch> (operator re-freeze)"`, and who
   approved it. C0's stand-down then compares against HEAD, so later commits
   that do not touch the flake again are formatted with the branch's lanes.

The next ordinary re-freeze (a resume or restart after master moves) returns to
master as before.

- Threat model: the escalation in §2 harm 1 happens only through a step the
  operator reviews, from the terminal. A worker cannot reach it (no MCP tool,
  TTY required). Its review has the same softness the operator ruled for #473:
  it trusts the terminal, not a signed act. If a signed EPIC 0001 grant is
  wanted later, this becomes the D13 `refreeze` right sketched as S3 in the
  11-09 note.
- Value: a long branch that changes lanes and wants formatting before it lands.
  Cool-cypress does not need it if C0 is in.

### Rejected

- **C2. Automatic re-freeze from the branch (literal (c), unreviewed).** The
  branch chooses what runs in the operator's later commits. That is harm 1, so
  it is rejected.
- **C3. Run both the base and the branch hook, and keep the stricter one.**
  It still runs the branch's code, so it is the same escalation. "Stricter" is
  also undefined for formatters.
- **C4. `--no-verify` for the one commit** (cool-cypress's (a)). It works as a
  stopgap, but it also skips native hooks and needs the committer to know when
  to use it.

## 4. Recommendation (superseded by the status above: hold)

1. Build **C0** now (spinclass, the dispatcher plus its baseline): bake
   `BaseSha`; stand down on a flake change against the base; keep native hooks;
   update FDR 0019, `spinclass-sweatfile(5)`, and the drift message. Tests:
   the dispatcher skips on a staged `flake.nix` change and on a committed one,
   runs on an unrelated change, and skips nothing when the hook is unfrozen.
   Then replay cool-cypress's sequence.
2. Hold **C1** until an operator ruling. Build it only if a branch needs
   lane-correct formatting before landing.
3. Deploy: like #475, none of this is live until the circus relock and flic
   deploy.

## 5. Questions for the operator

1. Is the property in §2 the intended one: the hook may do less on its own,
   but never runs branch-chosen code without the operator's review?
2. Is C0 an acceptable "equivalent" to (c) for cool-cypress, or do you want C1
   (or C1 alone) built now?
3. For C0: is "unformatted until landing" acceptable for every commit on a
   branch that changed a `.nix` file, given the gate and the repair phase still
   format and check?
