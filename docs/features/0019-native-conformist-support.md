---
status: proposed
date: 2026-06-20
promotion-criteria: |
  proposed -> experimental: conformist ships `lib.mkToolchainHooks` (the
  TOML-consumer mirror of build.{wrapper,preCommit,repair}); spinclass wires it
  into flake.nix and switches its sweatfile to `pre-commit = "conformist-pre-commit"`,
  proven to produce byte-identical fmt/check to today's conformistFmt.
  experimental -> testing: the wired hook survives a conformist bump (flake.lock
  update + devShell reload) with no version/formatter-PATH/flag breakage, ~1 week.
---

# Native conformist support in spinclass

> **Proposed / exploration.** Design-first, after `sc rebuild`. This is the
> THIRD revision and the direction is now settled with conformist/live-alder.
> The two earlier drafts were wrong-headed: (1) pinning the bare `conformist`
> binary + rewriting the flag string (reinvents what conformist already ships);
> (2) migrating spinclass to conformist's **Nix module** (`build.preCommit`).
> spinclass is **not** a module consumer — it is a **TOML consumer** (a
> hand-written `conformist.toml` + bespoke tools + a hand-rolled wrapper). For
> that shape conformist's answer is `wrapWithToolchain` (#51) and a new
> `lib.mkToolchainHooks` helper. Adoption becomes ~3 lines, no module port, no
> eng-preset tax.

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
- spinclass: `flake.nix` (`conformistFmt`), `./conformist.toml` (5 formatters /
  5 linters — handed to live-alder as the test case), `sweatfile`
  (`pre-commit`), FDR 0018 (`[hooks].repair`).
- The `sc rebuild`/setup-fingerprint feature (prior) — why a name-based hook is
  invisible to its fingerprint, and why that's fine.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.3.12+5220e30
([commit](https://github.com/amarbel-llc/clown/commit/5220e300cf122de53b80ff84d7b943e526653c9a)).
