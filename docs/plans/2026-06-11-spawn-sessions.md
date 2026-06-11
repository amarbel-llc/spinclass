# Spawn Sessions (FDR 0006) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** `sc spawn <repo> --brief "…"` / MCP `spawn-session` let a driver session launch a detached, harness-booted worker session in a sibling repo (chat-hello gated), and `sc fork --brief` reuses the same launch machinery for a detached worker on a branch of the current repo.

**Architecture:** New `[session-entry].spawn` (multiplexer argv template, zmx default) and `[session-entry].spawn-entry` (harness argv, `{prompt}` positional) sweatfile knobs; a new `internal/spawn` package owns dirname resolution, template substitution, worktree+state creation (`spawned_by` lineage), detached launch, and the 60s wait-for-chat-hello gate; the worker's hello is emitted mechanically by the existing `SessionStart` plugin hook when it finds `spawned_by` in the worktree's session state. CLI + MCP surfaces wrap one shared `spawn.Launch`.

**Tech Stack:** existing internals only — `internal/{sweatfile,sweatfileio,session,chat,worktree,shop,hooks}`, tommy codegen for the sweatfile fields, huh-free (no prompts), bats for e2e.

**Rollback:** Purely additive (new knobs, new commands, one new optional state field, one new hook branch). Revert the series; no behavior change for sessions that never spawn.

**Authoritative design:** `docs/features/0006-spawn-sibling-sessions.md` (status `proposed`; every decision in it is settled — do not re-litigate). Issue #59 carries the decision log.

**Naming note:** the FDR sketches `[session].spawn`; the REAL TOML table in code is `[session-entry]` (`sweatfile.SessionEntry`, toml tag `session-entry`). Use `[session-entry]` everywhere; do not rename the table.

**Build/test commands:** scoped tests via hamster MCP (`go-test` with packages/run); compile via `hamster go-build`. After changing `internal/sweatfile` struct fields you MUST regenerate the codec: `just gen-tommy` (runs `go generate ./internal/sweatfile` with the devshell tommy). Do NOT run full `just` mid-series (merge gate covers it). bats lanes only in Task 8.

---

### Task 1: sweatfile — `spawn` + `spawn-entry` knobs

**Files:**
- Modify: `internal/sweatfile/sweatfile.go` (SessionEntry struct ~line 23; accessors near `SessionStart` ~line 290; `MergeWith` in the same package — locate the SessionEntry merge arm; `GetDefault` ~line 355)
- Regenerate: `internal/sweatfile/sweatfile_tommy.go` via `just gen-tommy`
- Modify: `internal/validate/validate.go` (+ its test) — template sanity checks
- Test: `internal/sweatfile/sweatfile_test.go` (or the file holding SessionStart/SessionResume accessor+merge tests — mirror them)

