# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

The deep design rationale for most subsystems lives in `docs/features/` (FDRs)
and the `spinclass-*(5)`/`(7)` manpages — this file orients you and points there
rather than duplicating it. When a section cites an FDR or manpage, read it
before changing that subsystem.

## Overview

Shell-agnostic git worktree session manager, aliased `sc`. Manages worktree
lifecycles: creating worktrees with hierarchical config inheritance, attaching
via configurable entrypoints, rebasing/merging back to main, and cleaning up.

## Build & Test Commands

``` sh
just build         # nix build + regen the tommy codec
just test          # Go tests with TAP-14 output (via nix flake check)
just verify         # version+commit ldflag burn-in + tommy-codec drift guard
just codemod-fmt    # conformist: format Go/Nix/shell/TOML + regen tommy codec
just lint           # conformist check (sandboxed) + conformist check --tree-root .
                     #   (impure: git-remotes/sweatfile/agents-md/gomod2nix + golangci-lint)
just update-gomod2nix  # Regenerate gomod2nix.toml after dependency changes
```

Config is Nix-generated from `./conformist.nix` + `./conformist-impure.nix` +
`conformist.lib.presets.{eng,eng-go,eng-impure}` (`flake.nix`), not a
hand-written `conformist.toml`. golangci-lint lives in the impure lane
(`./conformist-impure.nix`) — it needs ambient `go` + a writable cache,
unavailable in the sandboxed `checks.formatting` the pure lane builds. Version
is `version.env` (`SPINCLASS_VERSION`, eng-versioning(7)); the fork's
`buildGoApplication` auto-reads it — no `version` attr is passed explicitly in
`flake.nix`.

`merge-this-session`'s pre-merge hook runs `just` (the default verification
suite) — do NOT redundantly run `just`/`just test` right before merging.
Cheap per-package `go build ./internal/foo/...` checks are fine.

## Architecture

- **CLI layer** (`cmd/spinclass/`): Built on purse-first `go-mcp/command.App`
  (not cobra). `main.go` bootstraps `buildApp()` (`commands.go`) and dispatches
  via `app.RunCLI()`. Commands split across `commands_*.go`. The same `App`
  registers both CLI subcommands and MCP tools: commands with `Run` become MCP
  tools via `serve`; `RunCLI` commands are CLI-only. `serve` starts the stdio
  MCP server. Manpages, completions, and the plugin manifest are generated at
  build time by the hidden `generate-artifacts` subcommand (`flake.nix`
  `postInstall`).
- **Core workflow** (`internal/shop/`): `Create()` sets up worktree + sweatfile
  + Claude trust; `Attach()` calls Create, writes session state, delegates to an
  `Executor`; `Fork()` branches from the current worktree.
- **Executor** (`internal/executor/`): session-attach interface.
  `SessionExecutor` (production; execs the entrypoint with SIGHUP forwarding),
  `ShellExecutor` (merge), `mockExecutor` (tests).
- **Session state** (`internal/session/`): tracks sessions in
  `~/.local/state/spinclass/sessions/<hash>-state.json`. States: `active` (PID
  alive + worktree), `inactive` (PID dead + worktree), `abandoned` (worktree
  gone); dirty computed live via git. **Implicit sessions** (FDR 0014) extend
  this to agents in a repo's **main checkout**: keyed `<repo>/<rand>`, stored
  worktree-local at `<checkout>/.spinclass/state-<rand>.json`, materialized by
  Claude Code `SessionStart`/`SessionEnd` hooks (`internal/hooks/`). See FDR
  0014 for the full model.
- **Git** (`internal/git/`): thin `git -C <dir>` wrapper. `Run()` captures,
  `RunPassthrough()` streams.
- **Worktree resolution** (`internal/worktree/`): `ResolvePath()` is the single
  target→absolute-path entry point. Bare name → `<repo>/.worktrees/<branch>`,
  relative → from repo root, absolute → as-is.
