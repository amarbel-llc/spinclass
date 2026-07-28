# spinclass

A shell-agnostic git worktree session manager. Aliased as `sc`.

`spinclass` manages the full lifecycle of git worktree-based sessions: it
creates worktrees with inherited configuration, attaches to them through a
configurable session entrypoint, rebases/merges them back to the default
branch, and cleans them up when they're done. It doubles as a Model Context
Protocol (MCP) server so coding agents can drive the same workflow.

## Why

Working on several branches at once means juggling worktrees by hand: creating
them, copying over local config and excludes, trusting paths, and tearing
everything down afterwards. `spinclass` turns that into a few commands. Each
session gets its own worktree, its own merged configuration, and tracked state,
so you can spin up an isolated workspace for a task, work in it, and merge it
back without losing track of what's in flight.

## Installation

`spinclass` is built with Nix against the `amarbel-llc/nixpkgs` flake.

```sh
# Build the binary (produces ./result/bin/spinclass and the `sc` symlink)
nix build

# Or run directly
nix run . -- list
```

The build installs the binary as `spinclass` with an `sc` symlink, plus bash
and fish shell completions and generated manpages.

## Quickstart

```sh
# Create and start a new worktree session
sc start "fix login bug"

# List tracked sessions
sc list

# Resume a session (auto-detects from the current directory)
sc resume

# Merge the worktree back into the default branch and clean up state
sc merge

# Remove merged worktrees and abandoned sessions
sc clean
```

## Commands

