# #188: cancel the pre-merge hook on the synchronous and CLI paths

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to
> implement this plan task-by-task. Tasks 1–2 are independent. Task 3 needs
> Task 2. Tasks 4–5 need Tasks 2–3. Task 6 is independent. Task 7 (the
> `require-hook-scope` knob) is independent. Task 8 (scope-spawn fallback)
> needs Tasks 2, 6 and 7 (same function, same lines). Task 9 (docs, the closing
> commit) goes last.

**Goal:** A cancelled `merge-this-session` / `check-this-session` / `sc merge` /
`sc check` / `sc run` must stop the pre-merge hook's process tree **before** its
`.merge-*` build worktree is removed, on every path spinclass controls.

## Decisions (2026-10-04)

Operator decisions, folded into the tasks below.

1. **A failed systemd scope spawn falls back to the bare hook, with a warn
   point, by default.** A hard failure is OPT-IN via the new
   `[hooks].require-hook-scope` (Task 7). Trajectory, to be stated in the docs
   (Task 9): opt-in now, later the default (flipped to an opt-out such as
   `allow-unscoped-hook`), eventually the fallback is removed. It applies
   uniformly to every path that scopes the hook (sync, CLI, async): today none
   of them falls back (see item 8 under "What is still broken"). Task 8.
2. **A cancel after landing SIGTERMs the post-merge deploy on the sync and CLI
   paths. ACCEPTED.** It matches async `session-job-cancel` today. Document it
   (Task 9); no code.
3. **Stdin EOF on `serve` does NOT cancel in-flight calls, and that is
   intended: merges are session-durable / session-agnostic.** An in-flight sync
   merge survives the client closing the session. Only a signal to `serve` (or,
   later, purse-first#200 per-request cancel) aborts it, and then the hook tree
   is torn down rather than orphaned. Verified, purse-first
   `libs/go-mcp/server/server.go`: `Run` spawns each message in a goroutine
   tracked by `s.wg` (`:74-79`); on `io.EOF` it calls `gracefulShutdown()` and
   returns nil (`:63-68`); `gracefulShutdown` does `s.wg.Wait()` before
   `transport.Close()` (`:100-105`). The server ctx is cancelled only by the
   `cancel` deferred at `:43` (on return) or by the caller's ctx. Captured by a
   characterization test (Task 4) and the docs (Task 9).
4. **SIGHUP: `sc merge` / `sc check` / `sc run` treat it like SIGINT/SIGTERM
   (cancel + cleanup). `serve` does NOT cancel on SIGHUP; it discards it.**
   Reasoning: `serve` is a stdio child of clown's MCP bridge, not a terminal
   foreground process. If the user's terminal drops, SIGHUP can still reach it
   (same session/process group as the client, unless the launcher `setsid`s it)
   and the default action kills it with no cleanup, orphaning the hook, and
   would contradict decision 3 (the client going away must not abort a merge).
   Discarding SIGHUP (`signal.Notify` to a drained channel, NOT `signal.Ignore`,
   which children inherit across exec) lets the stdin EOF that follows trigger
   the normal drain. Unverified: how clown actually sends shutdown (Open
   question 2). Tasks 4-5.
5. **`sc clean` removing a dead-pid `.merge-*` dir that still has live
   processes: OUT OF SCOPE.** See Non-goals / Out of scope (spinclass#350).

**What is already done (do not redo):**

- `hookrun.runHookInDirEnv` (`internal/hookrun/hookrun.go:345-431`) sets
  `c.Cancel` = SIGTERM (`:410`) and `c.WaitDelay = cancelGrace` (10s, `:411`,
  const at `:179`). No `Setpgid`, on purpose (FDR 0023 detached post-merge
  children must survive; doc comment `:340-344`).
- A systemd scope wraps the pre-merge hook when the ctx carries a job id
  (`:302`, `:387-393`), and `clown.ScopeStop` runs after a cancelled `Run`
  (`:417-429`). The id is set in exactly one place: `internal/job/runner.go:156`,
  i.e. only for an **async** job **under clown**.
- The build worktree is removed by `defer cleanup()` after the hook returns
  (`internal/check/check.go:252-256`, the remover at `:327`).
- `internal/hookrun/cancel_test.go` pins the hook-layer behaviour
  (`TestPreMergeCancelTearsDownChildren`,
  `TestPreMergeCancelEscalatesPastIgnoredSIGTERM`).

**What is still broken (verified in code):**

1. **Sync MCP handlers drop the ctx.** `handleMergeThisSession(_ context.Context, …)`
   (`cmd/spinclass/commands_mcp_only.go:395`) calls `merge.Resolved` (`:464`),
   which hardcodes `context.Background()` (`internal/merge/merge.go:123-125`).
   `handleCheckThisSession(_ …)` (`:486`) calls `check.Run` (`:501`), likewise
   (`internal/check/check.go:74-76`). `ResolvedContext` (`merge.go:140`) and
   `RunContext` (`check.go:91`) already exist.
2. **No scope on the sync path.** No job id is ever in a sync ctx.
3. **`serve` dies on SIGTERM.** Its only handler is
   `signal.NotifyContext(ctx, os.Interrupt)` (`cmd/spinclass/commands_mcp.go:31`).
   SIGTERM kills the process with no deferred cleanup: the hook is reparented
   and keeps running in a `.merge-*` dir nobody owns.
4. **The CLI has no signal handling.** `cmd/spinclass/main.go:31` passes a bare
   `context.Background()`. `sc merge` (`commands_session.go:170-197` →
   `merge.Run`, `merge.go:44`), `sc check` (`:206-224`) and `sc run` (`:116-140`
   → `run.Run`, `internal/run/run.go:85`, merge at `:205`) all discard it.
   Ctrl-C kills `sc` and leaves `.merge-*` behind.
5. **The orphan's cwd is deleted later.** `sc clean` removes any `.merge-*`
   whose embedded pid is dead (`internal/clean/clean.go:385-411`, `:447-459`)
   without checking for live processes inside. Together with 3/4 this
   reproduces the issue's `cannot get cwd` signature.
6. **No tests** cancel through the sync handlers or the check/merge layers.
7. **The unscoped escalation is silent.** With no scope tier and a
   SIGTERM-ignoring hook, `WaitDelay` kills only the top process and the
   worktree is removed with descendants possibly alive (the acknowledged
   residual, `hookrun.go:342-344`). Nothing tells the operator.
8. **A scope spawn failure fails the gate on EVERY scoped path, contrary to the
   comment promising otherwise.** `clown.ScopeArgv`'s doc says "a spawn failure
   still falls back to the bare command" (`internal/clown/clown.go:391-392`),
   but `ScopeArgv` is only an availability pre-check (`:393-395`), and
   `runHookInDirEnv` just returns `c.Run()`'s error (`hookrun.go:416,430`).
   The async path is identical: `internal/job/runner.go:156` only attaches the
   job id to the ctx; the same `runHookInDir` (`hookrun.go:302`) reads it, so
   an async job whose `systemd-run` fails also fails with no fallback. Nothing
   differs between sync and async today; Task 8 changes both together.

Tasks cite these as "item N".

**Architecture:** thread the caller's ctx through the three sync entry points;
give every synchronous gate a locally generated **scope id** under its own ctx
key; make `serve` and the gate CLI commands turn SIGINT/SIGTERM into a ctx
cancel; emit one honest warning line when the unscoped escalation fires.

**Tech stack:** Go, module `code.linenisgreat.com/spinclass`. `os/exec`,
`os/signal`, `ringmaster/pkgs/jobwake` (linked), crap reporter.

**Build/test:** scoped runs only: `just debug-go-test <dir> [run-regex]`.
`git add -N` every new file first (godyn sees only tracked paths). No bats case
is added, so no bats lane is needed. Do NOT run full `just`: the merge gate
runs it.

**Rollback:** revert the commits. `RINGMASTER_DISABLE_SCOPE=1` turns the scope
tier off at runtime (`jobwake.scopeAvailable`, ringmaster
`internal/0/jobwake/scope.go:111-113`); everything else then behaves as Tasks
2–5 minus the scope.

---

## What this does NOT fix (read before promising anything)

**A client rejecting one sync tool call still will not cancel the hook.**
Verified in the local purse-first checkout (`libs/go-mcp`):

- `server/handler.go:64-72` treats `notifications/cancelled` as a no-op.
- `server/server.go:74-79` runs every handler with the single server-wide ctx
  from `Run` (`:41-43`). Nothing cancels it per request.

That is **purse-first#200** (per-request cancellation). Until it lands, the
ctx spinclass now honours is cancelled only when `serve` itself is told to
stop. What Tasks 2–3 buy today:

- SIGINT or SIGTERM to `serve` cancels an in-flight sync hook, reaps its tree,
  and removes the build worktree, instead of orphaning it.
- The moment purse-first#200 ships and the `purse-first` flake input is bumped,
  a rejected tool call cancels the hook with no further spinclass change. Task
  2's handler tests already drive that path (they cancel the handler's ctx).

**Merges are session-durable (Decision 3).** Closing the client session (stdin
EOF) does not cancel an in-flight sync merge: `serve` drains in-flight handlers
(purse-first `server/server.go:63-68`, `:100-105`) and the merge finishes. This
is a feature, not a gap; do not "fix" it by cancelling on EOF. Only a signal to
`serve` or (later) purse-first#200 aborts a merge, and then the hook tree is
reaped, not orphaned.

**So the closing commit says `Refs #188`, not `Closes #188`.** Reasoning: the
issue's title and trigger are "the tool call is interrupted (rejected
mid-flight)". That exact trigger is still a no-op at the transport. The
shutdown path fixes the *consequence* the issue describes (an orphan outliving
its worktree) for the cases where `serve` or `sc` is signalled, which is
plausibly how the reported orphan arose (inference, see Open questions 1), but
it does not make a reject cancel anything. Close #188 from the commit that
bumps `purse-first` past #200, after re-running Task 2's tests against it.

