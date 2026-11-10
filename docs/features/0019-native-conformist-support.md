---
status: testing
date: 2026-06-20
promotion-criteria: |
  proposed -> experimental: DONE — conformist shipped `lib.mkToolchainHooks`
  (conformist#59, master @ 8ac5fce); spinclass wired it in flake.nix (commit
  3e4c210) and switched its sweatfile to `pre-commit = "conformist-pre-commit"`,
  proven byte-identical (formatting "0 changed") + a live staged-format hook run.
  experimental -> testing: the wired hook survives a conformist bump (flake.lock
  update + devShell reload) with no version/formatter-PATH/flag breakage, ~1 week.
---

# Native conformist support in spinclass

> **Testing** (promoted 2026-09-28): the hook has survived repeated conformist
> bumps with no version, formatter-PATH or flag breakage: ab34d67, 9ef6631,
> 31ba73e and fac9139. The last one also brought the generic codegen-repair
> lane, which restamped a stale tommy header at pre-commit. Watch item: #311.
>
> Earlier status (experimental): the conformist side (conformist#59: `mkToolchainHooks`,
> scaffold `repair`, the two-shapes guide) is merged, and the spinclass adoption
> landed (commit `3e4c210`): `flake.nix` uses `conformist.lib.mkToolchainHooks`,
> the sweatfile names `conformist-pre-commit`, proven end-to-end (the live hook
> reformatted + restaged a staged file hermetically via the wrapper's baked
> toolchain). Settled with conformist/live-alder. Two earlier drafts were
> wrong-headed: (1) pinning the bare `conformist` binary + rewriting the flag
> string (reinvents what conformist ships); (2) migrating spinclass to
> conformist's **Nix module**. spinclass is a **TOML consumer** (hand-written
> `conformist.toml`), so the path is `lib.mkToolchainHooks` — ~3 lines, no module
> port, no eng-preset tax.
>
> **Known post-adoption nuance (filed):** `sc rebuild`/`Create` re-apply the hook
> from the **main checkout's** sweatfile (`LoadHierarchy`), not the worktree
> branch's — so a worktree's `pre-commit` value only flips to
> `conformist-pre-commit` once the sweatfile change is on master. Functionally
> moot here (the devShell's `conformist` is already the wrapper, so even the old
> command string runs hermetically); the rename is cosmetic + post-merge.

## Amendment 2026-11-04: the hook is frozen at session start (spinclass#459)

Decided by the operator on 2026-11-04, planned in
`docs/plans/2026-11-04-459-frozen-pre-commit-hook.md`, built the same day on
the operator's second go (`internal/bravo/apply/frozenhook.go`; the plan's §6
records how each open question was resolved).

