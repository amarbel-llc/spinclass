# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

## Overview

Shell-agnostic git worktree session manager. Manages worktree lifecycles:
creating worktrees with config inheritance, attaching via configurable session
entrypoints, rebasing/merging back to main, and cleaning up. Aliased as `sc`.

## Build & Test Commands

``` sh
just build    # nix build
just test     # Go tests with TAP-14 output
just fmt      # gofumpt
just lint     # go vet
just deps     # Regenerate gomod2nix.toml after dependency changes
```

## Architecture

**CLI layer** (`cmd/spinclass/main.go`): Cobra commands mapping to internal
packages. Global flags: `--format` (tap/table), `--verbose`.

**Core workflow** (`internal/shop/`): Orchestrates create, attach, and fork.
`Create()` sets up worktree + sweatfile + Claude trust. `Attach()` calls Create,
writes session state, then delegates to an `Executor`. `Fork()` branches from
current worktree.

**Executor abstraction** (`internal/executor/`): Interface for session
attachment. `SessionExecutor` (production, execs sweatfile entrypoint with
SIGHUP forwarding) and `ShellExecutor` (used by merge). Tests use a
`mockExecutor`.

**Session state** (`internal/session/`): Tracks sessions in
`~/.local/state/spinclass/sessions/<hash>-state.json`. Three states: `active`
(PID alive, worktree exists), `inactive` (PID dead, worktree exists),
`abandoned` (worktree gone). Dirty state computed live via git.

**Git operations** (`internal/git/`): Thin wrapper --- all commands use
`git -C <dir>`. Two modes: `Run()` captures output, `RunPassthrough()` streams
to console.

**Worktree resolution** (`internal/worktree/`): Resolves targets to
`ResolvedPath` (branch, abs path, repo path, session key). Bare name →
`<repo>/.worktrees/<branch>`, relative path → resolved from repo root, absolute
→ used directly.

**Sweatfile config** (`internal/sweatfile/`): TOML-based hierarchical
configuration. Merges global (`~/.config/spinclass/sweatfile`) → intermediate
parent dirs → repo-level. Supports `git-excludes`, `claude-allow`, and
`envrc-directives` arrays (nil = inherit, empty = clear, non-empty = append),
`[env]` table (map merge), `[hooks]` table (create/stop lifecycle hooks, scalar
override), and `[session]` table (start/resume entrypoint commands, override
semantics).

**Merge/Pull/Clean** (`internal/merge/`, `internal/pull/`, `internal/clean/`):
Post-session workflows. Merge rebases onto default branch then ff-only merges,
removes session state. Clean removes fully-merged worktree branches and
auto-cleans abandoned sessions.

**Permission tiers** (`internal/perms/`): Claude Code hook integration.
Tier-based permission rules stored as JSON (`global.json` +
`repos/<repo>.json`).

**Claude integration** (`internal/claude/`): Updates `~/.claude.json` to trust
worktree paths. Applies `claude-allow` rules from sweatfile to
`.claude/settings.local.json`.

## Key Patterns

- **TAP-14 everywhere**: Most commands default to `--format tap`. Diagnostics
  include git stderr and exit codes in YAML blocks.
- **Path resolution**: `worktree.ResolvePath()` is the single entry point for
  target → absolute path conversion. Session keys follow
  `<repo-dirname>/<branch>` format.
- **Sweatfile merging**: Config files walk from `$HOME` down to repo root,
  merging at each level.
- **Session entrypoint**: `[session].start` and `[session].resume` in sweatfile
  control what command is exec'd. Defaults to `$SHELL`.
- **External tool deps**: `git`, `gum` (interactive selection in merge).

## CLI Commands

  Command                   Description
  ------------------------- --------------------------------------------------------------
  `sc start [desc...]`      Create and start a new worktree session (--pr N or --pr URL)
  `sc resume [id]`          Resume an existing session (auto-detects from cwd)
  `sc update-description`   Update session description (--id or auto-detect)
  `sc list`                 List all tracked sessions from state directory
  `sc merge [target]`       Merge worktree into main, remove session state
  `sc clean`                Remove merged worktrees and abandoned sessions
  `sc fork [branch]`        Fork current worktree (supports `--from <dir>`)
  `sc pull`                 Pull repos and rebase worktrees
  `sc validate`             Validate sweatfile hierarchy
  `sc exec-claude`          Run claude with sweatfile settings

## Nix Build

Standalone flake using `gomod2nix` / `buildGoApplication`. Binary installs as
`spinclass` with `sc` symlink. Shell completions for bash and fish included.

## Dependencies

Module: `github.com/amarbel-llc/spinclass`. Key dependencies: -
`github.com/amarbel-llc/bob/packages/tap-dancer/go` --- TAP-14 output library -
`github.com/amarbel-llc/purse-first/libs/go-mcp` --- MCP server framework -
`github.com/amarbel-llc/tommy` --- TOML library - `github.com/spf13/cobra` ---
CLI framework
