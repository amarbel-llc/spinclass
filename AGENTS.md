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
  job-wakeup emits (`internal/clown`, FDR 0010). `ringmaster` has **two** roles:
  a runtime PATH CLI (the wake emit/observe, never pinned) AND a **build-time
  linked library** — `code.linenisgreat.com/ringmaster/pkgs/jobwake`, bridged
  via `gomod.nix` — for the per-job liveness flock and the `ProtocolVersion`
  constant (#26). Interactive prompts use the in-process `huh` library.

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
  `sc close-child-session <child>` Reap a worker THIS session spawned (#249); refuses anything it did not spawn
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
  `--hello-timeout`). `--issue` prepends an issue to the brief and is
  **forge-aware** (#245, `cmd/spinclass/spawn_issue.go`): a bare number resolves
  the TARGET repo's forge via `internal/repoinfo` and dispatches to
  `gh issue view` (GitHub) or `fj api` (Gitea/Forgejo/Codeberg); a full issue
  URL (`https://<host>/<owner>/<repo>/issues/<n>`) is fetched from ITS host
  whatever the target's remote says. An unsupported or unresolvable forge is a
  hard error, never a gh fallback — a read-only GitHub mirror would answer with
  stale text and silently produce a wrong brief. Workers **may** spawn their
  own workers — the #148 one-level restriction was removed, since the
  always-ask floor (#151) already prevents silent fan-out at every depth and
  was the actual protection; the tree is no longer guaranteed flat, and
  reaping authority stays immediate-parent-only. Coordination
  afterward is clown chat. `internal/spawn` owns resolution + launch.
  **Reaping** (#249, `cmd/spinclass/close_child_cmd.go`): `close-child-session`
  lets a driver tear down a worker it spawned — completed children and failed
  spawns (a hello timeout leaves the worktree + state on disk by design) would
  otherwise pile up in `sc list` with no path for the agent that owns them.
  Authorization is the child's `spawned_by` lineage: `authorizeChildReap`
  requires it to equal the caller's own `currentSessionKey()`, so a foreign or
  never-spawned session is refused with both keys named. Teardown itself is
  plain `close.RunResolved` — the unintegrated/dirty check stays there, and a
  non-TTY MCP caller gets its `--force` refusal instead of a prompt.
  **Permission posture** splits on `force`: a clean reap auto-approves (the
  worst it can do is remove a fully-integrated worktree the caller spawned),
  while `force: true` is always-ask because it discards uncommitted changes
  and unmerged commits. The rule lives in `perms.AlwaysAsk` — one predicate
  shared by both enforcement surfaces (the PreToolUse hook and the perms-tier
  `RunCheck`), since duplicating it would seam a security floor. It judges an
  *invocation*, not a tool, which is why it takes tool input; a perms tier
  cannot draw this line itself because `BuildPermissionString` discards
  arguments for MCP tools, so allow-listing the tool would otherwise grant
  force too. `force` is read fail-closed: only absent, `null`, or boolean
  `false` count as safe. Elicitation could replace the flag entirely for the
  MCP path (#254).
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
  `.spinclass/job.log`, metadata → `.spinclass/job.json`). The `*-async` tools
  (and `session-job-cancel`) are registered **only under clown** — an async job
  IS a ringmaster job (#243), so without clown there is nothing to observe or
  wake on and only the synchronous merge/check exist. Inspect a running job via
  ringmaster's own `job_status`/`job_read`, or block with `job_wait`, using the
  id the start tool returns; `session-job-cancel` stops it. spinclass's own
  `session-job-wait` and `session-job-status` were retired in favour of those
  ringmaster surfaces (#21, #23). One active job per session. **Default to the
  synchronous tools** — reach for async only when you have other work to do
  while the hook runs; never start async then hot-poll.
- **Clown job-wakeup emits** (FDR 0010, `internal/clown`): the `ringmaster start`
  allocation happens **before** the job goroutine launches, so the id returned at
  dispatch IS the id the completion wake carries (#243) — previously the two
  diverged (`merge-<unix-ts>` vs ringmaster's `merge-<hash>`, same prefix) and
  agents read their own wake as a sibling's. Dispatch-time allocation is what
  `ringmaster start` is for (~7ms, measured). A failed allocation under clown
  **refuses the dispatch**: a job with no wake completes into silence. Without
  clown the caller's local id stands and there is simply no wake. When `serve` runs
  under clown (`CLOWN_BIN` set), async tools emit `ringmaster start/done`
  (clown's job-control CLI, clown RFC-0015) so clown wakes the agent with one
  `[clown-job]` line. **Runtime** resolution is PATH or `$RINGMASTER_BIN`,
  never a build-time pin — the wake must land in the journal of the clown
  hosting this process, not one spinclass froze at its own build time. But
  ringmaster IS a flake input as a **checkPhase** dep (#253): it puts the real
  binary in the sandbox so `internal/clown/contract_test.go` drives an actual
  start → spool-path → done lifecycle against a scratch `XDG_STATE_HOME`.
  Before that no lane had the binary and the contract had never been tested —
  the stub suites (`clown_test.go`, `internal/job/wake_test.go`) can only
  confirm the argv spinclass *intends* to send, since a stub accepts anything.
  The suite skips without a binary but **fails hard** inside `NIX_BUILD_TOP`
  if the pin is dropped, so coverage cannot silently return to zero.
  job.json/job.log stay the system of record; rollback is
  `CLOWN_DISABLE_JOB_WAKEUP=1`.
  **Self-sufficient wakes** (#251 piece 2): the hook's output is teed into
  ringmaster's spool (2a) so `status --tail`/`tail -f` show a running job, and
  the terminal emit attaches the rendered verdict ladder as a
  `madder://blobs/<digest>` via `done --resource` (2b, `storeResultBlob`).
  Resource and `result_ref` are **alternatives**: with a blob the blob IS the
  result, so `result_ref` is only emitted as the fallback for a build with no
  madder pin. Blob failures degrade to no attachment and are logged — the job
  has already finished, so failing a wake over a missing attachment would
  trade a working notification for none. Note plain `ringmaster read` renders
  attachments as a count (`· 2 resource(s)`); `--json` is needed to get the
  URIs.
- **Per-job liveness flock + ProtocolVersion gate** (#26, ringmaster RFC-0016 +
  RFC-0018, `internal/clown`): `serve` holds ringmaster's per-job advisory lock
  (`jobwake.AcquireJobLock`, wrapped by `clown.AcquireJobLock`) for an async
  job's lifetime, so a crashed serve is *detectable* — the OS releases the lock
  on process death, the probe reports `gone`, and ringmaster's reaper writes
  `interrupted` instead of leaving the job stuck in `running`. This is why
  `jobwake` is now **build-time LINKED** (the `gomod.nix` bridge onto
  ringmaster's `go-pkgs`, the tommy pattern) *in addition to* the runtime PATH
  CLI of the previous bullet — the flock must be an **in-process fd**, since a
  CLI subprocess would drop it the instant it exits. Keep both: the CLI still
  emits/observes at runtime (PATH-resolved, FDR 0010), the linked library
  supplies the flock and the version constant. The flock is gated by a
  **ProtocolVersion** check: `clown.CheckProtocol` (memoized, one shell-out per
  serve) compares the compiled-in `jobwake.ProtocolVersion` against
  `ringmaster version --protocol`. On an **exact** match, serve-start calls
  `clown.SetFlockEnabled(true)` and `job.Start` acquires the flock (releasing it
  in `clearRunning` *after* the terminal record, preserving "lock held ⟺ job
  running"). On a mismatch — the linked lib's lock-path derivation may not match
  the running ringmaster's probe — it **degrades loudly and skips only the
  flock**: a warning to stderr + `servelog` + a ⚠ line prepended to the
  system-prompt fragment, and `FlockEnabled` stays false so `job.Start` reads
  the flag and skips the acquire. Async wakes and cancel-observe are pure CLI
  shell-outs (self-consistent with the running binary), so they are **unaffected**
  by a skew — only crash auto-reap is lost until versions align. `job.Start`
  reads the cached flag, never re-shelling `version --protocol` per dispatch.
  The core contract is `TestCheckProtocolMatchesRealRingmaster` (against the
  checkPhase-pinned binary): it fails loudly if the linked `ProtocolVersion`
  ever drifts from the pinned ringmaster's runtime value.
- **Cancellation observer** (#22, ringmaster RFC-0018, `internal/job` +
  `internal/clown`): `ringmaster cancel` is cooperative — it records a
  **non-terminal `cancel-requested`** and wakes, but signals no process. So for
  each async job under clown, `job.Start` launches an observer goroutine that
  blocks in `clown.WaitForCancel` (`ringmaster wait <id> --on-cancel
  --timeout 0`) and, on a cancel-requested, fires the job's context cancel —
  tearing down the hook exactly as `session-job-cancel` and the inactivity
  watchdog do. The producer then writes the terminal itself, staying the **sole
  terminal-writer** (no double-write, no journal lie). The cancel terminal is
  now **`StatusAborted`** (`"aborted"`, RFC-0018), renamed from `cancelled`; the
  reaper writes `interrupted` if serve dies mid-teardown. `WaitForCancel` is
  CLI-based (**not** the linked `jobwake.WaitDoneOnCancel`) and gated on
  `clown.Enabled()` alone — **not** `FlockEnabled` — so cancel observation
  survives a `ProtocolVersion` skew, where the in-process flock is disabled but
  the runtime ringmaster's cancel surface still works. NON-OBVIOUS (verified
  live, locked by `TestWaitForCancelObservesRealCancelRequested`): `wait
  --on-cancel`'s status reports the DERIVED state `running` for a cancel-requested
  job (the record is non-terminal), never `cancel-requested` — so `WaitForCancel`
  maps a **non-terminal** return to `cancelRequested=true`, since --on-cancel's
  only non-terminal stop condition is a cancel-requested. The observer runs
  under the job's own ctx, so `clearRunning`'s `cancel()` tears down its
  `ringmaster wait` subprocess when the job ends by any other path (no separate
  cancel to track — which also sidesteps a govet `lostcancel` false positive).
  `clearRunning` then **joins** the observer (`<-observerDone`) after `cancel()`
  and before closing `done`, so a woken `WaitDone` caller is guaranteed the
  observer subprocess is already reaped — cancel() SIGKILLs it, so the join
  cannot hang. Without the join a completed job could report done with the
  subprocess still alive; in tests that surfaced as the stub still writing into a
  `t.TempDir()` during `RemoveAll` (an `ENOTEMPTY` race seen only under the eng
  checkPhase's load, never in a faster devshell).
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
- **Stacked / queued intra-session merges** (FDR 0025, #265): a second
  `merge-this-session-async` while a gate runs ENQUEUES the next batch
  (in-process per-worktree queue, `cmd/spinclass/merge_queue.go`) rather than
  refusing. A queued entry records **no pin**: at dequeue it re-runs
  `PrepareMerge`+`FinishMerge` fresh, so git patch-id dedup drops the
  already-landed prior batch. `job.OnJobDone` drives `processMergeQueue`: a
  failed merge drains the queue (aborted wake naming the culprit), a succeeded
  merge or completed check dequeues the next. The **attestation** is consumed
  only once a merge is committed (dispatch or enqueue), so a refusal never
  burns it (`attestation.Peek`/`Consume` split from `Check`). Worktree sessions
  only; queued merges carry no ringmaster job id (the wake signals);
  `[hooks].disable-merge-stacking` is the rollback.
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
  survived teardown, else `repoPath`; no build worktree. Bounded by
  `[hooks].post-merge-timeout` — a **wall-clock** cap, **on by default at 10m**
  (#246), because a wedged hook holds the repo's queue, not just its session;
  `"0"` disables, a bad value falls back to the default rather than to no cap.
  Publishes `SPINCLASS_MERGED_SHA` (the **landing** sha
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
- **Hook cancellation** (#188, `sweatfile.runHookInDirEnv`): `cmd.Cancel` is
  overridden to **SIGTERM**, not exec's default SIGKILL, so a cancelled hook
  can tear down its own children. The argv collapses by exec
  (`direnv exec … sh -c <script>` → the hook command itself), so SIGKILL used
  to orphan the `nix` below `just` still holding the inherited pipe — and
  `Wait` cannot return until every pipe holder closes it (measured: 224s).
  `cancelGrace` (10s) is the SIGKILL escalation for a hook that swallows
  SIGTERM. Deliberately **no `Setpgid`**: a group kill would reap the detached
  children FDR 0023 sanctions for slow post-merge deploys.
- **Pre-merge hook systemd scope** (#25, ringmaster#12/RFC-0016 §3-4,
  `internal/clown` + `sweatfile.runHookInDirEnv`): the `no Setpgid` decision
  above leaves a residual — a hook whose top process swallows SIGTERM orphans
  its descendants. So the **pre-merge** hook runs inside a transient systemd
  scope: `clown.ScopeArgv(jobID)` prepends `systemd-run --user --scope
  --unit=ringmaster-<id>.scope --property=KillMode=control-group --` (outermost,
  after the direnv wrap), and on cancel `runHookInDirEnv` calls
  `clown.ScopeStop` to reap the whole cgroup — the backstop above the #188
  SIGTERM/`WaitDelay` floor, which stays. The reap is prompt: ringmaster#16 made
  `ScopeArgv` set `--property=TimeoutStopSec=3s` on the scope AND `ScopeStop`
  force-kill via `systemctl --user kill --signal=SIGKILL <unit>`, so a
  SIGTERM-*ignoring* subtree is reaped well within the ~10s `ScopeStop` ctx (the
  first cut, against a stop-only ScopeStop with systemd's ~90s default
  stop-timeout, let the stubborn child survive the ctx — `TestRunPreMergeHook
  ScopeReapsSubtreeOnCancel` guards the regression). Producer-
  called and **RFC-0016 §4.2-safe**: ringmaster only supplies the argv + unit
  name (`ScopeUnitName`, the single derivation site), never decides to kill and
  is not in the `status` path, so cancel and status stay platform-uniform. The
  job id reaches the hook via a ctx value — `clown.WithJobID` set in `job.Start`,
  read by `runHookInDir` via `clown.JobIDFromContext`; this scopes **only** the
  pre-merge hook (post-merge calls `runHookInDirEnv` directly with `""` so its
  FDR-0023 detached children are never caught in the control-group kill; repair/
  create/attach/detach run under `context.Background` so carry no id). Async is
  clown-only, so a job id is present exactly when there is a ringmaster job to
  scope. Availability-gated by `jobwake.ScopeArgv` (systemd-run on PATH +
  reachable user manager + session bus, off under `RINGMASTER_DISABLE_SCOPE`);
  unavailable ⇒ the hook runs **bare** and the #26 flock stays the liveness
  floor. The wrap-active path needs a systemd user bus, absent in the nix
  checkPhase sandbox and on macOS, so CI covers only the disabled/bare path +
  the unit-name derivation + the ctx threading; the active path is dogfooded.
- **Base-branch freshening at creation** (#250, `internal/basebranch`): a fresh
  session's branch is cut from the repo's **default branch**, fetched and
  fast-forwarded first, passed to `git worktree add -b` as an explicit sha.
  Fixes two independent defects: `-b` with no start-point bases on **HEAD** (so
  a checkout parked on a feature branch handed that branch to every session),
  and only *some* paths freshened anything — `shop.Attach` pulled the main
  worktree, but `spawn.Launch` calls `shop.Create` directly and so did nothing,
  which is the case most likely to be stale. The retired `pullMainWorktree` was
  also aimed wrong: it pulled the checkout's *current* branch (so it could
  advance a feature branch) and skipped silently on a dirty tree. The gate lives
  in `shop.createWorktree` — the single funnel below start/spawn/run — so the
  spawn gap closes by construction. `sc fork` and `start-gh_pr` are excluded
  (both have a base by intent). **Ahead of upstream is NOT staleness** (local
  contains upstream; the state of every repo after a `--local-only` merge) and
  never refuses. Unreachable, dirty-blocking-the-ff, and diverged do refuse,
  overridable by `--allow-stale-base` or `[hooks].allow-stale-base`; there is
  deliberately **no MCP parameter**, so a driver cannot wave away its worker's
  stale toolchain. `sc resume` runs the same step advisorily and cannot fail on
  it. Two mechanics worth not re-deriving: the fetch uses an explicit
  `+refs/heads/<b>:refs/remotes/<r>/<b>` refspec (whether a bare
  `git fetch <remote> <branch>` also updates the tracking ref depends on
  `remote.<name>.fetch` covering it, and the ancestry check reads that ref
  back), and it is context-bounded with `GIT_TERMINAL_PROMPT=0` +
  `ssh -o BatchMode=yes` because ssh and credential helpers read `/dev/tty`, not
  stdin — a nil `Stdin` is not protection, and a hang there surfaces only as a
  spawn hello-deadline expiry with nothing to explain it.
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
0001): it maps bridged Go modules (`tommy`, `crap`, `ringmaster`) onto their
producer flakes' `go-pkgs` outputs, threaded as `goFlakeInputs`. Bump a bridged
dep with `nix flake update <input>` — no gomod2nix lockstep unless the new rev
changes the producer's own dependency graph. `ringmaster` is bridged at the
repo-root module (no `subPath`, the tommy shape); it is *also* a checkPhase
`nativeCheckInputs` binary and a devShell package, so the same flake rev backs
the linked `jobwake` library, the contract-test binary, and the dev-loop CLI.

## Dependencies

Module: `code.linenisgreat.com/spinclass`.

- `code.linenisgreat.com/tap/go` — TAP-14 output (non-merge/check commands).
- `code.linenisgreat.com/crap/go-crap/v2` — ndjson-crap reader + the
  merge/check output stack (`crap.Reporter` emission, `viewport` presentation;
  wired in `internal/present`).
- `code.linenisgreat.com/purse-first/libs/go-mcp` — MCP server framework
  (`command.App` does CLI dispatch + MCP serving; no cobra).
- `code.linenisgreat.com/tommy` — TOML library.
- `code.linenisgreat.com/ringmaster/pkgs/jobwake` — the linked half of the
  job platform: `AcquireJobLock` (per-job liveness flock) + `ProtocolVersion`
  (the serve-start compatibility pin), consumed by `internal/clown` (#26), plus
  the scope-tier helpers `ScopeArgv`/`ScopeStop`/`ScopeUnitName` (#25). The
  runtime CLI is resolved from PATH, not this import.