- **What changes.** The per-commit dispatcher no longer resolves the formatter
  by name from the committing shell's PATH, and no longer re-evaluates the
  worktree flake with `nix develop` when `flake.lock` drifts (#267). At
  `sc start`, `sc spawn` and `sc resurrect` spinclass resolves the formatter to
  an absolute store path by evaluating the devShell of the **landed default
  branch** (the base the session is cut from, FDR 0024) and bakes that path
  into the dispatcher. A lock drift prints a warning only; the frozen
  formatter still runs; fail-open stays.
- **Why.** The #449 canary showed the commit-time `nix develop` fetching an
  unlocked `path:` input of the branch's flake into the world-readable store,
  the copy the guarded gate refuses (spinclass#459). Evaluating the landed
  base instead of the branch means a branch cannot change what formats its own
  commits, and the hook never fetches on the branch's behalf. The hook's
  provenance follows the session (trusted layers plus the landed base), as the
  rest of FDR 0035's required view does.
- **Re-provisioning.** The frozen path is refreshed on `sc resume` and on a
  restart (FDR 0037), which re-run the install from the then-landed base. For a
  restart this happens in the restart preflight, once the restart is approved,
  because clown relaunches without `sc resume`. Before spinclass#475
  (2026-11-10) a restart did not re-freeze, although this record and the hook's
  drift message said it did. A
  stale or broken hook is fixed by the session's holder, never by the session.
  Sessions created before the change keep their old dispatcher until resumed;
  every resume rewrites it, so no sweep is needed.
- **Effect on this record.** "Relationship to `sc rebuild` / setup staleness"
  below is superseded: the frozen path and its base sha are recorded on the
  session (`session.State.FrozenHook`) and compared with the current landed
  base by `sc rebuild --check` and on resume, so a landing that bumps
  conformist is visible as "hook frozen from origin/master at X, origin/master
  now at Y". They do not enter the fingerprint hash itself, which would have
  needed a devShell evaluation on every staleness check; the comparison is a
  `rev-parse`. The name-based model stays the sweatfile's contract
  (`pre-commit = "conformist-pre-commit"`); only the resolution moves from
  commit time to provisioning time.
- **In front of the evaluation.** The base's committed `flake.lock` must exist
  and name no local-path or file-scheme input (the one check a devShell
  evaluation of landed content needs, since nix would copy such an input into
  the store); the guarded gate's full declared-inputs evaluation (#449) is not
  run here, because the base is landed content direnv already evaluates at
  every session start, so the hook adds no exposure beyond the devShell load.
  An operator-accepted deviation; spinclass#464 tracks its re-evaluation.

## As built: the composing per-commit dispatcher (spinclass#184, 2026-11-06)

The per-commit hook that `[hooks].pre-commit` enables was designed in
`docs/plans/2026-06-16-per-commit-repair-hook-design.md` and
`docs/plans/2026-06-17-precommit-hook-composition-design.md` and shipped
without a record of its own; this section is that record, as the code stands
in `internal/bravo/apply/precommit.go` and `frozenhook.go`.

- **Intent.** Repair drift at authoring time, so every commit in history is
  conformant, where the merge-time REPAIR phase (FDR 0018, still live) amends
  only the top commit at the gate.
- **Install and scope.** `installPreCommitHook` runs from the worktree setup at
  `sc start`, `sc spawn`, `sc resurrect`, `sc rebuild` and on every `sc resume`
  (the re-freeze of the amendment above). It enables `extensions.worktreeConfig`
  and sets a per-worktree `core.hooksPath` to `<worktree>/.spinclass/hooks`, so
  one worktree's hook never reaches another's or the main checkout's. It
  refuses when `core.worktree` is set in the common config, the documented
  worktreeConfig footgun.
- **Composition.** `.spinclass/hooks` holds one script, `_spinclass-dispatch`,
  and a symlink to it for `pre-commit` and for every standard hook name the
  repo's original hooks dir provides as an executable, non-sample file. The
  dispatcher branches on its own basename: on `pre-commit` it runs the
  formatter, then (and for every other name) it `exec`s the original same-named
  hook with the same arguments, stdin and exit code, so a blocking native hook
  still blocks and nothing is shadowed. A self-reference guard prevents the
  exec loop. The original hooks dir is captured once, on first install, into
  the sentinel `.spinclass/precommit-original-hooks` (the effective
  `core.hooksPath` if it is not ours, else `$GIT_COMMON_DIR/hooks`), because a
  later install would otherwise read our own override back as the original.
  The hooks dir is rebuilt from scratch on each install so a native hook removed
  upstream drops its shim.
- **Formatter.** The canonical command is `conformist-pre-commit`. Its leading
  word is frozen at install to the absolute path the landed default branch's
  devShell provides (the 2026-11-04 amendment); the dispatcher runs `sh -c` on
  the resulting command line when `command -v` finds the binary.
- **Robustness.** Best effort and non-blocking: the commit proceeds whatever
  the formatter does. A nonzero formatter exit is surfaced loudly, its captured
  stderr plus a banner naming the command and the likely cause (a stale or
  misconfigured formatter), never swallowed, since a formatter silently
  rejecting its own flags would land commits unformatted while looking fine
  (#183 review). A `flake.lock` that differs from the hash baked at install
  prints the one-line drift warning and nothing more.
- **Uninstall.** `disable-pre-commit = true` (or an inactive hook) restores
  native hooks only: our `core.hooksPath` is unset only when it is ours, the
  dispatcher dir, the sentinel and the frozen-hook record are removed. A
  user-managed `core.hooksPath` is left alone.
- **Record.** `session.State.FrozenHook` and the display sidecar
  `.spinclass/precommit-hook.json`, as the amendment describes.
- **Limitations.** A hook manager that rewrites `core.hooksPath` after install
  defeats the composition until the next resume. The captured original is the
  shared `$GIT_COMMON_DIR/hooks`, so a blocking native hook there gates every
  worktree. Implicit (main-checkout) sessions get no hook (#182). A confined
  worker's `grit_commit` bypasses hooks entirely (FDR 0036).

## Problem Statement

spinclass drives conformist in a hand-rolled, pre-helper shape: `flake.nix`
wraps the raw `conformist` binary in a `conformistFmt` `writeShellApplication`
(named `conformist-fmt`) with the formatters assembled on PATH, the config is a
hand-written `./conformist.toml`, and the per-commit hook is the bare string
`pre-commit = "conformist --staged --exit-zero-on-fix"`.

This shape's breakage classes (two bit us this week):

1. **conformist-version drift** — the hook resolves `conformist` from PATH at
   commit time; a stale binary (0.1.6) rejected `--staged --exit-zero-on-fix`.
2. **formatter-PATH drift (conformist#51).** *Refined (live-alder):* this is NOT
   inherent to the flag string — it bites only when the `conformist` on PATH is
   the **toolchain-less bare binary**, which then resolves each formatter from
   PATH and silently skips any that's missing. When `conformist` on PATH is a
   **toolchain wrapper** (formatters baked as store paths), the same bare-flag
   string is safe. spinclass's devShell currently puts the *bare* binary on PATH
   (the wrapper is named `conformist-fmt`), so it's exposed today.
3. **flag-soup** — the exact flags/exit-codes restated by hand in the sweatfile.

## What conformist provides (two consumer shapes)

conformist already ships the store-pinned, config-bundled hooks; which API you
use depends on your shape:

- **Module consumers** get `config.build.{wrapper,check,preCommit,repair}`
  (issues #47/#51/#54) → `conformist-pre-commit` / `conformist-repair`. Requires
  expressing config as a Nix module (`conformist.nix` + presets).
- **TOML consumers** (spinclass) get **`lib.wrapWithToolchain` (#51)** — the
  generalized form of spinclass's own `conformistFmt`: a `writeShellApplication`
  that execs `conformist` with the given `tools` on PATH, optionally pinning
  `--config-file=./conformist.toml`. Keeps the hand-written toml verbatim.

**The eng linters (`presets.eng`) are orthogonal to the hooks.** The hooks come
from the wrapper/build outputs; `presets.eng` is a *separate* opt-in that
enforces eng conventions. A repo can take fully hermetic format/repair hooks
while importing **zero** eng linters, and adopt those later, linter-by-linter.
There is no "quiet adoption" mode to build — it's just "don't import the preset."
This dissolves the adoption-tax friction the earlier draft worried about.

## The gap, and the conformist-side deliverable

`wrapWithToolchain` returns ONE wrapper; the consumer writes the mode flags in
the sweatfile, and there are no named siblings. Module consumers get clean
named `conformist-pre-commit`/`conformist-repair`; TOML consumers don't.

**Deliverable A (conformist, live-alder building):**
`conformist.lib.mkToolchainHooks pkgs { conformist; tools; configFile ? null; }`
→ `{ formatter, preCommit, repair }` — the TOML-world mirror of
`build.{wrapper,preCommit,repair}`: three `writeShellApplication`s carrying the
toolchain on PATH, named `conformist` / `conformist-pre-commit` /
`conformist-repair`, the latter two baking `--staged --exit-zero-on-fix` and
`--commit --amend --exit-zero-on-fix`. Mirroring the module names 1:1 keeps the
two consumer shapes parallel (clean docs; move between shapes without renaming).

## spinclass adoption (the effortless path)

Once A lands, spinclass's migration is small and module-free:

1. **`flake.nix`:** replace the bespoke `conformistFmt` with one call —
   `hooks = conformist.lib.mkToolchainHooks pkgs { conformist = …default; tools = [ gofumpt gotools nixfmt shfmt shellcheck statix deadnix golangci-lint tommy conformist-tommy-codegen ]; configFile = ./conformist.toml; };`
   — then `formatter = hooks.formatter`, and put `hooks.preCommit`/`hooks.repair`
   on the devShell. **`conformist.toml` stays verbatim.** (`go` is ambient from
   the devShell's `mkGoEnv`; golangci-lint/tommy-codegen need it — not baked.)
2. **`sweatfile`:** `pre-commit = "conformist-pre-commit"` (replacing the bare
   flag string). spinclass uses the per-commit model, so no `repair =` line; the
   `conformist-repair` wrapper exists for symmetry / other consumers.

No `conformist.nix`, no `presets.eng`, no Go change in spinclass. The
`GOLANGCI_LINT_CACHE` env stays in the sweatfile `[direnv.dotenv]`.

## Relationship to `sc rebuild` / setup staleness

> Superseded by the 2026-11-04 amendment above (spinclass#459): the resolved
> store path and its base are recorded on the session, and `sc rebuild --check`
> reports a moved base. Kept as the record of the earlier model.

The hook command is the stable name `conformist-pre-commit` resolved via the
devShell PATH; a conformist bump changes the wrapper's store path, not the
command string, so the setup fingerprint doesn't flag it — intended (conformist's
freshness model is devShell-rebuild-on-flake.lock-bump). Unchanged from the prior
revision; the obviated Go-side pin would have been the only way to make `sc
rebuild` track conformist, and it's not worth trading away the name-based model.

## Conformist-side plan (ranked, live-alder)

- **A. `mkToolchainHooks`** — unblocks spinclass first; smallest change; exercised
  by a real consumer (spinclass is the test case, proving byte-identical output).
- **B. Extend the flake-parts `flakeModule` to auto-wire `build.devShell`** (it
  exists in `module-options.nix` but the flakeModule only surfaces
  `formatter`+`checks`) — the win for future *module-shaped* consumers.
- **C. Wire `repair` into `templates/eng` + `conform`** — they scaffold only
  pre-commit; `repair` (#54) was missed.
- **D. Best-practices doc** distinguishing the two consumer shapes (module vs
  toml) and "hooks ⊥ eng conventions."

## Sequencing & follow-ons

conformist ships A → spinclass adopts + proves it end-to-end (devShell reload on
Sasha's side) → best-practices doc (conformist D + a spinclass "paved path"
note) → **optionally, spinclass MCP tooling to bootstrap a repo onto the path**
(e.g. a tool that detects a hand-rolled/bare-conformist setup and emits the
`mkToolchainHooks` flake snippet + sweatfile values) — the "effortless for other
downstream users" endgame.

## Limitations / non-goals

- **Ambient `go`** — the toolchain wrapper relies on `go` from the consumer's
  devShell (golangci-lint/tommy-codegen need it); not baked.
- **devShell-active dependency** — the wrappers are devShell PATH entries.
- TOML-consumer path only here; module-consumer ergonomics (B) are conformist's
  follow-up, not blocking spinclass.
- spinclass does not scaffold other repos — `conformist conform` owns that; the
  optional spinclass MCP bootstrap (above) would emit a snippet, not own config.

## More Information

- conformist: `lib.wrapWithToolchain` (`nix/wrap-with-toolchain.nix`, #51),
  `build.{preCommit,repair,devShell}` (`module-options.nix`, #47/#51/#54),
  `flake-module.nix` (formatter+checks only today), `cmd/conform/`. The
  `mkToolchainHooks` deliverable (A) is in progress (conformist/live-alder).
- spinclass: `flake.nix` (`conformistFmt`, since replaced by the
  `conformistEval` module path), `./conformist.toml` (5 formatters /
  5 linters — handed to live-alder as the test case; since replaced by
  `./conformist.nix`), `sweatfile`
  (`pre-commit`), FDR 0018 (`[hooks].repair`).
- The `sc rebuild`/setup-fingerprint feature (prior) — why a name-based hook is
  invisible to its fingerprint, and why that's fine.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.3.12+5220e30
([commit](https://github.com/amarbel-llc/clown/commit/5220e300cf122de53b80ff84d7b943e526653c9a)).
