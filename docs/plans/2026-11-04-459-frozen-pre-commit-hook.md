# Freezing the per-commit hook at session start (plan for #459, 2026-11-04)

Plan, no code. The operator decided the shape on 2026-11-04 in conversation and
on the same day gave a go to write this plan and the FDR 0019 amendment, not to
implement: implementation waits for a second go after the operator has read
them. The companion decision records are FDR 0035 ("The environment is part of
the required view") and `2026-11-04-confined-direnv-less-pilot.md`.

Labels: **R** read in code or docs, **V** observed in a run, **I** inferred or
proposed, **U** unverified.

## 1. The facts (R, V)

- The per-session pre-commit dispatcher (`internal/bravo/apply/precommit.go`,
  installed at `sc start` and re-installed on every `sc resume`) runs the
  configured formatter (`[hooks].pre-commit`, canonically
  `conformist-pre-commit`) on every `git commit` in the session worktree. The
  command is a bare name resolved from the committing shell's PATH, the
  devShell's by convention (FDR 0019).
- When `flake.lock` exists and `git hash-object flake.lock` differs from the
  hash baked into the dispatcher at install time, the dispatcher runs
  `nix develop --command sh -c <formatter>` in the session worktree, as the
  committing user, so a stale devShell cannot restamp generated files (#267).
- `nix develop` evaluates the worktree flake as it stands on the branch. An
  input the lock does not hold is fetched first, into the world-readable
  store; a `path:` input reaches anything the committing user can read. The
  #449 canary proved it with a fresh marker (V, spinclass#459): the commit with
  `--no-verify` wrote nothing, the guarded gate refused before any command, the
  hook alone wrote the flake tree copy and the marker.
- The guarded gate (#449, FDR 0035) evaluates the pinned commit in a scrubbed
  build worktree behind the lock policy and refuses an unlocked input by name.
  The hook and the gate disagree about what a worktree flake may do.
- The hook fails open ("committing anyway"): a formatter or evaluation failure
  costs nothing, and by then the fetch has happened.
- A confined worker's `grit_commit` runs with `core.hooksPath=/dev/null` and
  `--no-verify` (`internal/alfa/store/git/confined.go`, R), so the dispatcher
  never fires inside a unit. The exposure is unconfined sessions and the
  operator's own commits in any worktree, the canary's case.

## 2. The decision (operator, 2026-11-04)

The hook is defined when the session is provisioned, cannot change afterwards,
and follows the provenance chain of the session rather than the branch:

1. **Frozen to a store path, resolved from the landed base.** At `sc start`,
   `sc spawn` and `sc resurrect`, spinclass resolves the formatter command to
   an absolute store path by evaluating the devShell of the **landed default
   branch** (the base the session is cut from, FDR 0024), never the branch the
   session will go on to commit. That path is baked into the dispatcher in
   place of the bare name, and recorded on the session.
2. **No evaluation at commit time.** The dispatcher never runs `nix develop`.
   A `flake.lock` that differs from the baked hash prints a warning naming the
   drift and the way out (below); the frozen formatter still runs. The lock
   check becomes advisory, which is what #267 could afford once the formatter
   is a fixed path: a stale formatter is now a visible warning, not a silent
   restamp and not a fetch.
3. **Re-provisioned by the parent, not repaired by the session.** The frozen
   path is refreshed when the session is resumed (`sc resume`) or restarted
   (FDR 0037), both of which re-run the install from the then-landed base. A
   broken or stale hook is fixed by whoever holds the session (the operator at
   the terminal, the spawner through `restart-child-session`), the same shape
   as a confined unit's spec: the session cannot re-point its own hook.
4. **Fail-open stays.** A missing or failing formatter never blocks a commit;
   the loud banner stays. A commit on a flake mid-edit must not be blocked by
   its own formatter.

Why the base and not the branch: the operator's question ("why would the base
eval at start be an issue? we were eval'ing it at every commit before, and the
start already does a devShell eval") settles the cost; the point is *whose*
flake. Evaluating the landed base means the formatter's provenance is the
trusted layers plus what has already passed the gate, so a branch cannot change
what formats its own commits, and an unlocked input on the branch is never
fetched by the hook. The gate (#449) and the hook then agree.

## 3. What is built (I, for the second go)

- **apply:** `installPreCommitHook` gains a resolution step: given the base
  (the fetched remote default's commit, FDR 0024, or the local default under
  its documented caveats), evaluate its devShell and read the formatter's
  absolute path (`command -v <bare name>` inside `nix develop <base-ref>
  --command`, or the devShell's `PATH` entry that provides it). The dispatcher
  template's `Bin`/`Cmd` carry the absolute path; the `via_nix` branch and
  `command -v nix` guard go; the lock-hash branch prints the warning only.
- **Resolution target.** The base is addressed as a git ref of the repo, not
  as the main checkout's working tree (which may be dirty or behind), so the
  evaluation reads committed, landed content: `git+file://<repo>?rev=<base
  sha>` (I; the exact form and the `--no-update-lock-file` posture are to
  check against the guarded gate's lock policy, which should admit the same
  references).
- **Session record.** `session.State` records the frozen hook path and the
  base sha it was resolved from; the setup fingerprint
  (`internal/alfa/setupfingerprint`) includes them so `sc rebuild --check`
  and `sc list` can say "hook frozen at <sha>, base now <sha>". This closes
  FDR 0019's "Relationship to sc rebuild" limitation: a conformist bump is
  now visible to the fingerprint.
- **Resume and restart.** `sc resume` and the restart path re-run the
  install, so they re-resolve from the then-landed base and rewrite the
  dispatcher; the warning on a resumed session then clears.
- **The warning text** names the drift (`flake.lock changed since the hook was
  frozen at <base sha>`), what still runs (the frozen formatter), and the way
  out (`sc resume`, or `restart-this-session` under clown) in one line on
  stderr.
- **No change for confined commits** (hooks already bypassed) and none for the
  guarded gate.

## 4. Open questions for the second go

- **Evaluation cost at start.** One devShell evaluation of the base per
  session start, spawn, resume and restart, where today there is one at start
  (direnv's) and one per commit after a lock change. Expected a wash or a win;
  to measure on the first build (U).
- **Hosts without nix in the frontend's PATH.** Today the hook silently skips
  the re-eval; frozen resolution would fail at install. Proposal: fall back to
  the bare name with a warning, as today (fail-open at install too), and record
  "unfrozen" on the session so `sc list` shows it.
- **The base ref form** and whether the pinned-base evaluation itself needs the
  lock policy (#449) in front of it: the base has landed, so its lock has
  passed a gate, but a repo whose default branch never ran a guarded merge has
  no such guarantee. Decide whether the resolution runs behind the same policy
  (I: yes, it is one more evaluation of a flake spinclass did not write).
- **Legacy sessions.** Sessions created before the change keep their old
  dispatcher until resumed; a sweep is not needed because every resume
  rewrites it. Say so in the FDR 0019 amendment.
- **Testing.** Dispatcher template tests for the absolute path and the
  warning-only lock branch; an apply test that a resolution failure leaves the
  bare name with the warning; the #459 probe replayed on the first build (a
  commit that changes the lock and declares an unlocked `path:` input writes
  no store path).

## 5. Sequence (recorded in the arc, `_tags.org`)

Frozen hook (this plan) first, then C (#429, the merge-admission rulings note,
`2026-11-04-429-merge-admission-rulings.md`) plus B's test-harness fix, then
B's canary, then B ships. Implementation of this plan waits for the operator's
second go after reading it and the FDR 0019 amendment.
