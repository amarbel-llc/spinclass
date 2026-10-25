# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

This file is an **orientation map, not a reference.** Deep design rationale
lives in `docs/features/` (FDRs), `docs/plans/` (per-issue designs), and the
`spinclass-*(5)`/`(7)` manpages in `doc/`. Your system prompt carries a live
**Design records** index (FDR 0021), so a bare `(FDR NNNN)` cite here is enough
to locate the record — read it before changing that subsystem. `docs/epics/`
holds vision-level records above FDRs. Put new reference prose in a manpage or
FDR, not here (the lint caps this file at 40000 bytes; keep it well under).

## Overview

Shell-agnostic git worktree session manager, aliased `sc`. Manages worktree
lifecycles: creating worktrees with hierarchical config inheritance, attaching
via configurable entrypoints, rebasing/merging back to main, and cleaning up.

## Build & Test Commands

Recipes come from the just-us clown plugin — the system prompt carries the
recipe index; use `list_recipes` / `show_recipe` / `run_recipe`. Inner loop:
`just debug-go-test <dir>` (godyn-test; `git add -N` new files first; no
ambient `go`).

Config is Nix-generated from `./conformist.nix` + `./conformist-impure.nix` +
`conformist.lib.presets.{eng,eng-go,eng-impure}` (`flake.nix`), not a
hand-written `conformist.toml`. Lint runs as the pure, sandboxed `checks.lint`,
NOT in the impure lane (spinclass#294). Version is `version.env`
(`SPINCLASS_VERSION`, eng-versioning(7)); `buildGoApplication` auto-reads it.

`merge-this-session`'s pre-merge hook runs the full verification suite — do NOT
re-run that suite right before merging.

## Architecture

- **CLI layer** (`cmd/spinclass/`): purse-first `go-mcp/command.App` (not
  cobra). `main.go` → `buildApp()` (`commands.go`) → `app.RunCLI()`; commands
  split across `commands_*.go`. The same `App` registers CLI subcommands and MCP
  tools (commands with `Run` become tools via `serve`; `RunCLI` are CLI-only).
  Manpages, completions and the plugin manifest are generated at build time by
  the hidden `generate-artifacts` subcommand; hand-written section 5/7 pages
  are compiled from `doc/*.N.scd` (spinclass#313).
- **Core workflow** (`internal/shop/`): `Create()` = worktree + sweatfile +
  Claude trust; `Attach()` = Create + session state + `Executor`; `Fork()`.
- **Executor** (`internal/charlie/executor/`): `SessionExecutor` (prod, SIGHUP
  forwarding), `ShellExecutor` (merge), `mockExecutor` (tests).
- **Session state** (`internal/alfa/session/`): authoritative frontend record
  `$XDG_STATE_HOME/spinclass/records/<hash>.json` (FDR 0035); the worktree's
  `.spinclass/state.json` is a display-only mirror; `active`/`inactive`/
  `abandoned`, dirty computed live.
  Implicit sessions in a main checkout: FDR 0014. Handles/principals: FDR 0032.
- **Git** (`internal/0/git/`): thin `git -C <dir>` wrapper (`Run` captures,
  `RunPassthrough` streams).
- **Worktree resolution** (`internal/charlie/worktree/`): `ResolvePath()` is the single
  target→path entry point (bare name → `<repo>/.worktrees/<branch>`).
- **Sweatfile** (`internal/0/sweatfile/` pure schema + tommy codec;
  `sweatfileio` Parse/Load; `hookrun` runs hooks; `apply` sets up a worktree,
  #309). Split so codegen regenerates cleanly (tommy #93). Reference:
  `spinclass-sweatfile(5)`.
- **Merge/Pull/Clean** (`internal/merge/`, `pull/`, `clean/`): merge pre-flights
  merge drivers (#324), rebases onto the default branch, then lands; see below.
- **Permission tiers** (`internal/perms/`): Claude Code hook integration, rules
  as JSON. **Claude integration** (`internal/0/claude/`): trust + `claude-allow`.
- **Remote sessions** (`internal/bravo/remote/`): `host:` targets, resume-only
  (FDR 0011).

## Key Patterns

- **TAP-14 everywhere — except merge/check.** `sc merge`/`sc check` and the
  merge/check MCP tools emit ndjson-crap (`--format auto|viewport|plain|ndjson`);
  wiring in `internal/bravo/present` (FDR 0015).
- **Targets.** Session keys are `<repo-dirname>/<branch>`; explicit
  `resume/close/merge/update-description` targets resolve via
  `session.FindByTarget` (worktree basename OR key, from any cwd; ambiguous
  bare names error, misses are `session.ErrTargetNotFound`). `sc resume`/`close`
  pickers: `internal/bravo/sessionpick`; resume confirmation: `resume_confirm.go`
  (`-y` skips; an `active` session warns and `-y` does NOT skip it).
- **External tool deps.** `git` always required. `madder`/`direnv` are runtime
  deps only when built via `lib.mkSpinclass` (paths burned in at link time —
  `spinclass-build-pins(7)`, FDR 0003); a madder write failure never fails a
  gate (#349). `papi`/`gh` are pinned in the default build for the sysprompt
  repository line. `clown` is optional (FDR 0010). `ringmaster` is both a
  runtime PATH CLI and a build-time **linked** library (`jobwake`, via `go.nix`)
  — see `spinclass-internals(7)`.

## CLI Commands

`sc start|start-gh_pr|start-gh_issue|start-<custom>`, `resume`,
`update-description`, `exec`, `run` (FDR 0020), `list [--watch]`, `merge`,
`close [target...]`, `check`, `clean`, `rebuild [--check]`, `fork`, `spawn`
(FDR 0006), `close-child-session`, `restart-this-session` /
`restart-child-session` (`--force` is always-ask) / `cancel-restart` (FDR 0037;
hidden `restart-preflight`), `grant-session-handle` /
`release-session-handle` / `list-handles` / `whoami` (FDR 0032), `resurrect`
(FDR 0027), `grit <tool> [json]` (CLI-only, TTY-required, logged operator escape
hatch for the grit store tools, FDR 0036), `pull`, `validate`, `perms list|review|edit`. Per-command flags:
the generated `spinclass-<cmd>(1)` pages or `sc <cmd> --help`. `start*` and
`update-description` take one positional value (quote multi-word descriptions).
Subcommands register hyphenated (`perms-list`); the spaced form is accepted.

**merge/check tool gating.** `merge-this-session` and `check-this-session` are
mutually exclusive in the MCP catalog, gated on `[hooks].disable-merge`;
`sc check` is always available.

## Subsystems (one line each — read the cited record before changing)

- **Spawned workers** (FDR 0006, #262; confinement FDR 0035; confined spawn: #382, switches in FDR 0035): `sc spawn` /
  `spawn-session` launch a detached worker; async under clown (#266). Reap
  authority is a **handle** (FDR 0032): `close-child-session` gates on it,
  `force` is always-ask (`perms.AlwaysAsk`); grant/release are always-ask.
- **Self-close** (#389, FDR 0035): `close-this-session` queues a close
  (`[permissions] allow-self-close`, trusted layers only, never forces); the
  teardown is `close-self-run`, run from outside the unit.
- **Restart** (#419, FDR 0037): spinclass authorizes/records, clown relaunches;
  the `[session-entry].restart` hand-off is trusted-layers-only, read live.
- **Resurrect** (FDR 0027, #291): `close.RunResolved` records `DeletedSHA` in
  the tombstone; `sc resurrect` recreates the worktree from it.
- **Exemption predicates** (FDR 0031, `merge/policy.go`) and **attestation
  lifecycle** (FDR 0007, #219, `internal/bravo/attestation`).
- **No implicit-session merge** (#317): refused before any gate; `sc check`
  still works.
- **Async merge/check** (`internal/alfa/job`, FDR 0010, FDR 0025): registered only
  under clown; an async job IS a ringmaster job (#243). **Default to the
  synchronous tools**; never hot-poll. A second async merge enqueues (#265,
  `[hooks].disable-merge-stacking`). Liveness flock, ProtocolVersion gate,
  cancel observer, systemd scope: `spinclass-internals(7)`; hook cancellation
  (#188): `docs/plans/2026-10-04-188-sync-hook-cancellation.md`.
- **Dynamic system-prompt fragment** (`internal/bravo/sysprompt`; FDR 0021, FDR 0030,
  #187): `serve` answers `prompts/get`; the fetch is **deadline-capped**
  (`repoFetchTimeout` 2s) and must never block. Trailers: repository line
  (`internal/alfa/repoinfo`), forge workflow, co-active sessions (#238), design
  records, optional manpage/repo indexes (`[sysprompt]`).
- **Pre-merge build worktree** (FDR 0013) → **merge queue** (FDR 0022, #235) →
  **detached landing worktree / Alt B** (FDR 0029, #284) → **landing target**
  (#315/#295, `internal/alfa/landing`; merges read the remote tracking ref, never
  local `<default>`) → **post-merge** (FDR 0023, #244) with named targets +
  verify (FDR 0026, #273; config read at the LANDED sha, #300). Local-default
  caveats: `spinclass-local-default-ref(7)`.
- **Per-session forge push credentials** (FDR 0028, #285, `internal/charlie/auth`;
  `url-resolver` #335 is read only from layers above the repo).
- **Pre-merge REPAIR** (FDR 0018; a dirty tracked tree is refused, #345) and
  **per-commit hook** (FDR 0019, #183/#267; canonical value
  `conformist-pre-commit`).
- **Inactivity watchdog** (`[hooks].inactivity-timeout`; funnels through
  `hookrun.PreMergeInDir`).
- **Base-branch freshening** (FDR 0024, #250, `internal/bravo/basebranch`): fresh
  sessions cut from the fetched remote default; `--allow-stale-base` has
  deliberately **no MCP parameter**.
- **Setup staleness & `sc rebuild`** (`internal/alfa/setupfingerprint`):
  fingerprint recorded in session state; setup applies once at `sc start`.
  Limitation: external-tool *version* drift behind an unchanged command is
  undetected. See `spinclass-sweatfile(5)`.
- **`sc exec`, multi-target close, nix gc on close/clean**:
  `spinclass-internals(7)`, `spinclass-sweatfile(5)` (`disable-nix-gc`).
- **Metrics** (`internal/0/statsd`, #363): `spinclass-local-default-ref(7)`.
- **Grit store tools** (FDR 0036, #379; `internal/alfa/store`, `internal/alfa/store/git`,
  `cmd/spinclass/grit*_cmd.go`): the nine `grit_*` MCP tools, their
  ownership-based verdicts, agentless remote lane and the `sc grit` escape
  hatch: `spinclass-grit(7)`; `[store]` identity: `spinclass-sweatfile(5)`.

## Sweatfile config

Full reference: `spinclass-sweatfile(5)`; custom start commands:
`spinclass-start-commands(7)`. Merge semantics (hierarchy global → parent dirs
→ repo): string arrays are nil=inherit, `[]`=clear, non-empty=append;
arrays-of-tables dedup by name (`[[mcps]]`, `[[start-commands]]`, `[[remotes]]`,
`[[post-merge]]`; a name-only entry removes an inherited one); `[sysprompt]`
arrays **override** rather than append; scalars override. A worktree's own
sweatfile is NOT a layer (`LoadHierarchy` resolves to the main checkout; only
`sweatfileio.WorktreeOverrides` data fields are read); `[session-entry]`
start/resume/env are pinned in `session.State.Entry` at creation.

## Nix Build

Standalone flake against `amarbel-llc/nixpkgs`; the fork's `buildGoApplication`
injects `-X main.version`/`-X main.commit`. Binary `spinclass` with an `sc`
symlink; bash + fish completions included. `go.nix` (igloo FDR 0008) is the
module manifest — no go.mod/go.sum; `flakeInputs` bridge `tommy`, `crap`,
`ringmaster` and `purse-first/libs/dewey` onto producer `go-pkgs` outputs (bump
with `nix flake update <input>`); `purse-first` is a direct input
(`ringmaster.inputs.purse-first.follows`). `ringmaster` is also a test
`nativeCheckInputs` binary and devShell package (#253). On x86_64-linux the
default build, `checks.spinclass-tests`, `vet`, `lint` and `tommy-codegen` run
on igloo's godyn backend; `.#spinclass-build_go_application` elsewhere.

## Dependencies

Module `code.linenisgreat.com/spinclass`: `tap/go` (TAP-14 output),
`crap/go-crap/v2` (merge/check ndjson-crap + `present`), `purse-first/libs/go-mcp`
(`command.App`), `purse-first/libs/dewey/pkgs/mesa` (`sc list` List-Table
renderer, #185; `--format json` stays the raw `session.ListRow` array),
`tommy` (TOML), `ringmaster/pkgs/jobwake` (linked half of the job platform).