| Command | Description |
| --- | --- |
| `sc start "<desc>"` | Create and start a new worktree session |
| `sc start-gh_pr <N\|URL>` | Start a session from a GitHub pull request |
| `sc start-gh_issue <N>` | Start a session with GitHub issue context |
| `sc start-<custom> <arg>` | User-defined start commands declared in a sweatfile |
| `sc resume [id]` | Resume an existing session (auto-detects from cwd; `host:id` reattaches on a `[[remotes]]` host over ssh) |
| `sc update-description "<desc>"` | Update a session's description |
| `sc exec [--session <t>] [-- <util> …]` | Run a util in a session's worktree (devshell + `SPINCLASS_*` env; defaults to `$SHELL`) |
| `sc list [--watch]` | List tracked sessions (styled table on a TTY; `--watch` live-reloads), plus sessions on `[[remotes]]` hosts |
| `sc merge [target]` | Merge a worktree into main, remove session state |
| `sc check` | Run `[hooks].pre-merge` in the current worktree |
| `sc clean` | Remove merged worktrees and abandoned sessions |
| `sc rebuild [target] [--check]` | Re-apply a drifted worktree's setup and refresh its fingerprint; `--check` reports stale/fresh (exits nonzero if stale) |
| `sc fork [branch]` | Fork the current worktree (supports `--from <dir>`; `--brief` launches the fork as a detached worker) |
| `sc spawn <repo> --brief "…"` | Launch a detached, harness-booted worker session in a sibling repo (blocks on the worker's chat hello) |
| `sc close-child-session <child>` | Close a worker session this session spawned; refuses anything it did not spawn (`--force` for a child with unmerged work) |
| `sc pull` | Pull repos and rebase worktrees |
| `sc validate` | Validate the sweatfile hierarchy |
| `sc perms list\|review\|edit` | Inspect or edit permission tier rules |

Multi-word descriptions must be quoted, e.g. `sc start "fix login bug"`.
Global flags: `--format` (`tap`/`table`) and `--verbose`. Most commands default
to [TAP-14](https://testanything.org/) output, with git stderr and exit codes
surfaced in YAML diagnostic blocks. `sc merge` and `sc check` instead emit
ndjson-crap and take `--format auto|viewport|plain|ndjson` (auto: live
viewport on a TTY, raw records when piped).

## Configuration: the sweatfile

Sessions are configured through a hierarchy of TOML files called *sweatfiles*.
Configuration is merged from the global level down to the repository level:

```
~/.config/spinclass/sweatfile  →  intermediate parent dirs  →  <repo>/sweatfile
```

A minimal repo-level `sweatfile`:

```toml
# vim: set ft=toml

[hooks]
pre-merge = "just"
```

Sweatfiles support:

- **Array directives** (`git-excludes`, `claude-allow`, `envrc-directives`,
  `allowed-mcps`): nil inherits, empty clears, non-empty appends.
- **`[[mcps]]`** and **`[[start-commands]]`**: arrays of tables merged by name.
- **`[env]`**: environment variable map merge.
- **`[hooks]`**: `create` / `stop` / `on-attach` / `on-detach` / `pre-merge` / `post-merge` / `repair` / `pre-commit` lifecycle hooks, plus `disable-merge`, `disable-merge-queue`, `disable-repair`, `disable-post-merge`, `disable-pre-commit`, `disable-nix-gc`, `disable-merge-build-worktree`, and `auto-rebuild-on-resume` flags, and the `inactivity-timeout` / `post-merge-timeout` bounds. `auto-rebuild-on-resume = true` makes `sc resume` re-apply a drifted worktree's setup before attaching (default: warn only; see `sc rebuild`). By default the `pre-merge` hook runs in a transient detached build worktree pinned to the committed sha being merged (freeing the session worktree for concurrent work); `disable-merge-build-worktree = true` runs it in place. `pre-commit` (e.g. `conformist-pre-commit` — conformist's store-pinned toolchain wrapper from `lib.mkToolchainHooks`, preferred over a bare `conformist --staged --exit-zero-on-fix` string so a stale PATH conformist or a missing formatter can't break/skip the hook) installs a per-session git pre-commit hook that repairs staged content at authoring time so each commit is conformant in history (worktree-scoped via `extensions.worktreeConfig` + per-worktree `core.hooksPath`; best-effort, never blocks a commit). It installs as a composing dispatcher that delegates to the repo's existing native hooks rather than shadowing them; `disable-pre-commit` is a true uninstall that restores native-hooks-only. The older `repair` (e.g. `conformist --commit --amend --exit-zero-on-fix`) instead runs before the `pre-merge` VERIFY hook to fold fixes into the merged commit at merge time (FDR 0018). `post-merge` is the mirror of `pre-merge`, running once a merge has landed (and, with git sync, been pushed) — typically a deploy trigger; it receives the landed sha via `SPINCLASS_MERGED_SHA`, runs *under* the per-repo merge lock so a merge stays exclusive end to end (no sibling session can land or deploy mid-hook — at the cost of delaying them), and is non-fatal (a nonzero exit warns rather than failing the already-landed merge). It is capped by `post-merge-timeout` (wall-clock, default 10m, `"0"` disables) — on by default precisely because it holds the queue. See FDR 0023.
- **`[session-entry]`**: `start` / `resume` entrypoint commands (default `$SHELL` — spinclass no longer wraps in a multiplexer; clown owns attach + the terminal title via its clownfile `[attach]`, FDR 0017), `spawn-entry` / `spawn-window` for detached worker launch (FDR 0006; `spawn-entry` is exec'd directly and defaults to the clown spawn form), `liveness-probe` (deprecated — post-FDR-0017 liveness derives from clown's presence index, not this argv), `tombstone-retention`, and `[session-entry.env]` for per-session environment injection.
- **`[[pre-merge-skills]]`**: skill attestation gate — agents must invoke listed skills and record reasoning via `nothing-but-the-truth` before `merge-this-session` will proceed.

Run `sc validate` to check the resolved sweatfile hierarchy for the current
directory.

### Custom start commands

Declare your own `sc start-<name>` subcommands in a sweatfile via
`[[start-commands]]`:

```toml
[[start-commands]]
name             = "jira"               # registers `sc start-jira`
description      = "Start session for a JIRA ticket"
arg-name         = "ticket"
arg-help         = "JIRA ticket ID"
arg-regex        = "^[A-Z]+-[0-9]+$"    # optional RE2 validator
exec-completions = ["sh", "-c", "jira list --json | jq '[.[] | {arg: .key, description: .summary}]'"]
exec-start       = ["sh", "-c", "jira show {arg} --json | jq '{context: .body}'"]
```

`exec-start` runs when the command is invoked, with `{arg}` substituted for the
positional value. Its stdout must be JSON of the form
`{"branch"?: string, "description"?: string, "context": string}`.

### MCP servers

`allowed-mcps` and `[[mcps]]` control which MCP servers are registered and
auto-approved in Claude Code sessions:

```toml
allowed-mcps = ["some-external-server"]

[[mcps]]
name    = "my-linter"
command = "my-linter"
args    = ["serve"]

[mcps.env]
DEBUG = "1"
```

## MCP server

The same command framework that powers the CLI also exposes commands as MCP
tools. Start the stdio MCP server with:

```sh
sc serve
```

Commands defined with a `Run` handler are exposed as MCP tools; CLI-only
commands are not. Whether `merge-this-session` or `check-this-session` is
registered depends on the `[hooks].disable-merge` flag.

## Session state

Sessions are tracked in
`~/.local/state/spinclass/sessions/<hash>-state.json`. A session is one of:

- **active** — PID alive and worktree exists
- **running-detached** — the spinclass attach exited, but a live clown harness is present for the session (matched via clown's presence index, `decoration == <repo>/<branch>`)
- **inactive** — PID dead and no live clown harness, but worktree still exists
- **abandoned** — worktree gone

Dirty state is computed live via git.

## Development

```sh
just build    # nix build
just test     # Go tests with TAP-14 output
just fmt      # conformist (format + repair)
just lint     # conformist check (format-drift + golangci-lint + shellcheck + statix/deadnix)
just deps     # regenerate gomod2nix.toml after dependency changes
```

The codebase is organized under `internal/` by concern: `shop` (core
create/attach/fork workflow), `executor` (session attachment), `session`
(state tracking), `git` (command wrapper), `worktree` (target resolution),
`sweatfile` (TOML config), `merge` / `pull` / `clean` (post-session
workflows), `perms` (permission tiers), and `claude` (Claude Code
integration). The CLI lives in `cmd/spinclass/`.

See [`CLAUDE.md`](CLAUDE.md) for a deeper architectural tour.

## Build pins

The default `nix build` produces a binary with no runtime dependencies burned
in. A consumer flake can call `lib.mkSpinclass` to pin `madder` and `direnv`
at specific Nix store paths:

```nix
spinclass.lib.${system}.mkSpinclass {
  madder = madder.packages.${system}.default;
  direnv = pkgs.direnv;
}
```

With `madder` pinned, `sc start` initialises a per-worktree blob store and
`merge-this-session` / `check-this-session` emit a compact response with a
`resource_link` to the full hook output rather than inlining it. With `direnv`
pinned, spinclass invokes it directly instead of relying on `PATH`.

## Reference

- `spinclass-sweatfile(5)` — full sweatfile field reference and merge semantics
- `spinclass-start-commands(7)` — custom start-* plugin authoring guide
- `spinclass-build-pins(7)` — build-time pin details (madder, direnv)

## License

See the repository for license details.