- **Sweatfile config** (`internal/sweatfile/` + `internal/sweatfileio/`):
  hierarchical TOML, merged global (`~/.config/spinclass/sweatfile`) → parent
  dirs → repo. `sweatfile` holds the structs, `MergeWith`, `GetDefault`,
  `Apply`, and the tommy-generated codec (`sweatfile_tommy.go`); `sweatfileio`
  holds the decode/IO consumers (`Parse`/`Load`/`Save`/`LoadHierarchy`) — split
  so codegen can regenerate the codec package cleanly (dodder codegen-isolation
  pattern, tommy #93). Config surface is documented in `spinclass-sweatfile(5)`.
- **Merge/Pull/Clean** (`internal/merge/`, `internal/pull/`, `internal/clean/`):
  post-session workflows. Merge rebases onto the default branch then ff-only
  merges; clean removes merged worktree branches and abandoned sessions. A
  starting merge (all surfaces, not `sc check`) emits a best-effort
  informational test point listing co-active sessions on the repo (#238).
- **Permission tiers** (`internal/perms/`): Claude Code hook integration,
  rules as JSON (`global.json` + `repos/<repo>.json`).
- **Claude integration** (`internal/claude/`): trusts worktree paths in
  `~/.claude.json`, applies `claude-allow` to `.claude/settings.local.json`.
- **Remote sessions** (`internal/remote/`): routes `host:`-prefixed targets to
  sweatfile `[[remotes]]` hosts; resume-only (FDR 0011).

## Key Patterns

- **TAP-14 everywhere — except merge/check.** Most commands default to
  `--format tap` with git stderr/exit-codes in YAML diagnostic blocks.
  `sc merge`/`sc check` and the merge/check MCP tools instead emit ndjson-crap:
  `--format` is `auto` (TTY viewport, raw ndjson when piped) | `viewport` |
  `plain` | `ndjson` (`tap`/`table` rejected). Consumer wiring in
  `internal/present`; see FDR 0015.
- **Path / target resolution.** `worktree.ResolvePath()` converts targets;
  session keys are `<repo-dirname>/<branch>`. Explicit
  `resume`/`close`/`merge`/`update-description` targets resolve via
  `session.FindByTarget` (worktree basename OR `<repo>/<branch>` key, the exact
  strings `sc list` prints, from any cwd). Ambiguous bare names error; misses
  are `session.ErrTargetNotFound`. `merge` tries the current repo's git
  worktrees first (bare worktrees merge), then the session lookup.
- **Session picking** (`internal/sessionpick`): `sc resume`/`sc close` source
  from `session.ListForRepo`; tab completion (`completeWorktreeTargets`) scopes
  via `session.ListForScope`. The resume picker also appends cached remote rows
  (`remote.ReadAllCaches`). Non-TTY callers get an error, not a hung prompt.
- **Resume confirmation.** Auto-detect / picker-single-match resume show a huh
  confirm (`resumeConfirmPlan` in `cmd/spinclass/resume_confirm.go`); `-y/--yes`
  skips it. Explicit `sc resume <id>` and multi-match selection are dialog-free.
  An `active` session warns with default Cancel (`-y` does NOT skip that
  variant; non-TTY always errors).
- **External tool deps.** `git` is always required (from PATH). `madder` and
  `direnv` are runtime deps **only** when built via `lib.mkSpinclass` with the
  matching input (paths burned in at link time — `spinclass-build-pins(7)`, FDR
  0003); the default `nix build` leaves both pins empty (madder dormant, direnv
  from PATH). `papi` and `gh` are pinned the same way (`-X main.papiBin`/`ghBin`)
  and, unlike madder, are burned into the **default** build; they power the
  dynamic system-prompt repository line (`internal/repoinfo`) and fall back to
  PATH lookup when unpinned (devshell `go build`). `clown` is optional, for async
  job-wakeup emits (`internal/clown`, FDR 0010). Interactive prompts use the
  in-process `huh` library.

## CLI Commands

  Command                          Description
  -------------------------------- ---------------------------------------------------------
  `sc start "<desc>"`              Create and start a new worktree session
  `sc start-gh_pr <N|URL>`         Start a session from a GitHub pull request
  `sc start-gh_issue <N>`          Start a session with GitHub issue context (config-driven)
  `sc start-<custom> <arg>`        User-defined start commands (sweatfile `[[start-commands]]`)
  `sc resume [id]`                 Resume a session (auto-detects from cwd; `host:id` reattaches over ssh)
  `sc update-description "<desc>"` Update session description (--id or auto-detect)
  `sc exec [--session <t>] [-- <util> …]` Run a util in a session's worktree (devshell + SPINCLASS_* env; defaults to $SHELL)
  `sc run [flags] (-- <util> … \| <stdin>)` One-shot: start → run one command → merge + clean up (FDR 0020)
  `sc list [--watch]`              List tracked sessions (charm table on TTY, plain/JSON when piped); `--watch` live-reloads
  `sc merge [target]`              Merge worktree into main, remove session state
  `sc check`                       Run [hooks].pre-merge in the current worktree (agent-CI surface)
  `sc clean`                       Remove merged worktrees and abandoned sessions
  `sc rebuild [target] [--check]`  Re-apply a drifted worktree's setup; `--check` reports stale/fresh
  `sc fork [branch]`               Fork current worktree (`--from <dir>`; `--brief` launches a detached worker, FDR 0006)
  `sc spawn <repo> --brief "…"`    Launch a detached worker session in a sibling repo (FDR 0006)
  `sc pull`                        Pull repos and rebase worktrees
  `sc validate`                    Validate sweatfile hierarchy
  `sc perms list|review|edit`      Inspect or edit permission tier rules

`start*` and `update-description` take a single positional value (quote
multi-word descriptions). Registered subcommands use hyphenated names
(`perms-list`), but the space-separated form (`sc perms list`) is accepted.

**merge/check tool gating.** `merge-this-session` and `check-this-session` are
mutually exclusive in the MCP catalog, gated on `[hooks].disable-merge`: default
registers `merge-this-session`; `disable-merge = true` removes it (and
`sc merge`) and registers `check-this-session` instead. The `sc check` CLI
subcommand is always available.

## Subsystems (read the cited record before changing)

- **Spawned/forked workers** (FDR 0006): `sc spawn <repo> --brief`,
  `sc fork --brief`, and the `spawn-session`/`fork-session` MCP tools launch
  detached harness-booted workers. Target repo addressed by dirname. Launch
  execs the cascade-merged `[session-entry].spawn-entry` (default the clown
  spawn form) with the brief as initial prompt; blocks on the worker's
  `SessionStart` hello (`internal/spawnhandshake`, 60s default,
  `--hello-timeout`). Workers can't spawn sub-workers (#148); the MCP tools are
  always-ask (#151). Coordination afterward is clown chat. `internal/spawn` owns
  resolution + launch.
- **Implicit-session merge** (FDR 0014): from a main-checkout session, merge
  routes to `merge.MergeImplicit` — runs `[hooks].pre-merge` against HEAD then
  `git push` (nothing to rebase). MCP path enforces the implicit attestation
  gate; CLI is gate-free. `sc close` drops only `state-<rand>.json`.
- **`sc exec`** (`internal/sessionexec`): runs a util in a session worktree,
  devshell-scoped via `direnv exec`, with `SPINCLASS_*` identity env. No
  `--session` → auto-detect from cwd; util defaults to `$SHELL`. Explicit
  `--session` miss errors; cwd miss degrades (runs in cwd, strips identity).
  Motivating consumer: posh "escape to shell". CLI-only; raw passthrough.
- **`sc run`** (`internal/run`, FDR 0020): one-shot start → run one command (or
  a shebang-aware stdin script) → merge + teardown. Teardown is a 2×2 matrix
  over `--no-merge`/`--no-close`; an empty run (0 commits) is a clean success;
  any nonzero step leaves the session intact for inspection. `--local-only`
  passes through to merge. Builds on `sc start --no-attach` writing findable
  inactive state. CLI-only. Uses the merge/check `present` stack.
- **Async merge/check** (`internal/job`): `merge-this-session-async` /
  `check-this-session-async` consume the attestation, launch in a background
  goroutine inside `serve`, return a job id immediately (output →
  `.spinclass/job.log`, metadata → `.spinclass/job.json`). Inspect via
  `session-job-status` (read-only), `session-job-cancel`, `session-job-wait`
  (blocking join). One active job per session. **Default to the synchronous
  tools** — reach for async only when you have other work to do while the hook
  runs; never start async then hot-poll status.
- **Clown job-wakeup emits** (FDR 0010, `internal/clown`): when `serve` runs
  under clown (`CLOWN_BIN` set), async tools emit `ringmaster start/done`
  (clown's job-control CLI, clown RFC-0015; resolved from PATH or
  `$RINGMASTER_BIN`, never by pinning clown) so clown wakes the agent with one
  `[clown-job]` line. Purely additive;
  job.json/job.log stay the system of record; rollback is
  `CLOWN_DISABLE_JOB_WAKEUP=1`.
- **Dynamic system-prompt fragment** (spinclass#187, clown plugin protocol
  RFC-0002 §5, `internal/sysprompt`): `serve` advertises an MCP `prompts`
  capability and answers `prompts/get` for the well-known `system-prompt-append`
  prompt. clown's stdio bridge (opted in via `clown.json`
  `stdioServers.spinclass.systemPrompt = true`) fetches it **before
  `initialize`** and appends it last into the agent's system prompt; go-mcp's
  V0-only `PromptRegistry` answers the cold request. `sysprompt.Resolve` branches
  on runtime state — **worktree session** (`SPINCLASS_WORKTREE` set + cwd inside
  it) vs **main checkout** (implicit session, FDR 0014; coordinates from
  cwd+git) — and `Render` picks the matching embedded template. Both templates
  carry a best-effort **repository line** (provider/owner/link/description)
  resolved by `internal/repoinfo`: the git remote gives host/owner/name/link with
  no network (for vanity single-segment remotes like `git@host:repo.git` the owner
  is absent from the path and resolved via papi — spinclass#221), then a
  **deadline-capped** (`repoFetchTimeout`, 2s) live lookup adds the forge kind and
  owner login via the operator's published PAPI (`.forges[]` for kind,
  `.organizations[]` for the missing owner on vanity remotes) and the description
  (`gh api` for GitHub, the Gitea/Forgejo REST API self-hosted). Any failure omits
  only the affected lines — the fetch runs before `initialize`, so it must never
  block. Both templates also conditionally render a **Forge workflow** block (when
  the resolved forge kind is non-GitHub and a URL is present) instructing the agent
  to use `fj`/`smith` rather than `gh` and noting any GitHub copy is a read-only
  mirror. Both templates also carry a best-effort **co-active sessions** line
  (#238) — the other `active` sessions on the same repo, from local session
  state + PID liveness only (no network, current session excluded; any failure
  omits the line). Both templates additionally gain a Go-composed
  **Design records** trailer (FDR 0021, `internal/sysprompt/docsindex.go`): an
  index of the repo's `docs/features`/`docs/adrs`/`docs/rfcs` records by
  number·title·status, grouped by status, scanned scan-if-exists from local
  files (no network, so pre-`initialize`-safe); the scanned dirs are overridable
  via `[sysprompt].doc-index-dirs` (override not append; `[]` disables), read by
  `sysprompt.Resolve` via a `sweatfileio.LoadHierarchy` load. Malformed records
  (unreadable / unterminated frontmatter) surface in a `⚠ malformed` diagnostic
  block, and a `recover()` guarantees a broken doc can never fail the
  pre-`initialize` render. This **replaces**
  the retired static `.clown-plugin/system-prompt-append.d/` fragments (no static
  fallback); rollback is restoring those files + the flake install lines.
- **Pre-merge build worktree** (FDR 0013): by default the hook runs in a
  transient detached worktree pinned to the committed sha (`check.resolveHookDir`
  → `.merge-<branch>-<sha>-<pid>` under `.worktrees/`), freeing the session
  worktree for concurrent edits. Merge splits into `merge.PrepareMerge` (gate →
  pull → rebase → nothing-to-merge → REPAIR → pin HEAD sha) and
  `merge.FinishMerge` (gate + landing, serialized under the merge queue by
  default — next bullet; with `[hooks].disable-merge-queue`: hook →
  `git merge --ff-only <pinnedSha>` → teardown → push). Async runs Prepare
  synchronously, backgrounds Finish. Opt out with
  `[hooks].disable-merge-build-worktree`.
- **Per-repo merge queue** (FDR 0022, #235): `FinishMerge` serializes landings
  on an advisory flock (`internal/mergelock`; `spinclass-merge.lock` in the
  shared git common dir via `git.CommonGitDir` — poll-based so acquisition is
  ctx-cancellable, self-releases on process death, never unlinked). Acquired
  BEFORE the gate; under the lock: re-pull → ancestry check → if the default
  tip moved, rebase the pinned commits in a transient `.land-*` worktree
  (conflict ⇒ `merge.ErrIntegrationConflict`, the queue's only hard failure —
  resolution is a plain re-merge) → gate on the LANDING sha → ff-only →
  teardown (`branch -D` when rebased) → push. Queue waits heartbeat to the
  async job log and are exempt from the inactivity watchdog (it wraps only the
  hook subprocess). Worktree merges only (`MergeImplicit` excluded); lock is
  host-local. `[hooks].disable-merge-queue` restores the pre-#235 fail-on-race
  path verbatim.
- **`post-merge` hook** (FDR 0023, #244): when `[hooks].post-merge` is set,
  `merge.runPostMergePhase` runs it after a merge has fully landed (ff-only
  done; pushed when gitSync). On the queued path it runs **under the landing
  lock**, as the last stage before `FinishMerge` returns and the deferred
  `Release` fires — a merge is exclusive end to end, so no sibling session can
  land or deploy mid-hook (an early release would let two deploys interleave,
  the exact failure #244 exists to prevent). Cost: a slow hook extends the
  exclusive region and delays every other merge in the repo.
  Non-fatal by design: a nonzero exit emits a `severity=warn` not-ok point
  with the hook's output but does NOT fail the merge (nothing to roll back;
  a retry would find nothing to merge). Runs in the session worktree if it
  survived teardown, else `repoPath`; no build worktree, no
  `inactivity-timeout`. Publishes `SPINCLASS_MERGED_SHA` (the **landing** sha
  on a rebased queued landing), `_MERGED_BRANCH`, `_DEFAULT_BRANCH`,
  `_MERGE_PUSHED`, `_REPO_PATH` via `sweatfile.runHookInDirEnv`. All three
  land paths fire it (queued, `disable-merge-queue`, `MergeImplicit`);
  `sc check` never does. `disable-post-merge` is the opt-out.
- **Pre-merge REPAIR phase** (FDR 0018): when `[hooks].repair` is set,
  `PrepareMerge` runs it in the **session worktree** before the pin to fold
  mechanical fixes into the merged commit (canonical
  `conformist --commit --amend --exit-zero-on-fix`; amend detected via HEAD-sha
  delta). Merge-only; worktree sessions only. spinclass's own sweatfile has
  **retired** this in favour of the per-commit hook below.
- **Per-commit repair hook** (FDR 0019, #183): when `[hooks].pre-commit` is set,
  `sweatfile.Apply` installs a per-worktree git pre-commit hook
  (`internal/sweatfile/precommit.go`), so drift is repaired at authoring time and
  every commit is conformant in history. Canonical value is the store-pinned
  wrapper **`conformist-pre-commit`** (name the wrapper, NOT a bare
  `conformist --staged` string — conformist#51). Scoped to the worktree via
  `core.hooksPath`; `.spinclass/hooks` is a composing dispatcher that runs the
  formatter then execs the original native hook. Best-effort, non-blocking.
  `disable-pre-commit` is a true uninstall (the rollback). Worktree sessions
  only.
- **Inactivity watchdog**: `[hooks].inactivity-timeout` (Go duration; unset =
  off) bounds how long the pre-merge hook may go silent. Covers
  merge/check/`sc check` + async twins (all funnel through
  `RunPreMergeHookContext`). Distinct error message vs `session-job-cancel`.
- **Setup staleness & `sc rebuild`** (`internal/setupfingerprint`): setup is
  applied **once at `sc start`** (`sc resume` does NOT re-apply), so it drifts.
  A fingerprint (`sha256(scheme · version+commit · pins · canonical-JSON(merged
  config))`) is recorded in session state; a mismatch flags stale. `sc rebuild`
  re-applies (`--check` reports read-only, nonzero when stale); `sc resume`
  warns and, with `[hooks].auto-rebuild-on-resume`, re-applies.
  **Limitation:** does not detect external-tool *version* drift behind an
  unchanged command.
- **Nix gc on close/clean**: `sc close`/`sc clean` gc the worktree's resolved
  closure (`nix-store --delete`; Nix liveness is the safety net). Opt out with
  `[hooks].disable-nix-gc` or `sc close --nix-gc=<bool>`. No-op without
  `nix-store` on PATH.

## Sweatfile config quick reference

Full reference: `spinclass-sweatfile(5)`. The hierarchy merges global → parent
dirs → repo at each level. Notable surface:

- Arrays with nil/empty/non-empty merge semantics (nil inherit, `[]` clear,
  non-empty append): `git-excludes`, `claude-allow`, `envrc-directives`,
  `allowed-mcps`.
- Arrays of tables, dedup-by-name: `[[mcps]]`, `[[start-commands]]`,
  `[[remotes]]`.
- `[env]` (map merge); `[hooks]` (lifecycle hooks + the `disable-*` /
  `*-timeout` / output-format knobs, scalar override);
  `[session-entry]` (start/resume/spawn-entry/spawn-window argv, per-field
  override; `model-flags` provider→CLI-flag map, merged per-key like `[env]`);
  `[sysprompt]` (`doc-index-dirs` array — **override not append**:
  non-empty replaces, `[]` disables, nil inherits the built-in default).

**Custom start commands** (`[[start-commands]]`): each entry registers
`sc start-<name>` with a validated positional arg + tab completion.
`exec-completions` emits `[{arg, description}]` JSON; `exec-start` emits
`{branch?, description?, context}` JSON with `{arg}` substituted. Merges across
the hierarchy (later wins by name); built-in subcommands always win. The
built-in `gh_issue` ships via `GetDefault()` as a tracer bullet.

**MCP servers**: `allowed-mcps` (array-append) auto-approves external servers;
`[[mcps]]` (dedup-by-name) registers + auto-approves servers (name-only entry
removes an inherited one; every entry with a command implicitly allow-lists).

**Remotes**: `[[remotes]]` declares `host:`-prefixed resume targets (dedup-by-
name; `remove = true` drops an inherited one). `sc list` queries each host in
parallel and refreshes a completion cache; `close`/`merge` reject `host:`
targets (FDR 0011).

## Nix Build

Standalone flake against `amarbel-llc/nixpkgs`; the fork's `buildGoApplication`
overlay injects `-X main.version`/`-X main.commit` ldflags from the derivation's
`version`/`commit` attrs. Binary installs as `spinclass` with an `sc` symlink;
bash + fish completions included.

`gomod.nix` is the consumer half of the flake-input-go_mod protocol (igloo RFC
0001): it maps bridged Go modules (`tommy`, `crap`) onto their producer flakes'
`go-pkgs` outputs, threaded as `goFlakeInputs`. Bump a bridged dep with
`nix flake update <input>` — no gomod2nix lockstep unless the new rev changes
the producer's own dependency graph.

## Dependencies

Module: `code.linenisgreat.com/spinclass`.

- `code.linenisgreat.com/tap/go` — TAP-14 output (non-merge/check commands).
- `code.linenisgreat.com/crap/go-crap/v2` — ndjson-crap reader + the
  merge/check output stack (`crap.Reporter` emission, `viewport` presentation;
  wired in `internal/present`).
- `code.linenisgreat.com/purse-first/libs/go-mcp` — MCP server framework
  (`command.App` does CLI dispatch + MCP serving; no cobra).
- `code.linenisgreat.com/tommy` — TOML library.