## Design notes

1. **Item 2: a separate ctx key for the scope id, not `WithJobID`.**
   - Evidence that reuse would *work today*: the only non-test reader of
     `clown.JobIDFromContext` is `internal/hookrun/hookrun.go:302`. The spool
     tee (`internal/job/runner.go:185-198`), the flock and the done-emit all
     use the local `clownID` variable, never the ctx.
   - Evidence that the id need not be a ringmaster job:
     `jobwake.ScopeUnitName` is plain concatenation, `"ringmaster-" + id +
     ".scope"`, and "does not validate" (ringmaster
     `internal/0/jobwake/scope.go:29-38`). `ScopeArgv` (`:56-64`) and
     `ScopeStop` (`:94-100`) touch only systemd, never the journal. ringmaster
     itself never calls them (`:12-22`, RFC-0016 §4.2).
   - Why a separate key anyway: `WithJobID`'s contract is "the async job id"
     (`internal/clown/clown.go:417-435`). A synthetic id under that key is a
     trap for the next reader that passes it to `ringmaster status`/`SpoolPath`,
     where `validateJobID` accepts the shape and the lookup then fails as a
     missing journal. Ten lines buy that safety.
   - Format constraints: the id must match ringmaster's job-id grammar
     `^[A-Za-z0-9._-]{1,128}$` (ringmaster `internal/0/jobwake/producer.go:15`,
     RFC-0017 §3: a subset of the systemd unit-name-safe set). It must be
     **unique per hook run**: `systemd-run --unit=` refuses an existing unit,
     and `runHookInDirEnv` has no bare fallback on a spawn failure today (`:416`
     just returns `c.Run()`'s error; Task 8 adds one), so a collision would
     fail the gate.
   - Shape: `sync-<kind>-<pid>-<unixnano>`, e.g. `sync-merge-41234-1759612345678901234`.
2. **Where the local scope id is attached:** at the entry points (the two sync
   handlers and the three CLI commands), via `clown.WithLocalScope(ctx, kind)`.
   Not in `check.RunWithReporterContext`: an async job already carries its own
   id, and a funnel-level default would hide which paths are scoped.
3. **Items 3–4: the signal ctx is scoped to `merge`, `check` and `run`, plus
   `serve` (which uses a smaller signal set, see Decision 4). It is NOT installed globally in `main.go`.** Almost every other
   `RunCLI` ignores its ctx (`RunCLI: func(_ context.Context, …)` throughout
   `commands_session.go`). A global `NotifyContext` would catch the first
   Ctrl-C and cancel a ctx nobody reads, so those commands would stop
   responding to Ctrl-C. `sc resume`/`sc start` exec an entrypoint with its own
   SIGHUP forwarding (`internal/executor/session.go:90-112`) and must keep
   default SIGINT/SIGTERM. huh prompts run in raw mode and read Ctrl-C as a
   key; `merge.Run` resolves every prompt (`chooseWorktree`,
   `ResolveDefaultBranch`, `merge.go:94-106`) before the reporter scope, so the
   signal ctx is installed for the whole command without touching them.
4. **Second Ctrl-C force-exits.** The helper calls `stop()` as soon as the ctx
   is done (`context.AfterFunc(ctx, stop)`), which restores the default
   disposition. The first signal starts the ≤10s teardown; a second one kills
   `sc` outright (and then leaves `.merge-*` for `sc clean`, as today).
5. **Item 7: warn, do not kill.** No `Setpgid`. When a cancelled, **unscoped**
   hook needed the escalation, write one line into the hook output saying
   descendants may still be running.
6. **`PrepareMerge` stays un-cancellable.** It takes no ctx and uses
   `context.Background()` for its fetch (`merge.go:182`). It rebases the
   session worktree; interrupting that mid-rebase is worse than waiting. A
   cancel that arrives during it takes effect at `FinishMerge`'s first ctx
   check (`mergelock.Acquire`, `merge.go:435`).

## Non-goals

- Per-request cancellation in go-mcp (purse-first#200).
- `Setpgid` / process-group kills (FDR 0023).
- Scoping or cancelling the post-merge hook differently. It keeps passing `""`
  (`hookrun.go:159-166`).
- Cancelling on stdin EOF (Decision 3: merges are session-durable).
- Cancelling `serve` on SIGHUP (Decision 4).

## Out of scope

- Making `sc clean` check for live processes inside a dead-pid `.merge-*`
  before removing it (item 5): spinclass follow-up issue spinclass#350.

---

### Task 1: characterization tests for cancel through `check` and `merge` (item 6)

**Promotion criteria:** N/A.

**Context:** `check.RunContext` and `merge.ResolvedContext` already honour a
ctx; nothing tests it. These tests are expected **GREEN on first run**. They
pin the layer behaviour Tasks 2–4 rely on, and fail if someone later reorders
`defer cleanup()` ahead of the hook's exit.

**Files:**
- Test: `internal/check/check_cancel_test.go` (new; package `check`, reuse
  `setupRepoWithWorktree` `check_test.go:36`, `writeSweatfile` `:105`,
  `decodeRecords` `:113`)
- Test: `internal/merge/merge_cancel_test.go` (new; reuse `setupRepo`
  `merge_test.go:46`, `setupWorktree` `:78`)

**Shared fixture (write it once per package as a helper `cancelHook(t, dir)`):**
write `<dir>/hook.sh`:

```sh
sleep 600 &
child=$!
echo $child > "$PIDFILE"
trap 'kill -TERM $child 2>/dev/null; exit 143' TERM
pwd > "$CWDFILE"
touch "$STARTED"
wait $child
```

with the three paths substituted as absolute paths under `t.TempDir()`. The
sweatfile is `[hooks]\npre-merge = "sh <dir>/hook.sh"\n`. Every test calls
`t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")` (deterministic on a dev host with a
user bus) and registers `t.Cleanup(func(){ syscall.Kill(pid, syscall.SIGKILL) })`.

**Step 1: write the tests**

- `TestRunContext_CancelReapsHookBeforeRemovingBuildWorktree` (check):
  1. `go func(){ _, err = RunContext(ctx, rep, wtPath, nil); close(done) }()`.
  2. Wait for `STARTED`; read the pid; read `CWDFILE`.
  3. Assert the recorded cwd's basename has prefix `BuildWorktreePrefix` and
     the dir exists.
  4. `cancel()`. Assert `done` closes within 30s and `err != nil`.
  5. Assert the child pid is dead (`syscall.Kill(pid, 0) != nil`, poll ≤10s).
  6. Assert the recorded build dir no longer exists, and no entry under
     `<repo>/.worktrees/` has the `BuildWorktreePrefix`.
- `TestResolvedContext_CancelDuringHookLandsNothing` (merge): one commit ahead
  on the branch, local-only (`gitSync=false`), `inSession=true`. Same
  start/cancel dance through `ResolvedContext`. Assert: error non-nil; child
  dead; no `.merge-*` and no `.land-*` under `.worktrees/`; `main` still at its
  original sha (`runGit(t, repoDir, "rev-parse", "main")` before and after).

**Step 2: run**

- `git add -N internal/check/check_cancel_test.go internal/merge/merge_cancel_test.go`
- `just debug-go-test internal/check 'TestRunContext_Cancel'`
- `just debug-go-test internal/merge 'TestResolvedContext_Cancel'`

Expected: PASS. If either is RED, stop and report: that is a new bug in the
already-shipped half of #188, not part of this plan.

**Step 3: commit**

`test(check,merge): pin hook cancellation through the check and merge layers (#188, task 1)`

---

### Task 2: a scope id of its own, and a local one for synchronous gates (item 2, part 1)

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/clown/clown.go:417-435` (add beside `WithJobID`)
- Modify: `internal/hookrun/hookrun.go:296-303` (`runHookInDir`)
- Test: `internal/clown/scope_test.go`
- Test: `internal/hookrun/hooks_test.go`

**Step 1: failing tests**

- `TestScopeIDFromContext` (clown): bare ctx → `""`; `WithJobID(ctx, "merge-9f3c1a2b")`
  → `"merge-9f3c1a2b"` (the async path keeps working); `WithScopeID(ctx, "x")`
  → `"x"`; both set → the explicit scope id wins; and
  `JobIDFromContext(WithScopeID(bg, "x")) == ""` (the synthetic id never leaks
  into the job-id key).
- `TestLocalScopeIDIsUniqueAndUnitSafe` (clown): two `LocalScopeID("merge")`
  calls differ; each matches `^[A-Za-z0-9._-]{1,128}$` and has prefix
  `sync-merge-`.
- `TestPreMergeScopeIDFromContextWrapsInCgroup` (hookrun): copy
  `TestPreMergeScopeActiveWrapsInCgroup` (`hooks_test.go:183-206`) but build
  the ctx with `clown.WithLocalScope(context.Background(), "check")` and read
  the id back with `clown.ScopeIDFromContext`. Self-skips without a user bus,
  like its model.

**Step 2: run, expect a compile failure** (`undefined: clown.WithScopeID`):
`just debug-go-test internal/clown 'TestScopeID|TestLocalScopeID'`

**Step 3: implement**

```go
// scopeIDKey carries the id of the transient systemd scope the pre-merge hook
// runs under when the caller is NOT an async ringmaster job (spinclass#188).
// Kept apart from jobIDKey so a synthetic id is never mistaken for a job id.
type scopeIDKey struct{}

func WithScopeID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, scopeIDKey{}, id)
}

