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

**CLI layer** (`cmd/spinclass/`): Built on the purse-first
`go-mcp/command.App` framework, not cobra. `main.go` is a thin bootstrap that
calls `buildApp()` (in `commands.go`) and dispatches via `app.RunCLI()`.
Commands are split across `commands_query.go`, `commands_session.go`,
`commands_perms.go`, `commands_hooks.go`, `commands_mcp.go`, and
`commands_mcp_only.go`. Global flags: `--format` (tap/table), `--verbose`.

The same `command.App` registers both CLI subcommands and MCP tools. Commands
with `Run` are exposed as MCP tools via `serve`; commands with `RunCLI` are
CLI-only. The `serve` subcommand starts the stdio MCP server.

Manpages, shell completions, and the purse-first plugin manifest are
generated at build time by the hidden `generate-artifacts` subcommand,
invoked from `flake.nix` `postInstall`.

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

  Command                          Description
  -------------------------------- ---------------------------------------------------------
  `sc start "<desc>"`              Create and start a new worktree session
  `sc start-gh_pr <N|URL>`         Start a session from a GitHub pull request
  `sc start-gh_issue <N>`          Start a session with GitHub issue context (config-driven, see below)
  `sc start-<custom> <arg>`        User-defined start commands declared in sweatfile
  `sc resume [id]`                 Resume an existing session (auto-detects from cwd)
  `sc update-description "<desc>"` Update session description (--id or auto-detect)
  `sc list`                        List all tracked sessions from state directory
  `sc merge [target]`              Merge worktree into main, remove session state
  `sc clean`                       Remove merged worktrees and abandoned sessions
  `sc fork [branch]`               Fork current worktree (supports `--from <dir>`)
  `sc pull`                        Pull repos and rebase worktrees
  `sc validate`                    Validate sweatfile hierarchy
  `sc perms list|review|edit`      Inspect or edit permission tier rules

`start`, `start-gh_pr`, `start-gh_issue`, and `update-description` take
their primary argument as a single positional value. Multi-word descriptions
must be quoted, e.g. `sc start "fix login bug"`. `start-gh_pr` and
`start-gh_issue` offer tab completion for open PRs and issues respectively.
Note that the underlying registered subcommands
use hyphenated names (`perms-list`, `perms-review`, `perms-edit`), but the
space-separated form (`sc perms list`) is also accepted.

## Custom start commands

`sc start-<name>` subcommands can be declared in a sweatfile via the
`[[start-commands]]` array of tables. At CLI startup spinclass loads the
sweatfile hierarchy for the current directory and dynamically registers one
command per entry. Each command validates its single positional argument,
offers tab completion, and injects a markdown fragment into the session's
`.spinclass/system_prompt_append.d/3-start-<name>.md` which is picked up
automatically by the `--append-system-prompt` flow.

```toml
[[start-commands]]
name             = "jira"               # registers `sc start-jira`
description      = "Start session for a JIRA ticket"
arg-name         = "ticket"             # positional parameter name
arg-help         = "JIRA ticket ID"
arg-regex        = "^[A-Z]+-[0-9]+$"    # optional RE2 validator
exec-completions = ["sh", "-c", "jira list --json | jq '[.[] | {arg: .key, description: .summary}]'"]
exec-start       = ["sh", "-c", "jira show {arg} --json | jq '{context: .body}'"]
```

- `exec-completions` is exec'd at tab-completion time. Stdout must be JSON:
  `[{"arg": "...", "description": "..."}, ...]`. Failures are silent.
- `exec-start` is exec'd when the command runs, with `{arg}` literally
  replaced by the positional value in every argv element. Stdout must be
  JSON with the schema: `{"branch"?: string, "description"?: string,
  "context": string}`. The command is exec'd directly (no shell); wrap in
  `sh -c` for shell features.
  - `context` — written to `3-start-<name>.md` as the session's system
    prompt fragment.
  - `description` — used as the session description if the user didn't
    pass `--description`.
  - `branch` — when present, checks out an existing local or remote
    branch (mirroring `start-gh_pr` behaviour) instead of creating a
    new one. The branch must already exist.
- Entries merge across the sweatfile hierarchy (`global → parent → repo`)
  and later definitions override earlier ones by `name`. The built-in
  `gh_issue` entry ships via `sweatfile.GetDefault()` as a tracer bullet —
  it is the same plugin mechanism users get, just baked in.
- Built-in subcommands (e.g. `start` itself or `start-gh_pr`) always win
  over a sweatfile entry with the same name; `sc validate` flags obviously
  broken entries (missing `exec-start`, bad name, non-compiling regex,
  shell interpreter without `arg-regex`).

## Nix Build

Standalone flake using `gomod2nix` / `buildGoApplication`. Binary installs as
`spinclass` with `sc` symlink. Shell completions for bash and fish included.

## Dependencies

Module: `github.com/amarbel-llc/spinclass`. Key dependencies: -
`github.com/amarbel-llc/bob/packages/tap-dancer/go` --- TAP-14 output library -
`github.com/amarbel-llc/purse-first/libs/go-mcp` --- MCP server framework -
`github.com/amarbel-llc/tommy` --- TOML library - `github.com/spf13/cobra` ---
CLI framework
