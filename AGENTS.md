# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

The deep design rationale for most subsystems lives in `docs/features/` (FDRs)
and the `spinclass-*(5)`/`(7)` manpages — this file orients you and points there
rather than duplicating it. Your system prompt already staples a live **Design
records** index (every FDR/doc by number·title·status, via FDR 0021) into
context, so a bare `(FDR NNNN)` cite here is enough to locate the record — read
it before changing that subsystem.

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
  `sc fork [branch]`               Fork current worktree into a new branch (`--from <dir>`); create-only (the `--brief` detached worker was removed in #262)
  `sc spawn [repo] --brief "…"`    Launch a detached worker session; `repo` optional — omitted = this repo, else a sibling (FDR 0006, #262)
  `sc close-child-session <child>` Reap a worker THIS session spawned (#249); refuses anything it did not spawn
  `sc resurrect <target> [--new-branch]` Recreate a closed session's worktree+branch from its captured commit (#291)
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

- **Spawned workers** (FDR 0006, #262): `sc spawn [repo] --brief` + the
  `spawn-session` MCP tool launch a detached harness-booted worker. `repo` is
  optional — omitted (or the current repo) spawns HERE, a sibling dirname/path
  targets that repo; the worker always starts fresh off the target's default
  branch with only the brief (fork-at-HEAD dropped; `fork-session` / `sc fork
  --brief` removed, create-only `sc fork` stays). `spawn-session` is ASYNC under
  clown (#266): returns session key + ringmaster job id, delivers the hello (or a
  reap-if-dead timeout) as a wake; `sc spawn` CLI blocks. The brief is the
  worker's ONLY context (#258). Workers may spawn workers (the #151 always-ask
  floor, not a depth cap, guards fan-out). `internal/spawn` owns resolution +
  launch (`LaunchDetached`/`WaitHello`). **Reaping** (#249): `close-child-session`
  tears down a worker you spawned; `authorizeChildReap` gates on
  `spawned_by == caller`, refusing foreign/never-spawned children. Force-reap
  discards uncommitted/unmerged work, so a clean reap auto-approves while
  `force: true` is always-ask — one `perms.AlwaysAsk` predicate shared by the
  PreToolUse hook and the perms-tier `RunCheck` (it judges an *invocation*, not a
  tool, since `BuildPermissionString` discards MCP args); `force` is read
  fail-closed. Elicitation could replace the flag (#254).
- **Resurrecting a closed session** (FDR 0027, #291, `internal/resurrect`): the
  undo half of `sc close`/`close-child-session`. Both funnel through
  `close.RunResolved`, which now best-effort resolves the branch's tip
  (`git.RevParse(wtPath, "HEAD")`) immediately before force-deleting the
  worktree/branch and threads it into `session.Tombstone`'s new `sha` param
  as `State.DeletedSHA`. `sc resurrect <target>` (+ MCP tool, no
  spawned-lineage gate — recovery isn't the privileged operation reaping is)
  resolves the target via `session.FindByTarget` (tombstones included, same
  as close/resume), refuses cleanly if it's not a tombstone, has no
  `DeletedSHA` (predates this feature, or closed outside spinclass — recover
  via `git reflog`), or the commit is `!git.CommitExists` (likely gc'd), then
  calls `worktree.Create(repoPath, path, "", DeletedSHA)` — the same
  arbitrary-base-commit primitive `sc start`'s base-branch freshening
  uses — and `session.Write` (documented to overwrite a stale tombstone) to
  re-register it inactive, carrying the description/spawn-lineage forward.
  Does not attach — `sc resume` afterward reuses that path unmodified.
  `sc clean`'s merged-worktree removal does NOT capture a SHA (that content
  already lives in the default branch).
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
  `check-this-session-async` consume the attestation, launch a background
  goroutine in `serve`, return a job id immediately (output → `.spinclass/job.log`,
  meta → `.spinclass/job.json`). Registered **only under clown** — an async job
  IS a ringmaster job (#243); inspect via ringmaster's `job_status`/`job_read` or
  block with `job_wait`, `session-job-cancel` to stop. One active job per session.
  **Default to the synchronous tools**; async only when you have other work while
  the hook runs, and never hot-poll.
- **Clown job-wakeup emits** (FDR 0010, `internal/clown`): async merge/check emit
  `ringmaster start/done` (clown RFC-0015) so clown wakes the agent with one
  `[clown-job]` line. The job id is allocated at dispatch, so the id returned to
  the caller IS the id the wake carries (#243); a failed allocation under clown
  **refuses the dispatch** (a wake-less job completes into silence). Runtime
  ringmaster is PATH/`$RINGMASTER_BIN`, never build-pinned (the wake must land in
  the hosting clown's journal); it IS a flake input as a **checkPhase** dep (#253)
  so `contract_test.go` drives a real start→done lifecycle. **Self-sufficient
  wakes** (#251): hook output tees into ringmaster's spool, and the verdict ladder
  attaches as a `madder://blobs/<digest>` via `done --resource` (blob and
  `result_ref` are alternatives; `ringmaster read --json` to get the URIs).
  Rollback `CLOWN_DISABLE_JOB_WAKEUP=1`.
- **Per-job liveness flock + ProtocolVersion gate** (#26, ringmaster RFC-0016/
  0018, `internal/clown`): `serve` holds ringmaster's per-job advisory lock
  (`jobwake.AcquireJobLock`) for an async job's life, so a crashed serve is
  detectable — the OS drops the lock, the reaper writes `interrupted` instead of
  a stuck `running`. This is why `jobwake` is build-time **LINKED** (the
  `gomod.nix` bridge) in ADDITION to the runtime PATH CLI: the flock must be an
  in-process fd. Gated by `clown.CheckProtocol` (memoized) comparing compiled-in
  `jobwake.ProtocolVersion` to `ringmaster version --protocol`: exact match
  enables the flock, a mismatch degrades **loudly** and skips only the flock
  (async wakes + cancel-observe are pure CLI shell-outs, unaffected by skew — only
  crash auto-reap is lost). `TestCheckProtocolMatchesRealRingmaster` guards the
  drift against the checkPhase-pinned binary.
- **Cancellation observer** (#22, ringmaster RFC-0018, `internal/job` +
  `internal/clown`): `ringmaster cancel` is cooperative — it records a
  non-terminal `cancel-requested` and wakes, signalling no process. So each async
  job runs an observer goroutine (`clown.WaitForCancel`, gated on `clown.Enabled()`
  alone so it survives a ProtocolVersion skew) that fires the job ctx cancel on a
  cancel-requested; the producer writes the terminal itself (sole terminal-writer;
  terminal is `StatusAborted`, reaper writes `interrupted` if serve dies
  mid-teardown). NON-OBVIOUS (locked by
  `TestWaitForCancelObservesRealCancelRequested`): `wait --on-cancel` reports the
  derived state `running` for a cancel-requested job, so a non-terminal return
  maps to cancelRequested=true. `clearRunning` joins the observer after `cancel()`
  (which SIGKILLs its `ringmaster wait` subprocess) before closing `done`, so a
  woken caller is guaranteed the subprocess is already reaped.
- **Dynamic system-prompt fragment** (spinclass#187, FDR 0021, clown plugin
  protocol RFC-0002 §5, `internal/sysprompt`): `serve` answers `prompts/get` for
  `system-prompt-append`; clown's stdio bridge fetches it **before `initialize`**
  and appends it last. `sysprompt.Resolve` branches worktree-session vs
  main-checkout (implicit session, FDR 0014) and `Render` picks the embedded
  template. Both carry best-effort, **deadline-capped** (`repoFetchTimeout` 2s —
  the pre-`initialize` fetch must never block) lines: a **repository line**
  (`internal/repoinfo` — git remote + a PAPI/`gh`/Gitea lookup for forge kind,
  vanity-remote owner (#221), description), a **Forge workflow** block (non-GitHub
  forge → use `fj`/`smith`), a **co-active sessions** line (#238; local state +
  PID only), and the Go-composed **Design records** trailer (`docsindex.go`)
  indexing `docs/features`/`adrs`/`rfcs` by number·title·status (dirs overridable
  via `[sysprompt].doc-index-dirs`; a `recover()` guarantees a broken doc never
  fails the render). Replaces the retired static
  `.clown-plugin/system-prompt-append.d/` fragments.
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
- **Per-repo merge queue** (FDR 0022, #235): `FinishMerge` serializes landings on
  an advisory flock (`internal/mergelock`; `spinclass-merge.lock` in the shared
  git common dir — poll-based, ctx-cancellable, self-releasing). Acquired BEFORE
  the gate; under the lock: re-pull → ancestry check → if the tip moved, rebase
  the pinned commits in a transient `.land-*` worktree (conflict ⇒
  `merge.ErrIntegrationConflict`, resolved by a plain re-merge) → gate on the
  LANDING sha → ff-only → teardown → push. Worktree merges only; lock is
  host-local. `[hooks].disable-merge-queue` restores the pre-#235 fail-on-race
  path.
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
- **`post-merge` hook** (FDR 0023, #244): `[hooks].post-merge` runs after a merge
  fully lands (ff-only done; pushed when gitSync). On the queued path it runs
  **under the landing lock**, as the last stage before `FinishMerge` returns — a
  merge is exclusive end to end so no sibling deploy interleaves (#244; a slow hook
  extends that exclusive region). Non-fatal: a nonzero exit emits a `severity=warn`
  point but does NOT fail the merge (nothing to roll back). Bounded by
  `[hooks].post-merge-timeout`, a **wall-clock** cap ON by default at 10m (#246;
  `"0"` disables). Publishes `SPINCLASS_MERGED_SHA` (the LANDING sha on a rebased
  landing), `_MERGED_BRANCH`, `_DEFAULT_BRANCH`, `_MERGE_PUSHED`, `_REPO_PATH`.
  All land paths fire it; `sc check` never does. `disable-post-merge` opts out.
- **Named post-merge targets + verify** (FDR 0026, #273): a top-level
  `[[post-merge]]` array (the `[[mcps]]`/`[[remotes]]` idiom; NOT `[[hooks.post-merge]]`
  — TOML can't union the `post-merge` key as both a string and an array, and
  retiring the string would be a flag-day) of `{name, command, verify?}` targets.
  `verify` runs iff `command` exits 0; per-target verdict ∈ `ok`/`command-failed`/
  `verify-failed` (the split a human needs). Named targets SUPERSEDE the legacy
  `[hooks].post-merge` string (dormant, not dead — stanza-by-stanza migration). A
  name-only entry is a removal sentinel (like `[[mcps]]`). `merge-this-session(-async)`
  gains a `targets` param (nil=all, `[]`=none, subset=those; unknown name fails
  PRE-landing) and `sc merge` gains `--post-merge-targets`/`--no-post-merge`. Targets
  run **concurrently** (spinclass#276), each as its own reporter **Phase node**
  (crap's muxing model, like the pre-merge hook): output streams as node-tagged
  `Output` records, verdict rides on the `node_end` (`✓/✗ post-merge <name>`), the
  ndjson writer serializes the wire, and the viewport demuxes — no hand-rolled
  prefixer. Nodes are allocated up front in declaration order (deterministic
  ladder; `crap.Reporter`'s id counter/err field aren't goroutine-guarded, so a
  shared mutex guards the goroutines' output/close + the raw job-log tee). One
  shared `post-merge-timeout` (size for max(), not sum — lock-hold = slowest
  single target); a deadline kill → `verdict=timeout`. A failed node is
  `severity=warn` and non-fatal (merge still succeeds); #259's wake-surfacing
  lifts every `✗ post-merge` line. Each target snapshots the deadline/cancel
  state at Run-return so a sibling's later timeout can't mislabel its failure.
  The legacy `[hooks].post-merge` string stays a result-family test point (the
  superseded single-command shim). Code: `sweatfile.PostMergeTarget`/`ActivePostMergeTargets`/
  `PostMergePhaseActive` + `PostMergeTarget.Run` (verdicts), `merge.runNamedPostMergeTargets`/
  `merge.postMergeFailDiag`/`merge.postMergeTargetSink`/`selectPostMergeTargets`,
  `validate.CheckPostMergeTargets`. Fields are phase-neutral
  (no git) — paves toward the operator's "configurable pipeline phases" direction;
  per-target `paths` filters deferred (would bake in a git-diff assumption).
- **Pre-merge REPAIR phase** (FDR 0018): when `[hooks].repair` is set,
  `PrepareMerge` runs it in the **session worktree** before the pin to fold
  mechanical fixes into the merged commit (canonical
  `conformist --commit --amend --exit-zero-on-fix`; amend detected via HEAD-sha
  delta). Merge-only; worktree sessions only. spinclass's own sweatfile has
  **retired** this in favour of the per-commit hook below.
- **Per-commit repair hook** (FDR 0019, #183, #267): `[hooks].pre-commit`
  installs a per-worktree git pre-commit hook (`internal/sweatfile/precommit.go`)
  so drift is repaired at authoring time. Canonical value is the store-pinned
  wrapper **`conformist-pre-commit`** (not a bare `conformist --staged` —
  conformist#51). `.spinclass/hooks` (via `core.hooksPath`) is a composing
  dispatcher that runs the formatter then execs the native hook. **#267**: it
  bakes `git hash-object flake.lock` at install; on a commit, if the live lock
  hash differs (a flake bump since the session froze) it re-evals the formatter
  via `nix develop --command` — building the current devShell if needed (a
  blocking commit, accepted) — so a stale toolchain can't restamp generated
  files. The baked hash never self-updates (that would fast-path back to the
  stale hook); `sc resume` regenerates it fresh. `disable-pre-commit` uninstalls
  (rollback). Worktree sessions only.
- **Inactivity watchdog**: `[hooks].inactivity-timeout` (Go duration; unset =
  off) bounds how long the pre-merge hook may go silent. Covers
  merge/check/`sc check` + async twins (all funnel through
  `RunPreMergeHookContext`). Distinct error message vs `session-job-cancel`.
- **Hook cancellation** (#188, `sweatfile.runHookInDirEnv`): `cmd.Cancel` is
  **SIGTERM** not SIGKILL, so a cancelled hook can tear down its own children
  (exec collapses the argv, so SIGKILL orphaned the `nix` below `just` still
  holding the inherited pipe — `Wait` blocks until every pipe holder closes it).
  `cancelGrace` (10s) escalates to SIGKILL. Deliberately **no `Setpgid`**: a group
  kill would reap the detached children FDR 0023 sanctions.
- **Pre-merge hook systemd scope** (#25, ringmaster#12/RFC-0016, `internal/clown`
  + `sweatfile.runHookInDirEnv`): to backstop the no-`Setpgid` residual (a hook
  swallowing SIGTERM orphans descendants), the pre-merge hook runs in a transient
  systemd scope — `clown.ScopeArgv(jobID)` prepends `systemd-run --user --scope`
  (outermost), and cancel calls `clown.ScopeStop` to force-kill the whole cgroup
  (ringmaster#16 sets `TimeoutStopSec=3s` + a SIGKILL `systemctl kill`, so a
  SIGTERM-ignoring subtree dies within the ~10s ctx; `TestRunPreMergeHookScope
  ReapsSubtreeOnCancel` guards it). Scopes **only** the pre-merge hook (job id via
  the `clown.WithJobID` ctx value; post-merge passes `""` so its FDR-0023 detached
  children survive). Availability-gated by `jobwake.ScopeArgv`; unavailable ⇒ bare
  hook, the #26 flock stays the liveness floor. Active path needs a systemd user
  bus (absent in the checkPhase sandbox + macOS) so it is dogfooded, not CI.
- **Base-branch freshening at creation** (#250, `internal/basebranch`): a fresh
  session's branch is cut from the repo's **default branch**, fetched +
  fast-forwarded first, passed to `git worktree add -b` as an explicit sha —
  fixing `-b`'s base-on-HEAD (a checkout parked on a feature branch) and the spawn
  path that freshened nothing (`spawn.Launch` → `shop.Create` bypassed the old
  main-worktree pull). Gate lives in `shop.createWorktree`, the single funnel
  below start/spawn/run. **Ahead-of-upstream is NOT staleness** and never refuses;
  unreachable, dirty-ff-blocked, and diverged refuse, overridable by
  `--allow-stale-base` / `[hooks].allow-stale-base` — deliberately **no MCP
  parameter** (a driver can't wave away a worker's stale toolchain). `sc fork` /
  `start-gh_pr` excluded. Fetch is context-bounded with `GIT_TERMINAL_PROMPT=0` +
  `ssh -o BatchMode=yes` (ssh/cred helpers read `/dev/tty`, so a nil Stdin isn't
  protection — a hang surfaces only as a spawn hello-deadline expiry).
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
0001): it maps bridged Go modules (`tommy`, `crap`, `ringmaster`,
`purse-first/libs/dewey`) onto their producer flakes' `go-pkgs` outputs,
threaded as `goFlakeInputs`. Bump a bridged dep with `nix flake update <input>`
— no gomod2nix lockstep unless the new rev changes the producer's own
dependency graph. `ringmaster` is bridged at the repo-root module (no
`subPath`, the tommy shape); it is *also* a checkPhase `nativeCheckInputs`
binary and a devShell package, so the same flake rev backs the linked
`jobwake` library, the contract-test binary, and the dev-loop CLI.
`purse-first` is a direct input (not just ringmaster's transitive one, which
predates `pkgs/mesa` — hence the `ringmaster.inputs.purse-first.follows`
override) so `sc list`'s pretty/plain rendering can bridge `libs/dewey`
(mesa, #185); same shape as clown's `gomod.nix`.

## Dependencies

Module: `code.linenisgreat.com/spinclass`.

- `code.linenisgreat.com/tap/go` — TAP-14 output (non-merge/check commands).
- `code.linenisgreat.com/crap/go-crap/v2` — ndjson-crap reader + the
  merge/check output stack (`crap.Reporter` emission, `viewport` presentation;
  wired in `internal/present`).
- `code.linenisgreat.com/purse-first/libs/go-mcp` — MCP server framework
  (`command.App` does CLI dispatch + MCP serving; no cobra).
- `code.linenisgreat.com/purse-first/libs/dewey/pkgs/mesa` — the List-Table
  NDJSON renderer (RFC 0003), bridged via `gomod.nix`. `sc list`'s
  pretty/plain rendering (`cmd/spinclass/list_view.go`) builds a `mesa.Table`
  and renders it styled (`--format` unset on a TTY, and `--watch`) or plain
  (piped / `--format tap`); `--format json` stays the original
  `session.ListRow` array, untouched by this migration (#185).
- `code.linenisgreat.com/tommy` — TOML library.
- `code.linenisgreat.com/ringmaster/pkgs/jobwake` — the linked half of the
  job platform: `AcquireJobLock` (per-job liveness flock) + `ProtocolVersion`
  (the serve-start compatibility pin), consumed by `internal/clown` (#26), plus
  the scope-tier helpers `ScopeArgv`/`ScopeStop`/`ScopeUnitName` (#25). The
  runtime CLI is resolved from PATH, not this import.