// ScopeIDFromContext is the id the pre-merge hook's scope is named after: an
// explicit WithScopeID, else the async job id, else "".
func ScopeIDFromContext(ctx context.Context) string {
	if id, _ := ctx.Value(scopeIDKey{}).(string); id != "" {
		return id
	}
	return JobIDFromContext(ctx)
}

var localScopeSeq atomic.Uint64

// LocalScopeID mints a scope id for a synchronous gate. Unique per call
// (systemd refuses a duplicate unit name) and within ringmaster's job-id
// grammar, which ScopeUnitName assumes.
func LocalScopeID(kind string) string {
	return fmt.Sprintf("sync-%s-%d-%d-%d", kind, os.Getpid(), time.Now().UnixNano(), localScopeSeq.Add(1))
}

// WithLocalScope gives a synchronous merge/check its own hook scope, unless
// ctx already names one.
func WithLocalScope(ctx context.Context, kind string) context.Context {
	if ScopeIDFromContext(ctx) != "" {
		return ctx
	}
	return WithScopeID(ctx, LocalScopeID(kind))
}
```

In `runHookInDir` replace `clown.JobIDFromContext(ctx)` with
`clown.ScopeIDFromContext(ctx)` and update the comment at `:297-301`: the id is
the async job id or a synchronous gate's local scope id. Rename the
`scopeJobID` parameter of `runHookInDirEnv` to `scopeID` and fix its doc
comment (`:316-322`). `kind` only ever receives the literals `merge`, `check`,
`run`.

**Step 4: run, expect PASS**

- `just debug-go-test internal/clown`
- `just debug-go-test internal/hookrun 'TestPreMergeScope'`
- `just debug-go-test internal/job` (async path unchanged)

**Step 5: commit**

`feat(clown): scope id ctx key and local scope ids for synchronous gates (#188, task 2)`

---

### Task 3: sync MCP handlers honour their ctx and scope the hook (items 1, 2, 6)

**Promotion criteria:** `merge.Resolved` and `check.Run` (the
`context.Background()` wrappers) stay for tests; no production caller remains
after Task 5.

**Files:**
- Modify: `cmd/spinclass/commands_mcp_only.go:395`, `:464-475`, `:486`, `:501`
- Test: `cmd/spinclass/sync_cancel_test.go` (new; package `main`; reuse
  `gatedWorktreeFixture` `merge_queue_test.go:36`, `readAttestation` `:453`,
  `commitFile` `:463`; model on `TestCheckThisSessionConsumesOnlyOnGreen` `:194`)

**Step 1: failing tests** (fixture and env as in Task 1; the sweatfile body is
`"[hooks]\npre-merge = \"sh <dir>/hook.sh\"\n\n" + gateSweat`)

- `TestHandleCheckThisSessionCancelReapsHook`: run
  `handleCheckThisSession(ctx, json.RawMessage(`{}`), nil)` in a goroutine;
  wait for `STARTED`; `cancel()`. Assert within 30s: the handler returned;
  `res.IsErr`; the child pid is dead; no `.merge-*` under
  `<repoPath>/.worktrees/`; `readAttestation` returns a non-nil attestation
  with `Claim == nil` (a cancel releases, never consumes — #219).
- `TestHandleMergeThisSessionCancelReapsHookAndLandsNothing`: `commitFile`
  first, args `{"local_only":true}`. Same assertions, plus the default branch
  sha is unchanged and no `.land-*` remains.

**Step 2: run, expect FAIL** with "handler did not return within 30s" (the
handler ignores the ctx; the `t.Cleanup` SIGKILL of the child then lets the
leaked goroutine finish):

- `git add -N cmd/spinclass/sync_cancel_test.go`
- `just debug-go-test cmd/spinclass 'TestHandle(Check|Merge)ThisSessionCancel'`

**Step 3: implement**

- `handleMergeThisSession(ctx context.Context, …)`: just before the reporter is
  built, `ctx = clown.WithLocalScope(ctx, "merge")`; replace `merge.Resolved(`
  with `merge.ResolvedContext(ctx, …, gitSync, true, nil, pm)` (the extra `nil`
  is `activity`, see the signature at `merge.go:140`).
- `handleCheckThisSession(ctx context.Context, …)`:
  `check.RunContext(clown.WithLocalScope(ctx, "check"), rep, cwd, nil)`.
- Leave `landed`/`passed` and the deferred `hold.settle` untouched: a cancel is
  a non-nil error, so the claim is released.

**Step 4: run, expect PASS**

- `just debug-go-test cmd/spinclass 'TestHandle(Check|Merge)ThisSessionCancel|TestCheckThisSessionConsumesOnlyOnGreen'`
- `just debug-go-test cmd/spinclass` (whole package; the handlers are widely
  exercised)

**Step 5: commit**

`fix(mcp): synchronous merge/check cancel their pre-merge hook with the request ctx (#188, task 3)`

---

### Task 4: `serve` turns SIGINT/SIGTERM into a cancel, discards SIGHUP (item 3, Decisions 3-4)

**Promotion criteria:** N/A.

**Files:**
- Create: `cmd/spinclass/signal_context.go`
- Modify: `cmd/spinclass/commands_mcp.go:31-32`
- Test: `cmd/spinclass/signal_context_test.go` (new)
- Test: `cmd/spinclass/serve_durability_test.go` (new; characterization)

**Step 1: failing tests**

- `TestGateSignalContextCancelsOnSIGTERM`: `ctx, stop := gateSignalContext(context.Background(), os.Interrupt, syscall.SIGTERM)`;
  `defer stop()`; `syscall.Kill(os.Getpid(), syscall.SIGTERM)`; assert
  `<-ctx.Done()` within 5s. (Safe: the handler is installed, so the test
  process is not killed.)
- `TestGateSignalContextCancelsOnSIGINT`: same with `syscall.SIGINT`.
- `TestServeSignalsDoNotIncludeSIGHUP` / `TestCLIGateSignalsIncludeSIGHUP`:
  assert the two exported-in-package signal sets (`serveSignals` =
  INT+TERM, `cliGateSignals` = INT+TERM+HUP) by membership. Plus
  `TestGateSignalContextCancelsOnSIGHUP` using `cliGateSignals`.
- `TestDiscardSIGHUPKeepsProcessAliveAndCtxLive`: call `discardSIGHUP()`
  (returns a stop func), `syscall.Kill(os.Getpid(), syscall.SIGHUP)`, sleep
  100ms, assert the process is alive and a `serveSignals` ctx is NOT done.
- `TestServeDrainsInFlightHandlerOnStdinEOF` (characterization, expected GREEN;
  Decision 3): stand up a go-mcp `server.Server` over an in-memory pipe
  transport with one stub tool that blocks until released and records whether
  its ctx was cancelled; send the call, close the client side (EOF), assert
  `Run` has NOT returned and the handler ctx is live; release the handler and
  assert `Run` returns nil after the response is written. It pins purse-first
  `server/server.go:63-68,100-105` so a `purse-first` bump that changes EOF
  semantics fails loudly. If go-mcp's transport constructor is not usable from
  spinclass tests, reduce this to a comment citing the lines above in
  `serve_durability_test.go` and say so in the commit body.
- `TestGateSignalContextRestoresDefaultAfterFirstSignal`: after `ctx.Done()`,
  poll ≤2s until `signal.Ignored(syscall.SIGTERM) == false` **and** a fresh
  `signal.NotifyContext(context.Background(), syscall.SIGTERM)` still works
  (i.e. the helper's registration was released, not leaked). Do **not** send a
  second real signal in-process.

**Step 2: run, expect a compile failure:**
`git add -N cmd/spinclass/signal_context.go cmd/spinclass/signal_context_test.go`
then `just debug-go-test cmd/spinclass 'TestGateSignalContext'`

**Step 3: implement**

```go
var (
	// serveSignals cancel in-flight tool calls. NOT SIGHUP: merges are
	// session-durable (see discardSIGHUP).
	serveSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	// cliGateSignals are for sc merge|check|run, which own a terminal.
	cliGateSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
)

// gateSignalContext returns a ctx cancelled by the first of sigs, so a running
// pre-merge hook is torn down and its build worktree removed instead of being
// orphaned (spinclass#188). The registration is dropped as soon as the ctx is
// done, restoring the default disposition: a SECOND signal kills the process
// outright rather than waiting on the teardown.
func gateSignalContext(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, sigs...)
	context.AfterFunc(ctx, stop)
	return ctx, stop
}

// discardSIGHUP swallows SIGHUP for the life of serve. serve is a stdio child
// of the client; a dropped terminal must neither kill it mid-merge (default
// action: no cleanup, orphaned hook) nor cancel it (merges outlive the client
// session; the stdin EOF that follows drains in-flight calls). signal.Notify to
// a drained channel, NOT signal.Ignore: an ignored disposition is inherited
// across exec and would make every hook ignore SIGHUP.
func discardSIGHUP() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		for range ch {
		}
	}()
	return func() { signal.Stop(ch); close(ch) }
}
```

In `registerServeCommand` replace
`signal.NotifyContext(ctx, os.Interrupt)` with
`gateSignalContext(ctx, serveSignals...)` and `defer discardSIGHUP()()`.
Nothing else changes: `srv.Run(sigCtx)` already hands that ctx to every handler
(purse-first `server/server.go:74-79`), and `gracefulShutdown` waits for the
in-flight handler (`:100-105`), which now returns within about `cancelGrace`.

**Step 4: run, expect PASS:** `just debug-go-test cmd/spinclass 'TestGateSignalContext|TestDiscardSIGHUP|TestServe|TestCLIGateSignals'`

**Step 5: commit**

`fix(serve): SIGTERM cancels in-flight tool calls instead of orphaning their hooks; SIGHUP is discarded (#188, task 4)`

---

### Task 5: `sc merge`, `sc check`, `sc run` cancel on Ctrl-C / SIGTERM / SIGHUP (item 4, Decision 4)

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/merge/merge.go:44` (`Run` → add `RunContext`), `:111`
- Modify: `internal/run/run.go:85` (`Run(spec)` → `Run(ctx, spec)`), `:178`,
  `:205`, `:244-266` (`runStep`)
- Modify: `cmd/spinclass/commands_session.go:116-140` (run), `:170-197`
  (merge), `:206-224` (check)
- Test: `internal/run/run_test.go` (or the package's existing test file)
- Test: `internal/merge/merge_cancel_test.go`

**Step 1: failing tests**

- `TestRunStepCancelTerminatesTheStep` (run): drive `runStep` with a ctx and a
  util of `sh -c 'touch <started>; sleep 600'`; cancel after `started` appears;
  assert `runStep` returns a non-nil error within 15s. RED today: `runStep`
  takes no ctx.
- `TestRunContextCancelDuringHook` (merge): chdir into the worktree
  (`t.Chdir`), format `"ndjson"`, target `""`, `gitSync=false`. Start
  `RunContext(ctx, executor.ShellExecutor{}, "ndjson", "", false, PostMergeOptions{})`
  in a goroutine with `os.Stdout` redirected to a temp file; cancel after
  `STARTED`. Assert: non-nil error, child dead, no `.merge-*`. RED today:
  `RunContext` does not exist.

**Step 2: run, expect compile failures:**
`just debug-go-test internal/run 'TestRunStepCancel'` and
`just debug-go-test internal/merge 'TestRunContextCancel'`

**Step 3: implement**

- `merge.go`: rename the body of `Run` to
  `RunContext(ctx context.Context, execr, format, target, gitSync, pm)`; keep
  `func Run(...) error { return RunContext(context.Background(), ...) }`. Line
  `:111` becomes `ResolvedContext(ctx, execr, rep, ts, …, inSession, nil, pm)`.
- `run.go`: `Run(ctx context.Context, spec Spec)`; `:205` becomes
  `merge.ResolvedContext(ctx, …, spec.NoClose, nil, merge.PostMergeOptions{…})`;
  `runStep(ctx, rep, st, spec)` replaces `cmd.Run()` with:

  ```go
  if err = cmd.Start(); err == nil {
      stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Signal(syscall.SIGTERM) })
      err = cmd.Wait()
      stop()
  }
  ```

  (The step keeps its default foreground-group SIGINT from the terminal; this
  adds SIGTERM-to-`sc` propagation, so installing the handler does not make the
  step unkillable.)
- `commands_session.go`, each of the three `RunCLI`s: take `ctx` instead of
  `_`, then

  ```go
  ctx, stop := gateSignalContext(ctx, cliGateSignals...)
  defer stop()
  ctx = clown.WithLocalScope(ctx, "merge") // "check" / "run"
  ```

  and call `merge.RunContext(ctx, …)`, `check.RunContext(ctx, rep, cwd, nil)`,
  `run.Run(ctx, spec)`. In `run` and `exec`-style branches that call
  `os.Exit`, call `stop()` first (deferred functions do not run on `os.Exit`).

**Step 4: run, expect PASS**

- `just debug-go-test internal/run`
- `just debug-go-test internal/merge 'TestRunContextCancel|TestResolvedContext_Cancel'`
- `just debug-go-test cmd/spinclass`

**Step 5: manual check (the implementer reports the result, does not skip):**
in a scratch repo with `pre-merge = "sleep 300"`, run `sc check`, press Ctrl-C
once. Expect: `sc` exits nonzero within ~1s, `pgrep -f 'sleep 300'` is empty,
`ls .worktrees/` shows no `.merge-*`. Repeat with `--format viewport` on a TTY
(Open question 1), and once more closing the terminal (SIGHUP) instead of
Ctrl-C: same expectations.

**Step 6: commit**

`fix(cli): sc merge/check/run cancel the pre-merge hook and remove its build worktree on SIGINT/SIGTERM/SIGHUP (#188, task 5)`

---

### Task 6: say so when an unscoped cancel may have left descendants (item 7)

**Promotion criteria:** N/A.

**Context:** with no scope tier (macOS, no user bus, the nix sandbox,
`RINGMASTER_DISABLE_SCOPE`), a hook whose top process swallows SIGTERM is
SIGKILLed alone after `cancelGrace`; its descendants survive and the build
worktree is removed under them. That stays true (no `Setpgid`). Today it is
silent.

Two signals mean "the escalation fired", and both are needed:

- `errors.Is(err, exec.ErrWaitDelay)`: the top exited by itself but a
  descendant still held the pipe when `WaitDelay` expired. A survivor is
  certain.
- the top process was SIGKILLed: `c.ProcessState.Sys().(syscall.WaitStatus)`
  with `Signaled() && Signal() == syscall.SIGKILL`. `Wait` then returns the
  `*exec.ExitError`, **not** `ErrWaitDelay`, so that check alone would miss it.

**Files:**
- Modify: `internal/hookrun/hookrun.go:416-430`
- Test: `internal/hookrun/cancel_test.go`

**Step 1: failing test**

`TestPreMergeCancelWarnsWhenUnscopedEscalationFires`: `t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")`;
the hook from `TestPreMergeCancelEscalatesPastIgnoredSIGTERM` (`:91-115`:
`trap '' TERM; touch started; sleep 600`). After `done`, assert `buf.String()`
contains `may still be running`. Also extend
`TestPreMergeCancelTearsDownChildren` (`:33`) with the negative: its output
must **not** contain that phrase (a hook that honoured SIGTERM is not warned
about).

**Step 2: run, expect FAIL:** `just debug-go-test internal/hookrun 'TestPreMergeCancel'`

**Step 3: implement** — after `err := c.Run()`:

```go
if !scoped && ctx.Err() != nil && escalated(c, err) {
	_, _ = fmt.Fprintf(w, "[spinclass] hook ignored SIGTERM for %s and was killed; "+
		"with no systemd scope its child processes may still be running (spinclass#188)\n",
		c.WaitDelay)
}
```

with `escalated` a small unexported helper implementing the two signals above.
It applies to **every** cancelled hook, not only pre-merge: the post-merge
path is always unscoped, and the line is equally true there. Update the doc
comment's "Residual" sentence (`:342-344`) to mention the warning.

**Step 4: run, expect PASS**

- `just debug-go-test internal/hookrun`

  (the post-merge cancel tests, e.g.
  `TestPostMergeCallerCancelIsNotReportedAsTimeout`, must stay green; if one
  asserts exact output, relax it to ignore the new line rather than
  suppressing the line).

**Step 5: commit**

`feat(hookrun): warn when a cancelled hook needed SIGKILL with no scope to reap its children (#188, task 6)`

---

### Task 7: `[hooks].require-hook-scope` sweatfile knob (item 8, Decision 1)

**Promotion criteria:** per the trajectory in Decision 1 this knob is a
transitional opt-in. When the default flips it is replaced by an opt-out
(e.g. `allow-unscoped-hook`) and finally removed with the fallback.

**Why this name:** existing `[hooks]` booleans are `disable-*` (turn a default
behaviour off) and `allow-*` (relax a default refusal), e.g. `disable-merge-queue`,
`allow-stale-base`, `allow-no-credential` (`sweatfile.go:85-99`). This knob
*tightens* a lenient default, so neither prefix fits; `require-` says exactly
that, and when the default flips the knob inverts naturally to `allow-*`.

**Files:**
- Modify: `internal/sweatfile/sweatfile.go:99` (field
  `RequireHookScope *bool \`toml:"require-hook-scope"\``) and an accessor beside
  `AllowNoCredential` (`:777-788`), same shape:
  `func (sf Sweatfile) RequireHookScope() bool`
- Modify: `internal/sweatfile/hierarchy.go` (scalar override in `MergeWith`,
  next to `AllowNoCredential`, `:155-156`)
- Regenerate: `internal/sweatfile/sweatfile_tommy.go` (generated by
  `//go:generate tommy generate`, `sweatfile.go:264`; do NOT hand-edit). Run
  `just build-tommy-codegen` (justfile `:66`) to regenerate, `git add` the
  result; `just verify-tommy-codegen` (`:116`) is the pure drift check the merge
  gate runs. See AGENTS.md "Sweatfile config".
- Doc: `doc/spinclass-sweatfile.5.scd` (entry after `*allow-no-credential*`,
  `:675-685`; escape `_` as `\_`)
- Test: `internal/sweatfile/sweatfile_test.go`

**Step 1: failing tests**

- `TestRequireHookScopeDefaultsFalse`: nil Hooks, nil field, `false` all report
  false.
- `TestRequireHookScopeParsesAndMerges`: `[hooks]\nrequire-hook-scope = true`
  parses to true; a repo-layer `false` overrides a global `true` (scalar
  override); an absent repo layer inherits `true`.
- `TestRequireHookScopeRoundTrips`: encode/decode through the tommy codec keeps
  the key (guards a stale regen).
- Validate: `validate.CheckUnknownFields` (`validate.go:605`) is driven by the
  codec's `Undecoded()`, so a regenerated codec is what stops
  `require-hook-scope` being flagged `unknown field`. Add
  `TestCheckUnknownFieldsAcceptsRequireHookScope` in `internal/validate`.

**Step 2: run, expect a compile failure:**
`just debug-go-test internal/sweatfile 'TestRequireHookScope'`

**Step 3: implement** the field, accessor, merge line, regenerate the codec,
write the manpage entry: *Boolean. When true, a pre-merge hook whose systemd
scope cannot be set up fails the gate instead of running unscoped with a
warning (the default). Transitional: the unscoped fallback is deprecated and
this opt-in is expected to become the default, then the fallback removed.
Merge: scalar override.* Note that the setup fingerprint
(`internal/setupfingerprint`) hashes the merged config, so adding the field
flags existing worktrees stale once; expected.

**Step 4: run, expect PASS**

- `just debug-go-test internal/sweatfile`
- `just debug-go-test internal/validate`
- `just debug-go-test internal/setupfingerprint`

**Step 5: commit**

`feat(sweatfile): [hooks].require-hook-scope opt-in for a hard scope-setup failure (#188, task 7)`

---

### Task 8: a failed scope setup falls back to the bare hook with a warn point (item 8, Decision 1)

**Promotion criteria:** transitional. Removed when the fallback is removed
(Decision 1 trajectory); this task's tests then flip to assert failure.

**Context:** one code path serves sync, CLI and async
(`runHookInDirEnv`, `hookrun.go:345-431`), so changing it makes all of them
uniform. Today a failing `systemd-run` fails the gate everywhere (item 8).

**Telling "scope never set up" from "hook ran and failed".** `systemd-run
--scope` execs the hook as its own child after setup, and exits with the hook's
status, so a bare exit code (1) cannot distinguish "scope refused" from "hook
exited 1". The fallback must NEVER re-run a hook that started. Detection, in
order of reliability:

1. **`c.Start()` fails** (systemd-run not spawnable: ENOENT/EACCES). Nothing
   ran; safe to fall back. Requires splitting `c.Run()` into `Start` + `Wait`.
2. **A start marker written inside the scope, immediately before the hook.**
   Wrap the scoped argv's payload as
   `sh -c 'touch "$SPINCLASS_SCOPE_MARK" && exec "$@"' sh <direnv/sh argv…>`
   with `SPINCLASS_SCOPE_MARK` a path in a per-run temp dir. After a nonzero
   exit: marker present => the scope was set up and the hook (or its wrapper)
   started => report the failure, never retry; marker absent => the payload
   never ran => safe to fall back. The `exec` keeps `cmd.Process` collapsing to
   the hook as the doc comment at `:324-332` requires. Residual: a failure
   between marker and `exec` is classified "started" and surfaces as a gate
   failure, which is the safe direction.
3. If (2) proves intrusive in review, the narrowest safe alternative is a
   pre-flight probe: run `systemd-run … --unit=<id>-probe true` first and fall
   back on a nonzero probe. It cannot see a failure that appears only on the
   real run (that surfaces as a hook failure, still never a double run).

Also never fall back when `ctx.Err() != nil` (a cancel is not a spawn failure).

**Files:**
- Modify: `internal/hookrun/hookrun.go:345-431`; `PreMergeInDir` (`:208`) reads
  `sf.RequireHookScope()` and passes it down (new parameter on
  `runHookInDirEnv`, `false`/irrelevant for post-merge, which is unscoped)
- Modify: `internal/clown/clown.go:391-392` (correct the "spawn failure still
  falls back" comment to describe where the fallback now lives)
- Test: `internal/hookrun/scope_fallback_test.go` (new)

**Step 1: failing tests** (a fake `systemd-run` on `PATH` makes them
deterministic; `RINGMASTER_DISABLE_SCOPE` must be unset and `ScopeArgv` must
report available, so also stub whatever availability pre-check `jobwake`
performs, or build the test around an injectable `scopeArgv` func var in
hookrun, which is the recommended seam)

- `TestScopeSetupFailureFallsBackToBareHookWithWarn`: the seam returns an argv
  prefix whose first element is a script that exits 1 without running its
  payload (marker absent). Hook = `touch ran`. Assert: the gate returns nil;
  `ran` exists exactly once; the output contains `[spinclass] hook scope
  unavailable (...); running unscoped` (the warn point, a `severity=warn` line
  in the reporter path, plain text at the hookrun layer); no
  `require-hook-scope` mention.
- `TestScopeSetupFailureFailsGateWhenRequireHookScope`: same fake, with
  `sf.Hooks.RequireHookScope=true` via `PreMergeInDir`. Assert: error non-nil,
  the error names `require-hook-scope`, and `ran` does NOT exist.
- `TestScopedHookFailureIsNotRetried`: the seam returns a prefix that creates
  the marker then execs the payload; the hook is `echo x >> count; exit 3`.
  Assert: error non-nil (exit 3 surfaced) and `count` has exactly one line (a
  started hook is never re-run).
- `TestScopeSpawnErrorFallsBack`: the prefix's argv[0] does not exist (Start
  error). Same assertions as the first test.
- `TestCancelDuringScopedHookDoesNotFallBack`: cancel the ctx after the marker
  appears; assert no second run.

**Step 2: run, expect FAIL** (today: error returned, no warn):
`git add -N internal/hookrun/scope_fallback_test.go`, then
`just debug-go-test internal/hookrun 'TestScope.*Fall|TestScopedHookFailure|TestCancelDuringScoped'`

**Step 3: implement** `Start`/`Wait` split plus the marker wrapper inside the
scoped branch only; on classified setup failure and `!require`, write the warn
line to `w` (including the first line of systemd-run's captured stderr if
available), rebuild the unscoped `exec.Cmd` (a `Cmd` cannot be reused) and run
it with the same Dir/Env/Cancel/WaitDelay; on `require`, return an error
wrapping the cause and naming `[hooks].require-hook-scope`. Task 6's `scoped`
flag must be updated to the post-fallback value so the escalation warning stays
truthful.

**Step 4: run, expect PASS**

- `just debug-go-test internal/hookrun`
- `just debug-go-test internal/job` (async path now falls back too)
- `just debug-go-test internal/check`

**Step 5: commit**

`fix(hookrun): fall back to the unscoped hook, with a warn, when its systemd scope cannot be set up (#188, task 8)`

---

### Task 9: docs and the `Refs #188` commit (after Tasks 1–8)

**Context:** Tasks 2–8 did the following (#188):
- Sync `merge-this-session` / `check-this-session` pass their ctx to the hook.
- Every synchronous gate (those two, `sc merge`, `sc check`, `sc run`) runs its
  pre-merge hook in a systemd scope named after a local `sync-<kind>-…` id
  (`clown.WithLocalScope`), a ctx key separate from the async job id.
- `serve` cancels on SIGINT/SIGTERM and discards SIGHUP; the three CLI commands
  cancel on SIGINT/SIGTERM/SIGHUP (`gateSignalContext`); a second signal
  force-exits.
- A cancelled unscoped hook that needed SIGKILL prints a warning.
- A failed scope setup falls back to the bare hook with a warn; the opt-in
  `[hooks].require-hook-scope` makes it fail the gate.
- A client rejecting one sync tool call still cancels nothing until
  purse-first#200.

No behaviour changes in this task.

**Statements the docs MUST carry (long form in FDR 0013 and the manpage;
AGENTS.md gets at most one clause each, see the byte budget):**

- *Session durability:* merges are session-agnostic. Closing the client session
  (stdin EOF) does not abort an in-flight sync merge; `serve` drains in-flight
  handlers (purse-first `server/server.go:63-68,100-105`). Only a signal to
  `serve` (or, later, purse-first#200) aborts it, and the hook tree is then
  torn down, not orphaned. `serve` discards SIGHUP for the same reason.
- *Cancel after landing (Decision 2, accepted):* the ctx also reaches the
  post-merge phase (`merge.go:573`, `:1245-1246`), so a SIGTERM to
  `serve`/`sc` during a deploy SIGTERMs that deploy command on the sync and CLI
  paths (detached children survive, FDR 0023), same as async
  `session-job-cancel`.
- *Scope fallback trajectory:* opt-in now (`require-hook-scope`), later the
  default (opt-out), eventually the fallback is removed.

**Steps:**

1. `AGENTS.md` (CLAUDE.md is the same file). **Hard cap 40000 bytes; it is
   39624 now, so the edit must net at most +376.** Budget is tight because the
   scope fallback and durability notes were added after this was sized: keep
   AGENTS.md to the clauses below and put everything longer in FDR 0013 /
   the manpage. Replace the two bullets at `:439-456` with (reflow to ~80
   columns):

   > - **Hook cancellation** (#188, `hookrun.runHookInDirEnv`): `cmd.Cancel` is
   >   **SIGTERM** (SIGKILL orphaned the `nix` below `just`, which kept the
   >   pipe `Wait` blocks on); `cancelGrace` (10s) escalates. **No `Setpgid`**
   >   (FDR 0023's detached children). Sync merge/check and `sc
   >   merge|check|run` pass a ctx cancelled on SIGINT/SIGTERM(/SIGHUP for
   >   `sc`) (`gateSignalContext`; `serve` too, minus SIGHUP), so the hook dies
   >   before its build worktree goes. Merges are session-durable: stdin EOF
   >   drains, never cancels. A client *rejecting* a call cancels nothing until
   >   purse-first#200.
   > - **Pre-merge hook systemd scope** (#25, ringmaster#12/RFC-0016): the
   >   pre-merge hook runs under `systemd-run --user --scope`
   >   (`clown.ScopeArgv`), and a cancel calls `clown.ScopeStop` to SIGKILL the
   >   cgroup (`TestPreMergeScopeReapsSubtreeOnCancel`). Scope id =
   >   `clown.ScopeIDFromContext`: the async job id, or a `sync-…` local id
   >   (`WithLocalScope`, its own ctx key). Post-merge passes `""`. No user bus
   >   (sandbox, macOS) or a failed scope setup ⇒ bare hook plus a warning
   >   (`[hooks].require-hook-scope` makes setup failure fatal; fallback is
   >   transitional).

   Then check the size with `folio_ls` flags `-la`. If it is over 40000, cut
   the parenthetical `(SIGKILL orphaned … blocks on)` first, then the test
   name, then the "Merges are session-durable" sentence (it stays in FDR 0013).
2. FDR 0013 (pre-merge build worktree; find it in the Design records index).
   Do not change `status`. Under the H1 add
   `> **Amended 2026-10-04 (#188):** cancellation.` In **Limitations** add a
   bullet: the build worktree is removed only after the hook returns; a cancel
   reaches the hook from an async job, `serve` shutdown, or a signalled
   `sc merge|check|run`; a hard kill of spinclass (SIGKILL, second Ctrl-C)
   still orphans the hook and leaves `.merge-*` for `sc clean` (live-process
   safety of that removal: spinclass#350); rejecting a sync MCP call does not
   cancel (purse-first#200). Add the three "Statements the docs MUST carry"
   above (session durability incl. `serve` discarding SIGHUP, cancel-after-
   landing SIGTERMs the post-merge deploy, scope-fallback trajectory) as their
   own short subsection.
3. `doc/spinclass-sweatfile.5.scd:306` mentions `*spinclass#188*` as future
   work. Read the surrounding paragraph and correct the tense only if it now
   states something false. Escape `_` as `\_`. Also add to the `[hooks]`
   pre-merge/inactivity prose a short note on the scope fallback and the
   session-durability and cancel-after-landing behaviour (the
   `require-hook-scope` entry itself was added in Task 7).
4. Commit. The message body carries **`Refs #188`** on its own line and one
   sentence: "A rejected sync tool call still does not cancel the hook; that
   needs purse-first#200."
5. In the final report tell the operator: #188 stays open; close it with the
   `purse-first` input bump past #200 after re-running
   `just debug-go-test cmd/spinclass 'TestHandle(Check|Merge)ThisSessionCancel'`.

**Verify:** `git diff --stat` shows only docs; AGENTS.md ≤ 40000 bytes. Do not
run `just`.

---

## Open questions and risks

**Verified in code (file:line above):** every claim in "What is still broken",
the purse-first no-op and server-wide ctx, the single `JobIDFromContext`
reader, the `jobwake` scope helpers' lack of validation and journal access, the
job-id grammar, and that `runHookInDirEnv` has no bare fallback when
`systemd-run` itself fails (item 8; Task 8 adds one).

**Needs the operator's call:**

1. **How did the reported orphan lose its cwd?** *Inference.* With the ctx
   discarded, spinclass could not have removed the worktree while the hook ran
   in-process. The likeliest sequence is: `serve` was killed or restarted after
   the reject, the hook was reparented, and a later `sc clean` (dead-pid prune,
   `clean.go:385-411`) or `git worktree prune` deleted the dir. If that is
   right, Tasks 4–5 remove the common cause. It is not proven.
2. **How does clown shut `serve` down at session exit?** *Unknown.* Decisions
   3-4 assume: stdin EOF first (drain, merge finishes), and a possible SIGHUP
   from a dropped terminal (discarded). If clown instead sends SIGTERM at
   exit, an in-flight merge is cancelled and torn down (acceptable, not
   orphaned), which contradicts "session-durable" only for that exit path. If
   it sends SIGHUP as its deliberate shutdown, `serve` now ignores it and
   relies on the EOF that follows. Confirm against clown's bridge before
   relying on durability across a `kill`.
3. **Marker-file wrapper vs pre-flight probe (Task 8).** The plan specifies
   `Start()`-error plus an in-scope marker as the reliable "never started"
   detector, with the probe as the fallback if the `sh -c 'touch … && exec "$@"'`
   wrapper proves intrusive (it adds one exec hop before the hook). Reviewer's
   call at implementation time.
4. **Viewport mode and Ctrl-C.** *Not verified:* whether crap's
   `viewport.Present` puts the TTY in raw mode. If it does, Ctrl-C never
   becomes SIGINT and Task 5 only helps for SIGTERM, SIGHUP and non-TTY
   formats. Task 5's manual step answers this; a fix would live in crap.

**Resolved (see Decisions, 2026-10-04):** the scope-spawn-failure policy and
its opt-in knob, cancel-after-landing (accepted), stdin EOF (session-durable),
SIGHUP, and `sc clean` live-process safety (spinclass#350).
