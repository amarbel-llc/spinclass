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
`commands_mcp_only.go`. Global flags: `--format` (tap/table for most
commands; merge/check take auto|viewport|plain|ndjson), `--verbose`.

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
**Implicit sessions** (`State.Kind == KindImplicit`, FDR 0014) extend this to
agents attached to a repo's **main checkout** (not an `sc start` worktree):
keyed `<repo>/<rand>` (`<rand> = sha256(session_id)[:8]`) — the branch is NOT in
the key (any branch qualifies; a slash-bearing branch would corrupt the key), it
is kept in `State.Branch` as a display-only hint surfaced as a separate `sc list`
column + `chat-list-sessions` `{branch}` annotation and refreshed on each
`SessionStart` re-fire. Stored worktree-local at
`<checkout>/.spinclass/state-<rand>.json` (one file per session, central index
symlink) so concurrent agents in one checkout never collide. Materialized/torn
down by Claude Code `SessionStart`/`SessionEnd` plugin hooks
(`runSessionStart`/`runSessionEnd` in `internal/hooks/`), which gate on
not-a-worktree (`.git` is a directory) + repo-root == cwd (any branch;
detached-HEAD is a no-op) + the `[hooks].disable-implicit-sessions` knob;
dead-PID orphans are reaped by a `SessionStart` sweep + PID-liveness. The
gates-plus-write core is `hooks.MaterializeImplicit`, shared with serve's
`currentSessionKey`, which lazily materializes an implicit session (#141:
process-random rand, serve's own PID, in-process key cache) when chat/spawn
sender resolution finds none — so chat works even where the harness never
delivered `SessionStart`.
`WriteImplicit`/`RemoveImplicit`/`SweepDeadImplicit`/`FindImplicitAtCwd` are the
per-rand storage API.

**Git operations** (`internal/git/`): Thin wrapper --- all commands use
`git -C <dir>`. Two modes: `Run()` captures output, `RunPassthrough()` streams
to console.

**Worktree resolution** (`internal/worktree/`): Resolves targets to
`ResolvedPath` (branch, abs path, repo path, session key). Bare name →
`<repo>/.worktrees/<branch>`, relative path → resolved from repo root, absolute
→ used directly.

**Sweatfile config** (`internal/sweatfile/`): TOML-based hierarchical
configuration. Merges global (`~/.config/spinclass/sweatfile`) → intermediate
parent dirs → repo-level. Supports `git-excludes`, `claude-allow`, `envrc-directives`, and
`allowed-mcps` arrays (nil = inherit, empty = clear, non-empty = append),
`[[mcps]]`, `[[start-commands]]`, and `[[remotes]]` arrays of tables
(dedup-by-name merge),
`[env]` table (map merge), `[hooks]` table (create/stop/pre-merge lifecycle hooks, scalar
override; includes `disable-merge`, `disable-nix-gc`,
`disable-merge-build-worktree`, `disable-implicit-sessions`,
`pre-merge-output-format`, `inactivity-timeout`), and `[session-entry]` table (start/resume entrypoint commands plus the
FDR 0006 `spawn`/`spawn-entry` argv templates for detached worker launch;
per-field override semantics). The package holds the struct definitions, accessors, `MergeWith`,
`GetDefault`, `Apply`, and the tommy-generated codec (`sweatfile_tommy.go`, via
`//go:generate tommy generate`).

**Sweatfile decode/IO** (`internal/sweatfileio/`): the decode/encode/IO
*consumers* of the generated codec — `Parse`, `Load`, `Save`, `LoadHierarchy`,
`LoadWorktreeHierarchy`. Kept in a package separate from `internal/sweatfile` so
the codegen package contains no hand-written references to the generated
`DecodeSweatfile`/`SweatfileDocument` API; that lets `tommy generate` re-analyze
and regenerate it (the codegen package still type-checks with the generated file
overlaid empty). See the dodder codegen-isolation pattern and tommy #93. No
post-decode nil-normalization is needed — tommy's generated decoder gives
present-empty arrays/maps a non-nil value and leaves absent ones nil, which is
the distinction `MergeWith` relies on.

**Remote sessions** (`internal/remote/`): Routes `host:`-prefixed targets to
sweatfile-declared `[[remotes]]` hosts at the CLI boundary — target grammar,
attach-argv construction (`{ssh}`/`{id}` substitution), parallel ssh list
query (3s/host), and the completion cache under
`~/.local/state/spinclass/remotes/<name>.json`. Resume-only v1; see FDR 0011.

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

- **TAP-14 everywhere — except merge/check**: Most commands default to
  `--format tap`. Diagnostics include git stderr and exit codes in YAML
  blocks. **Carve-out**: `sc merge`/`sc check` and the merge/check MCP tools
  emit ndjson-crap (the CRAP-2 wire format) instead — their `--format` is
  `auto` (default: live viewport on a TTY, raw ndjson records when piped) |
  `viewport` | `plain` (verdict-per-line) | `ndjson`; `tap`/`table` are
  rejected there. The viewport renders to stderr; record capture is the ndjson mode
  (`sc merge > records.ndjson` auto-resolves to ndjson — no viewport). See FDR 0015 and
  `internal/present`.
- **Path resolution**: `worktree.ResolvePath()` is the single entry point for
  target → absolute path conversion. Session keys follow
  `<repo-dirname>/<branch>` format.
- **Target resolution**: explicit `resume`/`close`/`merge`/
  `update-description` targets resolve via `session.FindByTarget`, which
  accepts a worktree directory basename OR a `<repo>/<branch>` session key —
  the exact strings `sc list` prints — from any cwd (cross-repo). An
  ambiguous bare name errors with the colliding session keys; misses are
  tagged `session.ErrTargetNotFound`. `merge` tries the current repo's git
  worktrees first (bare worktrees without session state still merge), then
  falls back to the session lookup.
- **Sweatfile merging**: Config files walk from `$HOME` down to repo root,
  merging at each level.
- **Session entrypoint**: `[session-entry].start` and `[session-entry].resume`
  in sweatfile
  control what command is exec'd. Defaults to `$SHELL`.
- **Session picking**: both `sc resume` and `sc close` source from `session.ListForRepo` via `internal/sessionpick` (`Choose` for close, `ChooseAutoSingle` for resume, which short-circuits a lone candidate past the picker), sorted active-first by `session.SortStates`. Tab completion (`completeWorktreeTargets`, shared by resume/close/merge/update-description) scopes via `session.ListForScope` instead: the containing repo's sessions (offered by bare dirname) plus any session whose repo sits beneath the cwd (offered by `<repo>/<branch>` session key — a cwd above nested repos, e.g. `~/eng` over `~/eng/repos/*`, sees them all); outside any repo, all non-abandoned sessions. Completion labels reuse the picker rows' Detail strings so both read identically. The resume picker (only — close stays local-only) appends cached remote rows from `remote.ReadAllCaches` after the local rows (`Item.State` nil, `Item.Target` = `host:<id>`); selecting one routes over the remote attach template with no dialog, and remote rows never count toward the lone-candidate shortcut. Non-TTY callers get an error listing IDs instead of a hung prompt. Orphaned git worktrees without a state file are not valid `sc close` targets; remove them with `git worktree remove`.
- **Resume confirmation**: auto-detect and picker-single-match resume show a clown-style huh confirm (`resumeConfirmPlan` in `cmd/spinclass/resume_confirm.go` is the pure decision seam); `-y/--yes` skips it. Explicit `sc resume <id>` and multi-match picker selection are dialog-free (naming/selecting is the confirmation — keeps remote attach templates non-interactive). An `active` session (live PID, probably attached elsewhere) warns with default Cancel; `-y` does NOT skip the warning variant, and non-TTY always errors there.
- **External tool deps**: `git` is always required and resolved from `PATH`. `madder` and `direnv` are runtime deps **only** when the binary was built via `lib.mkSpinclass` with the matching input — those paths are burned in at link time (see `spinclass-build-pins(7)` and FDR 0003); the default `nix build` produces a binary with both pins empty, in which case the madder integration is dormant and direnv falls back to `PATH`. `clown` is an optional runtime dep for job-wakeup emits (`internal/clown`): chat wake emits AND async merge/check job-lifecycle emits both fire when `$CLOWN_BIN` is set (the running-under-clown signal) and are dormant otherwise — clown's job-watch monitor is the sole chat push path; without clown, `chat-read` polling is the only receive path (see FDR 0010 and the async section below). `zmx` is an optional runtime dep for `sc spawn`/detached fork **only** when the default `[session-entry].spawn` template is in effect — the template is sweatfile-overridable to any multiplexer (FDR 0006). Interactive prompts use the in-process `huh` library (no `gum` dependency).

## CLI Commands

  Command                          Description
  -------------------------------- ---------------------------------------------------------
  `sc start "<desc>"`              Create and start a new worktree session
  `sc start-gh_pr <N|URL>`         Start a session from a GitHub pull request
  `sc start-gh_issue <N>`          Start a session with GitHub issue context (config-driven, see below)
  `sc start-<custom> <arg>`        User-defined start commands declared in sweatfile
  `sc resume [id]`                 Resume an existing session (auto-detects from cwd; `host:id` reattaches on a `[[remotes]]` host over ssh)
  `sc update-description "<desc>"` Update session description (--id or auto-detect)
  `sc list`                        List all tracked sessions, plus `host:`-prefixed rows from `[[remotes]]` hosts
  `sc merge [target]`              Merge worktree into main, remove session state
  `sc check`                       Run [hooks].pre-merge in the current worktree (agent-CI surface)
  `sc clean`                       Remove merged worktrees and abandoned sessions
  `sc fork [branch]`               Fork current worktree (supports `--from <dir>`; `--brief` launches the fork as a detached worker, FDR 0006)
  `sc spawn <repo> --brief "…"`    Launch a detached, harness-booted worker session in a sibling repo (dirname-addressed; blocks on the worker's chat hello, FDR 0006)
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

`merge-this-session` and `check-this-session` are mutually exclusive in
the MCP tool catalog, gated on `[hooks].disable-merge`:

- Default (flag unset/false): `merge-this-session` is registered;
  `check-this-session` is NOT.
- `[hooks].disable-merge = true`: `merge-this-session` and `sc merge` are
  unavailable; `check-this-session` is registered in their place so
  agents can still exercise `[hooks].pre-merge`.

The `sc check` CLI subcommand is available regardless of the flag.

**Spawned worker sessions** (FDR 0006): `sc spawn <repo> --brief "…"` and the
`spawn-session` / `fork-session` MCP tools launch detached, harness-booted
worker sessions. The target repo is addressed by dirname (leaf search under
`$HOME/*/repos/<leaf>`; explicit paths need a separator). The launch execs
the cascade-merged `[session-entry].spawn` multiplexer template (default
`["zmx", "attach", "{id}", "--detach", "{entry}"]` — attach execs the argv
directly and returns promptly; `zmx run` is unusable here, it types
space-joined keystrokes into a shell, see #145) which boots
`[session-entry].spawn-entry` (e.g. `["clown", "--", "{prompt}"]`) with the
driver's brief as the harness's initial prompt; the worker process env
carries the WORKER's spinclass identity (mirrors `SessionExecutor`). The
spawn blocks until the worker's `SessionStart` hook chat-sends a hello to
the driver (keyed off `spawned_by` in the worker's state; 60s default
deadline, `--hello-timeout` tunes it; dedup via `hello_sent_at`). On
timeout the worker worktree+state persist for inspection (`sc close`
cleans). An optional `[session-entry].spawn-window` argv template
(`{id}`/`{dir}`, no default) is exec'd fire-and-forget right after the
spawn template returns — it opens a terminal window onto the worker
(#149); failures are warnings, never spawn failures. Relatedly,
`sc resume` emits one TTY-gated OSC-2 title (`[session-entry].resume-title`,
default `"{id}"`, empty disables) before exec'ing the attach entrypoint,
since spawned sessions' ptys have no title-writing shell (#154).
Coordination after the hello is the FDR 0010 chat system —
the brief should tell the worker to message the driver's session key when
done. `fork-session` is the same-repo variant (caller must be in an sc
worktree; see #142 for the implicit-driver gap). `internal/spawn` owns
resolution, template substitution, and the hello-gated launch.

From an **implicit (main-checkout) session** (FDR 0014), `merge-this-session` /
`merge-this-session-async` / `sc merge` detect a live implicit session via
`session.FindImplicitAtCwd` and route to `merge.MergeImplicit`: there is nothing
to rebase or ff-merge (the work is already on the default branch), so merge runs
`[hooks].pre-merge` against HEAD (in the isolated build worktree) then `git push`
the default branch as a distinct test record. The MCP handlers enforce the implicit
attestation gate (`enforceAttestationImplicit` → `attestation.CheckImplicit`);
the `sc merge` CLI path is gate-free. `sc close` on an implicit session drops
`state-<rand>.json` only (never the checkout, no nix gc), and `sc list` marks it
`main`. (`check`/`sc check` don't yet route implicit sessions — #132.)

### Async (background + poll) merge/check

Each gated tool has a non-blocking opt-in twin that sidesteps the MCP client's
per-server request timeout for long `[hooks].pre-merge` runs:

- `merge-this-session-async` / `check-this-session-async` — registered next to
  their synchronous counterparts under the same `disable-merge` gating. They
  consume the pre-merge attestation at start (same gate as the sync tools),
  launch the merge/check in a background goroutine inside the long-lived `serve`
  process, and return a job id **immediately**. The hook's live output streams
  to `<worktree>/.spinclass/job.log`; job metadata to `.spinclass/job.json`.
- `session-job-status` (always registered, read-only) — reports the worktree
  session's job: `running|succeeded|failed|cancelled|interrupted`, elapsed,
  last-activity (job.log mtime), a tail of live output, and the full result
  (the same plain ✓/✗ verdict text the sync tool returns) once finished.
- `session-job-cancel` (always registered) — cancels the running job, killing
  the hook subprocess via the job's context.
- `session-job-wait` (always registered) — blocks until the running job finishes
  and returns its full result (the same payload the synchronous tool produces) —
  *join* semantics, it never starts a job. This is the "revert to sync" path: go
  async because you had other work, then call `session-job-wait` once that work
  is done instead of hot-polling. Errors if no job was started. Because it
  blocks, it IS subject to the MCP request timeout for the job's *remaining*
  duration, so call it at/near completion. Backed by `job.WaitDone` (a per-job
  completion channel), so it waits event-driven rather than polling.

One active job per session (async-start refuses while one is running). If the
`serve` process ends mid-job, the next status read reports `interrupted` (the
recorded `serve_pid` is no longer alive). The synchronous `merge-this-session`
/ `check-this-session` remain the default; async is purely additive
(`internal/job` is the store + runner). Heartbeat-style progress notifications
from within the synchronous tools are intentionally NOT implemented — that
requires go-mcp + clown-stdio-bridge changes (see those repos' issues); async
is the spinclass-only path.

**Async vs sync — pick by whether you have other work.** Default to the
synchronous `merge-this-session` / `check-this-session`: they block and return
the result, no polling. Reach for the `-async` twins *only* when you have other
independent work to make progress on while the hook runs — then check back via
`session-job-status` occasionally. The anti-pattern to avoid: starting an async
job and immediately spinning in a tight `session-job-status` loop with nothing
else to do — that's strictly worse than the synchronous tool (same wait, extra
turns). If you started async and then run out of other work, call
`session-job-wait` to block on the result instead of polling. The tool
descriptions encode this guidance so agents choose correctly.

### Clown job-wakeup emits (push instead of poll)

See `docs/features/0010-clown-job-wakeup-producer.md` for the full
feature record (limitations, tuning levers, promotion criteria).

When serve runs under clown (`CLOWN_BIN` set — the producer-may-emit
contract from clown RFC-0009), the async tools additionally emit clown
job lifecycle events via `internal/clown`: `clown job start --label
merge|check --source spinclass` at launch (the returned id is recorded
as `clown_job_id` in job.json) and `clown job done --state
succeeded|failed|cancelled` at completion, so clown's job-watch monitor
wakes the agent with one `[clown-job]` notification line — the
poll-discipline guidance above applies only without clown, and the
async tool descriptions switch wording accordingly at serve startup.
Emits are purely additive: job.json/job.log remain the system of
record, emit failures are logged to job.log (`[clown] ... emit
failed`) and never affect the job result, and the rollback is clown's
`CLOWN_DISABLE_JOB_WAKEUP=1` (emits become exit-0 no-ops; no
spinclass-side switch). Known limitation: a job whose serve process
dies mid-run is reported `interrupted` by the next `session-job-status`
read but never wakes — a dead producer can't emit. The failed-state
wake message carries the first `✗ ` line of the plain-rendered result
when present. `internal/clown` is the shared producer integration (chat's
`EmitWake` delegates to it).

### Pre-merge hook build worktree

By default the `pre-merge` hook runs in a transient **detached build worktree**
pinned to the exact committed sha being merged, not in the live session
worktree. `check.resolveHookDir` creates a hidden `.merge-<branch>-<sha>-<pid>`
sibling under `.worktrees/` via `git.WorktreeAddDetached` (which prunes stale
admin entries first), runs the hook with `cmd.Dir` set there, and removes it via
a deferred `git.WorktreeForceRemove`. madder still targets the session worktree
(`wtPath`); only the hook's working directory relocates. This frees the session
worktree for concurrent edits while the (slow) hook runs, and verifies the
committed tree rather than uncommitted state. See FDR 0013 and
`docs/features/0013-isolated-build-worktree.md`.

The merge flow is split into `merge.PrepareMerge` (disable-merge gate → optional
pull → rebase → nothing-to-merge short-circuit → **pin** the post-rebase
`HEAD` sha) and `merge.FinishMerge` (hook in the build worktree → `git merge
--ff-only <pinnedSha>` → teardown → push). `merge --ff-only` targets the pinned
**sha**, not the branch name, so a commit landing on the branch while the hook
runs is left for a later merge instead of leaking in. `merge-this-session-async`
runs `PrepareMerge` **synchronously** (sharing one `crap.Reporter`+buffer)
before returning the job id — so rebase conflicts /
nothing-to-merge surface immediately and the rebase can't race the agent's next
edits — then backgrounds `FinishMerge`, whose records are appended to the same
buffer for one continuous result stream. The sync `merge-this-session` and both
`check`/`check-this-session` paths inherit the build worktree transparently
(check pins `HEAD`). Opt out per the sweatfile cascade with
`[hooks].disable-merge-build-worktree = true` (runs the hook in place; legacy).
Caveat: the build worktree is detached HEAD, so a hook reading the current
branch name sees `HEAD`.

### Pre-merge hook inactivity watchdog

`[hooks].inactivity-timeout` (a Go duration string, e.g. `"180s"`; unset/`""`/
non-positive = disabled, the default) bounds how long the `pre-merge` hook may
go **silent**. An `activityWriter` in `sweatfile.RunPreMergeHookContext` bumps a
last-activity timestamp on every output line; a goroutine on a `timeout/4`
ticker (clamped `[1s, 15s]`) cancels the hook's child context once
`time.Since(lastActivity) > timeout`, which kills the `sh -c` subprocess
(`exec.CommandContext`). The kill surfaces as a distinct error — `pre-merge hook
killed: no output for <timeout> (inactivity-timeout)` — and, because the
watchdog ctx is a child of the caller's, it is distinguishable from a
`session-job-cancel` (only inactivity yields that error). Both the sync and
async paths funnel through `RunPreMergeHookContext`, so the watchdog covers
`merge`/`check`/`sc check` and their `-async` twins. This catches a *wedged*
hook (different from the async tools, which only sidestep the request-timeout
for hooks that are still making progress). `sc validate` rejects an
unparseable or negative value.

By default `sc close` and `sc clean` perform worktree-scoped Nix garbage
collection after removing the worktree: spinclass enumerates the gc roots
resolving into the worktree, expands their closure, and runs `nix-store
--delete` per path. Nix's own liveness check is the safety net — paths still
rooted elsewhere are reported as `kept`. Set `[hooks].disable-nix-gc = true`
in the sweatfile cascade to opt out, or use `sc close --nix-gc=<true|false>`
to override per-invocation. Silent no-op when `nix-store` isn't on PATH or
the worktree has no gc roots.

## Custom start commands

`sc start-<name>` subcommands can be declared in a sweatfile via the
`[[start-commands]]` array of tables. At CLI startup spinclass loads the
sweatfile hierarchy for the current directory and dynamically registers one
command per entry. Each command validates its single positional argument
and offers tab completion.

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
  - `context` — session context string.
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

## MCP Server Configuration

`allowed-mcps` and `[[mcps]]` control which MCP servers are registered
and auto-approved in Claude Code sessions.

```toml
# Auto-approve externally-registered MCP servers by name
allowed-mcps = ["some-external-server"]

# Register and auto-approve MCP servers
[[mcps]]
name = "my-linter"
command = "my-linter"
args = ["serve"]

[mcps.env]
DEBUG = "1"
```

- `allowed-mcps` uses array-append merge (nil = inherit, `[]` = clear,
  non-empty = append).
- `[[mcps]]` uses dedup-by-name merge (same as `[[start-commands]]`).
  Name-only entry (empty command) removes an inherited server.
- Every `[[mcps]]` entry with a command implicitly adds to the allow-list.

## Remote Sessions

`[[remotes]]` declares hosts whose spinclass sessions appear in `sc list`
and tab completion under a `<name>:` prefix and reattach via
`sc resume <name>:<id>` (execs the attach template; default
`ssh -t {ssh} spinclass resume {id}`).

```toml
[[remotes]]
name   = "devbox"            # the <host>: prefix users type
ssh    = "sasha@devbox.lan"  # optional; Dest() defaults to name
attach = ["ssh", "-t", "{ssh}", "spinclass", "resume", "{id}"]  # optional; shown value is the default
```

- Dedup-by-name merge like `[[mcps]]`, but removal is explicit:
  `remove = true` drops an inherited remote. A name-only entry is a valid
  all-defaults remote (`~/.ssh/config` does the work), NOT a removal
  sentinel — a deliberate divergence from `[[mcps]]`.
- `sc list` queries each host in parallel (`ssh <dest> spinclass list
  --format json`, 3s/host, per-host diagnostic rows) and refreshes the
  completion cache; completion reads only the cache, never networks.
  `close`/`merge` reject `host:` targets ("remote targets support resume
  only (v1)"). See FDR 0011.

## Nix Build

Standalone flake against `amarbel-llc/nixpkgs` (the fork's
`buildGoApplication` overlay auto-injects `-X main.version` and
`-X main.commit` ldflags from the derivation's `version` and `commit`
attrs — `spinclassVersion` and `spinclassCommit` in `flake.nix`). Binary
installs as `spinclass` with `sc` symlink. Shell completions for bash
and fish included.

`gomod.nix` is the consumer half of the flake-input-go_mod protocol
(igloo RFC 0001): it maps the bridged Go modules (`tommy`, `crap`) onto
their producer flakes' `go-pkgs` outputs, and `flake.nix` threads it as
`goFlakeInputs` into both `buildGoApplication` and `mkGoEnv` (see the
gomod.nix header for the lockstep rationale). Bump a bridged dep with
`nix flake update <input>` — no `go get`/`gomod2nix` lockstep, unless
the new rev changes the producer's own dependency graph (its
transitives still resolve organically, so go.mod/go.sum/gomod2nix.toml
need a regen then).

## Dependencies

Module: `github.com/amarbel-llc/spinclass`. Key dependencies: -
`github.com/amarbel-llc/tap/go` --- TAP-14 output library (non-merge/check
commands) -
`github.com/amarbel-llc/crap/go-crap/v2` --- ndjson-crap reader (consumed by
the `ndjson-crap` pre-merge-output-format, where the hook emits canonical
ndjson-crap directly; see `internal/check`) AND the merge/check output stack:
`package crap`'s `Reporter` backs merge/check stage emission and
`package viewport` backs the TTY presentation + plain rendering (consumer
wiring in `internal/present`; bridged from the `crap` flake input via
goFlakeInputs) -
`github.com/amarbel-llc/purse-first/libs/go-mcp` --- MCP server framework -
`github.com/amarbel-llc/tommy` --- TOML library - `github.com/spf13/cobra` ---
CLI framework