**Step 1: Write failing tests** for: (a) accessor defaults — `SessionSpawn()` returns the shipped default `["zmx", "run", "{id}", "--", "{entry}"]` when unset and the configured value when set; `SessionSpawnEntry()` returns nil when unset (no default — harness choice is the user's); (b) merge semantics — later hierarchy level overrides earlier per-field (same as Start/Resume; read the existing MergeWith SessionEntry arm and mirror its tests exactly); (c) TOML decode round-trip:

```toml
[session-entry]
spawn       = ["tmux", "new-session", "-d", "-s", "{id}", "{entry}"]
spawn-entry = ["clown", "{prompt}"]
```

**Step 2:** Run them: `hamster go-test packages=./internal/sweatfile run=Spawn` — expect compile failure (fields missing).

**Step 3:** Add fields to `SessionEntry`:

```go
	// Spawn is the multiplexer argv template `sc spawn` (and detached
	// fork) execs to launch a worker session detached: {id} = the
	// worker's branch/session name (multiplexer-safe), {dir} = the
	// worker worktree, {entry} = splice point for the spawn-entry argv
	// (replaced element-wise, not as one string). Default:
	// ["zmx", "run", "{id}", "--", "{entry}"]. See FDR 0006.
	Spawn []string `toml:"spawn"`
	// SpawnEntry is the harness argv the spawned session boots into;
	// {prompt} is replaced by the driver's brief (and {dir} by the
	// worker worktree). No default — the harness is the user's choice
	// (e.g. ["clown", "{prompt}"]); spawn errors when unset.
	SpawnEntry []string `toml:"spawn-entry"`
```

Add accessors `SessionSpawn() []string` (default when nil/empty) and `SessionSpawnEntry() []string` (nil when unset) mirroring `SessionStart`'s shape; wire both fields into the SessionEntry `MergeWith` arm exactly like Start/Resume. Run `just gen-tommy`. NOTE the codegen-isolation pattern (CLAUDE.md): hand-written code must not reference generated symbols; you are only adding struct fields + plain methods, which is safe.

**Step 4:** `hamster go-test packages=./internal/sweatfile` — PASS. Whole-module `hamster go-build` must stay clean.

**Step 5:** validate: in `internal/validate`, add `CheckSessionEntry` warnings/errors: `spawn` set but missing `{entry}` placeholder → error; `spawn-entry` set but missing `{prompt}` → warning (a harness might take the brief elsewhere, but flag it). Mirror `CheckHooks`'s Issue shape + tests. Run validate tests.

**Step 6:** Commit: `feat(sweatfile): [session-entry] spawn + spawn-entry templates` (+ Clown 0.4.0+cd38c2d trailer with link https://github.com/amarbel-llc/clown/commit/cd38c2d6298ea4b271bfa70c6476212c2d35e29e — same trailer on every commit in this plan).

---

### Task 2: session — `spawned_by` lineage

**Files:**
- Modify: `internal/session/session.go` (State struct ~line 58; ListRows + the `sc list` text rendering — locate `ListRows` and `runListResult` consumers)
- Modify: `cmd/spinclass/commands_query.go` (`runListResult` text rows) and the chat-list-sessions handler in `cmd/spinclass/` (grep `chat-list-sessions` — annotate rows like the existing `{branch}` hint)
- Tests: alongside each (session_test.go list-rows test; commands_query_test.go; the chat-list test file)

**Step 1:** Failing tests: State with `SpawnedBy: "spinclass/bright-cedar"` JSON-round-trips (`json:"spawned_by,omitempty"`); `sc list` text row for such a session carries a `spawned-by:<key>` annotation column/suffix (mirror how implicit sessions' branch hint renders — read the current row rendering FIRST and extend, don't redesign); chat-list-sessions row gains `[spawned-by <key>]` (mirror `{branch}` annotation).

**Step 2-4:** red → implement → green. The field is display-only everywhere; no behavioral branches.

**Step 5:** Commit: `feat(session): spawned_by lineage field + list annotations`.

---

### Task 3: chat — hello send + wait

**Files:**
- Create: `internal/chat/hello.go` + `internal/chat/hello_test.go`

**Step 1: Failing tests** (XDG_STATE_HOME-sandboxed like read_test.go):

```go
// SendHello posts the spawn handshake from worker to driver.
// WaitForHello polls (peek reads — the driver agent's cursor must NOT
// advance) for a hello from `from` newer than `since`, returning nil on
// arrival or a deadline error.
func TestHelloRoundTrip(t *testing.T)   // SendHello then WaitForHello(…, 2s) succeeds fast
func TestWaitForHelloTimesOut(t *testing.T) // no hello → error mentioning the deadline, promptly (use a ~300ms deadline)
func TestWaitForHelloIgnoresOlder(t *testing.T) // a pre-`since` message from the worker does not satisfy the gate
```

**Step 3: Implement:**

```go
// HelloSubject is the spawn handshake subject prefix; WaitForHello keys on it
// so unrelated worker chatter cannot satisfy the gate.
const HelloSubject = "hello from spawned session"

func SendHello(from, to string) error {
	return Send(Message{From: from, To: to, Subject: HelloSubject + " " + from,
		Body: "spawn handshake (FDR 0006): session " + from + " is up."})
}

// WaitForHello polls every 250ms with peek reads filtered From=from, ToMe,
// accepting only messages with the HelloSubject prefix and Timestamp after
// since. reader is the driver's session key.
func WaitForHello(reader, from string, since time.Time, deadline time.Duration) error
```

(Poll loop: `time.NewTicker(250 * time.Millisecond)`; overall `time.After(deadline)`; peek=true always. `Send` already does the clown wake emit — no extra wiring.)

**Step 4-5:** green; commit `feat(chat): spawn hello handshake (send + gated wait)`.

---

### Task 4: spawn — resolution, templates, launch orchestration

**Files:**
- Create: `internal/spawn/spawn.go`, `internal/spawn/resolve.go`, `internal/spawn/template.go` + `_test.go` each

**Step 1 (templates, pure functions — failing tests first):**

```go
// SubstituteEntry renders the spawn-entry argv: {prompt}→brief, {dir}→wtPath.
func SubstituteEntry(entry []string, brief, wtPath string) []string
// SubstituteSpawn renders the spawn argv: {id}→id, {dir}→wtPath, and the
// element "{entry}" is replaced by splicing in the (already-substituted)
// entry argv element-wise. Returns an error when entry is empty (no
// spawn-entry configured) or spawn has no {entry} element.
func SubstituteSpawn(spawnTpl []string, id, wtPath string, entry []string) ([]string, error)
```

Table tests: zmx default template + `["clown","{prompt}"]` + brief "fix the thing" → `["zmx","run","wt-name","--","clown","fix the thing"]`; missing {entry} errors; empty entry errors; {dir} substitution; brief containing spaces/quotes stays one argv element (no shell joining — assert len).

**Step 2 (resolution — failing tests):**

```go
// ResolveRepo resolves a spawn target: a value containing a path separator
// (or matching an existing path) is used directly (escape hatch); otherwise
// it is a repo dirname leaf-searched under the workspace-root pattern
// $HOME/*/repos/<leaf>. Exactly one match must exist and be a git repo
// (its .git a directory); zero matches, multiple matches (error lists
// candidates), or resolving to the driver's own repo are errors.
func ResolveRepo(home, target, driverRepoPath string) (string, error)
```

Tests build fake `$HOME/eng/repos/foo`, `$HOME/eng-alt/repos/bar` trees (git init via testgit; package gets the hermetic TestMain from `testgit.SetHermeticEnv` like every git-touching package). Cover: leaf hit, ambiguous leaf across roots, miss, same-repo rejection, explicit path escape.

**Step 3 (Launch — orchestration):**

```go
type Result struct{ SessionKey, WorktreePath, MultiplexerID string }

// Launch creates a detached, harness-booted worker session in repoPath and
// blocks until its SessionStart hello (FDR 0006). Steps: generate the
// worker's branch name (REUSE the same generator `sc start` uses — locate it
// via cmd/spinclass's start command; it lives in internal/worktree); resolve
// the ResolvedPath exactly as `sc start` does; create the worktree
// (shop.Create with format "" and nil writer is the existing non-attaching
// create path — read shop.Create/createWorktree and reuse, do NOT duplicate
// worktree.Create wiring); write session.State{PID: 0, SessionState:
// StateActive, …, SpawnedBy: driverKey, Description: desc}; load the WORKER
// repo's sweatfile hierarchy (sweatfileio.LoadWorktreeHierarchy) for its
// spawn/spawn-entry templates; exec the substituted spawn argv with
// cmd.Dir = worktree (exec.Command, Run() — the template contract is that it
// returns promptly after detaching, like `zmx run`); then
// chat.WaitForHello(driverKey, workerKey, startTime, deadline).
func Launch(home, repoPath, driverKey, brief, desc string, deadline time.Duration) (Result, error)
```

Unit-test Launch with a stub template in a fixture sweatfile: `spawn = ["sh","-c","echo launched >{dir}/launched; <send hello>","sh"]`… in Go tests the cleanest hello stub: spawn template runs a script that calls nothing, and the test calls `chat.SendHello(workerKey, driverKey)` itself after observing the marker file — i.e., test the orchestration seams (state written with SpawnedBy; template exec'd in the right dir; hello gate honored; timeout error when no hello). PID note: state is written with PID 0 — `SweepDeadImplicit` only applies to implicit sessions, and worktree-session liveness handles PID 0 as "not alive" (verify against `ResolveState`: PID 0 + StateActive → resolves inactive — that is CORRECT pre-hello; the SessionStart hook refresh in Task 5 takes over). Document this in a comment.

**Step 4-5:** green; commit `feat(spawn): dirname resolution, launch templates, hello-gated orchestration`.

---

### Task 5: hooks — SessionStart hello + state refresh for spawned worktrees

**Files:**
- Modify: `internal/hooks/hooks.go` `runSessionStart` (~line 64) — BEFORE Gate 1's early return
- Test: `internal/hooks/hooks_test.go` (mirror the existing implicit-session SessionStart tests' fixture style)

**Step 1: Failing test:** a worktree (testgit.MustInit + MustWorktreeAdd) whose session state has `SpawnedBy: "driver/key"`; fire `Run` with a SessionStart hookInput (cwd = worktree). Assert: a chat message From the worker's session key To "driver/key" with the HelloSubject prefix exists (read the chat store via chat.Read with a fresh reader key); the state's PID was refreshed to a live value. Counter-tests: worktree WITHOUT SpawnedBy → no message; second SessionStart fire (resume/clear) → no DUPLICATE hello (the hook clears a `hello_sent` marker — simplest: a `HelloSentAt *time.Time` field on State set by the hook; add it in this task with `json:"hello_sent_at,omitempty"`).

**Step 3: Implement** in `runSessionStart`, before the IsWorktree early return:

```go
	if worktree.IsWorktree(cwd) {
		maybeSendSpawnHello(cwd, input) // never blocks startup; all errors swallowed
		return nil
	}
```

`maybeSendSpawnHello`: resolve repoPath/branch via git (mirror the existing helpers), `session.Read(repoPath, branch)`; if `SpawnedBy != "" && HelloSentAt == nil`: `chat.SendHello(st.SessionKey, st.SpawnedBy)`, set HelloSentAt=now + PID=os.Getppid() + StateActive, `session.Write`. Log failures via sessionlog; return nothing.

**Step 4-5:** green (`hamster go-test packages=./internal/hooks`); commit `feat(hooks): SessionStart emits the spawn hello and adopts the worker state`.

---

### Task 6: CLI `sc spawn` + MCP `spawn-session`

**Files:**
- Modify: `cmd/spinclass/commands_session.go` (new command next to `start`)
- Modify: `cmd/spinclass/commands_mcp_only.go` (MCP tool — `Run:` handler; register unconditionally like update-this-session-description)
- Tests: `cmd/spinclass/commands_mcp_only_test.go` (handler-level, fixture pattern from TestHandleUpdateDescriptionImplicit)

CLI:

```
sc spawn <repo> --brief "<text>" [--issue <N>] [--description "<text>"]
```

`Run` (MCP) + `RunCLI` on ONE command named `spawn` (mirrors how merge has both surfaces — but note spawn's MCP tool name comes from the command name; name the command `spawn-session`? NO: CLI is `sc spawn`; MCP tools take the command's name. Read how `merge` registers `Run` (exposed as tool `merge`?) — the merge MCP path is actually the separate `merge-this-session`. DECISION: register the CLI command `spawn` (RunCLI only) and a separate MCP-only command `spawn-session` (Run only) whose handler resolves the driver key via `currentSessionKey()` (exists in commands_mcp_only.go), mirrors the CLI flow otherwise. Both delegate to `spawn.Launch`.) Driver key for the CLI: `currentSessionKey()` equivalent — `$SPINCLASS_SESSION_ID` or implicit fallback; factor or reuse.

`--issue N`: fetch the issue body via the same exec path `start-gh_issue` uses? NO — that's a start-command plugin (`gh` CLI via sweatfile exec). Smallest honest v1: `--issue` shells `gh issue view N --json title,body` in the TARGET repo dir and prepends `title\n\nbody\n\n---\n\n` to the brief; skip gracefully with an error if `gh` is absent. (The FDR says "prepends the issue body".)

Tool description must state: blocks up to 60s for the worker's hello; returns session_key/worktree_path/multiplexer id; the brief should carry everything the worker needs plus a message-me-back instruction (cite the chat target = the returned session_key).

Completion for `<repo>`: a `Completer` listing `$HOME/*/repos/*` leaf names (cheap glob; mirror completeWorktreeTargets's shape).

Tests: handler validation errors (missing brief; unknown repo) via the fixture pattern; full Launch is covered by Task 4 + bats.

Commit: `feat(cli,mcp): sc spawn + spawn-session`.

---

### Task 7: detached fork — `sc fork --brief` + MCP `fork-session`

**Files:**
- Modify: `cmd/spinclass/commands_query.go` (fork command ~line 180 — add `brief`/`description` params)
- Modify: `internal/shop/shop.go` `Fork` OR (preferred) keep Fork untouched and compose in the command: fork creates the worktree (existing `worktree.CreateFrom` path), then reuse `spawn.Launch`'s post-create steps. To avoid duplicating Launch's tail, refactor `spawn.Launch` into `Launch` (create+launch) and `LaunchExisting(home, rp worktree.ResolvedPath, driverKey, brief, desc string, deadline)` (state+template+hello over an existing worktree); Fork-detached calls LaunchExisting after CreateFrom. Same-repo is ALLOWED here (that's the point) — only `sc spawn` rejects same-repo.
- Modify: `cmd/spinclass/commands_mcp_only.go`: MCP-only `fork-session` (Run) — forks the CURRENT session's worktree repo from the current branch HEAD, brief required, driver key via currentSessionKey.
- Tests: handler-level + a LaunchExisting unit test (reuses Task 4 fixtures).

`sc fork [branch]` without `--brief` keeps today's behavior byte-identical (create only, print path). With `--brief`: detached launch + hello gate, prints the Result.

Commit: `feat(fork): detached fork reuses the spawn launch machinery`.

---

### Task 8: bats e2e

**Files:**
- Create: `zz-tests_bats/spawn.bats` (+ wire into the lane the same way other .bats files are picked up — check zz-tests_bats lane config/justfile targets; usually auto-globbed)

Fixture strategy (no zmx, no real harness in the sandbox):
- Worker repo at `$HOME/eng/repos/workerrepo` (the resolver's glob is rooted at $HOME — bats already sandboxes HOME; verify common.bash).
- Sweatfile in the worker repo:
  ```toml
  [session-entry]
  spawn       = ["sh", "-c", "\"$@\" >\"$PWD/spawn.log\" 2>&1 &", "sh", "{entry}"]
  spawn-entry = ["sh", "-c", "echo \"$1\" >brief.txt; printf '{\"hook_event_name\":\"SessionStart\",\"session_id\":\"bats\",\"cwd\":\"'\"$PWD\"'\"}' | \"$SC_BIN\" <hook-subcommand>", "sh", "{prompt}"]
  ```
  i.e. the stub "harness" records the brief then fires the real SessionStart hook handler so the REAL hello path runs. Find the hook handler invocation: grep cmd/spinclass for the hooks entrypoint subcommand (commands_hooks.go registers it — likely `claude-hook` / `hook`; use the real name and the real stdin JSON shape from internal/hooks.hookInput).
- Driver side: `SPINCLASS_SESSION_ID=driver/bats sc spawn workerrepo --brief "do the thing"` (run_sc-style helper; spawn output isn't merge/check so it speaks TAP or plain prints — read what the command emits and assert on that + on effects).
- Assertions: exit 0; worker worktree exists; `brief.txt` contains the brief; worker state JSON has `spawned_by`; `sc list` shows the annotation; a timeout case (spawn-entry that does NOT fire the hook) exits non-zero mentioning the deadline within ~70s (use a shortened deadline if a flag/env for it exists — if the 60s constant is a hard constant, add `--hello-timeout` hidden flag in Task 6 to keep this test fast; prefer the flag, it IS the tuning lever).
- Fork-detached happy path: same stub inside a started session fixture.

Run: `just test-bats` (async via moxy if >300s). Commit: `test(bats): spawn + detached fork e2e over stub multiplexer/harness`.

---

### Task 9: docs + FDR promotion

**Files:**
- Modify: `CLAUDE.md` — CLI commands table (+`sc spawn`, fork `--brief`), sweatfile section (`[session-entry]` spawn/spawn-entry), MCP tools mention, External deps (zmx optional: only when the default spawn template is used).
- Modify: `README.md` if it lists commands (check).
- Modify: `docs/features/0006-spawn-sibling-sessions.md` — status `proposed` → `accepted` ONLY IF the end-to-end exercise criterion is met by the bats lane + a real manual spawn; otherwise add an "implemented 2026-06-11, awaiting first production spawn" note. Be honest.
- `cmd/spinclass` description tests updated as needed.

Commit: `docs: sc spawn / detached fork surfaces (FDR 0006)`.

---

### Task 10: sweep + verify

`rg "spawn" --type go` for stragglers/dead code; `hamster go-build`; `hamster go-vet`; `just lint-fmt`; full `hamster go-test packages=./...`. Fix; commit only if changes. The merge gate runs the full suite + bats lanes — do not pre-run `just`.
