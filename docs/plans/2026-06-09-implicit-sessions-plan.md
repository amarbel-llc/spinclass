# Implicit Sessions Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Let an agent started in a repo's main checkout become a first-class spinclass session (visible in `sc list`, addressable in chat, usable with `merge`/`check`/`update-description`/`close`), materialized by Claude Code `SessionStart`/`SessionEnd` plugin hooks.

**Architecture:** Approach A — *parallel* storage. Implicit sessions get their own `<checkout>/.spinclass/state-<rand>.json` files (`<rand> = sha256(session_id)[:8]`) with central index symlinks, leaving the existing per-worktree `state.json` path (the daily driver) untouched. A `kind:"implicit"` discriminator on `session.State` drives the few behavioral divergences (list marker, close-state-only, merge=hook-then-push). Lifecycle is driven by new `SessionStart`/`SessionEnd` cases in the existing `hooks.Run` event switch — NOT new `sc` subcommands; the single `hook` command already dispatches on `hook_event_name`. The `hooks/hooks.json` plugin manifest is hand-maintained and gets two new event blocks.

**Tech Stack:** Go; JSON (state files + hook I/O); the existing `internal/session`, `internal/hooks`, `internal/merge`, `internal/sweatfile` packages; bats for integration.

**Rollback:** Sweatfile knob `[hooks].disable-implicit-sessions = true` (cascades like other `[hooks]` flags) makes the `SessionStart` handler a no-op. Plugin hooks stay registered but do nothing. Single config change; no revert. The feature is otherwise purely additive — worktree sessions are untouched.

**Design doc:** `docs/plans/2026-06-09-implicit-sessions-design.md`
**Issue:** #118

---

## Conventions for the implementer

- **Worktree, not root.** All work happens in this worktree
  (`.worktrees/quick-aspen`). Never touch the root git dir.
- **Build/test the paved-path way.** Compile-check a single package with
  `hamster.go-build` (packages `./internal/session`, etc.). Run a single Go
  test with `hamster.go-test` (`run` = the test name). Do NOT run the full
  `just` suite between tasks — `merge-this-session`'s pre-merge hook is the CI
  lane and runs it once at the end.
- **bats** lives in `zz-tests_bats/`. The existing `hooks.bats` is the model
  for piping hook JSON to the binary. See `eng:wiring-bats-tests` if extending
  the harness.
- **Commit after every green task.** GPG signing is required; if a commit
  fails on signing, STOP and ask the user to unlock piggy-agent — do not
  commit unsigned.
- **TDD.** Write the failing test first, watch it fail, implement minimally,
  watch it pass, commit.

---

## Task 1: Add `Kind` discriminator to `session.State`

**Promotion criteria:** N/A (additive field).

**Files:**
- Modify: `internal/session/session.go:54-78` (the `State` struct)
- Test: `internal/session/session_test.go`

**Step 1: Write the failing test**

Add to `internal/session/session_test.go`:

```go
func TestStateKindRoundTrips(t *testing.T) {
	s := State{Kind: KindImplicit, WorktreePath: "/x", Branch: "master"}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"kind":"implicit"`) {
		t.Fatalf("kind not serialized: %s", data)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindImplicit {
		t.Fatalf("kind = %q, want %q", got.Kind, KindImplicit)
	}

	// Absent kind ⇒ empty (worktree session, the existing default).
	var wt State
	if err := json.Unmarshal([]byte(`{"branch":"x"}`), &wt); err != nil {
		t.Fatal(err)
	}
	if wt.Kind != "" {
		t.Fatalf("absent kind should be empty, got %q", wt.Kind)
	}
}
```

**Step 2: Run test to verify it fails**

Run via `hamster.go-test`: packages `./internal/session`, run `TestStateKindRoundTrips`.
Expected: FAIL — `KindImplicit` undefined, `State.Kind` undefined.

**Step 3: Write minimal implementation**

In `internal/session/session.go`, add a const near the `State*` consts (line 43-52):

```go
// KindImplicit marks a session materialized for a repo's main checkout (no
// sc-created worktree). Absent Kind ⇒ a normal worktree session.
const KindImplicit = "implicit"
```

Add the field to `State` (after `SessionKey`, ~line 60):

```go
	Kind string `json:"kind,omitempty"`
```

**Step 4: Run test to verify it passes**

Run `TestStateKindRoundTrips` — Expected: PASS.

**Step 5: Commit**

```
feat(session): add Kind discriminator to State (#118)

Marks implicit (main-checkout) sessions. omitempty so existing worktree
state.json files are unaffected.
```

---

## Task 2: `session.WriteImplicit` / `RemoveImplicit` (per-`<rand>` state files)

The existing `Write`/`Read`/`Remove` are keyed by worktree path → one
`state.json` per worktree. Implicit sessions need `state-<rand>.json` siblings,
so they get parallel functions that take an explicit filename. This keeps the
worktree-session path (Task-0 daily driver) literally untouched.

**Promotion criteria:** N/A (additive).

**Files:**
- Modify: `internal/session/session.go` (add functions + path helper)
- Test: `internal/session/session_test.go`

**Step 1: Write the failing test**

```go
func TestWriteRemoveImplicit(t *testing.T) {
	checkout := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	rand := "a3f9b2c1"
	s := State{
		Kind:         KindImplicit,
		PID:          os.Getpid(),
		SessionState: StateActive,
		RepoPath:     checkout,
		WorktreePath: checkout,
		Branch:       "master",
		SessionKey:   "myrepo/master-" + rand,
		StartedAt:    time.Now(),
	}
	if err := WriteImplicit(s, rand); err != nil {
		t.Fatal(err)
	}

	// Worktree-local file exists at .spinclass/state-<rand>.json.
	local := filepath.Join(checkout, ".spinclass", "state-"+rand+".json")
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("local state not written: %v", err)
	}
	// Central index symlink exists and resolves to the local file.
	idx := implicitIndexPath(local)
	resolved, err := os.Readlink(idx)
	if err != nil {
		t.Fatalf("index symlink not written: %v", err)
	}
	if resolved != local {
		t.Fatalf("symlink target = %q, want %q", resolved, local)
	}

	if err := RemoveImplicit(checkout, rand); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("local state not removed: %v", err)
	}
	if _, err := os.Lstat(idx); !os.IsNotExist(err) {
		t.Fatalf("index entry not removed: %v", err)
	}
}
```

**Step 2: Run to verify it fails**

Run `TestWriteRemoveImplicit` — Expected: FAIL (undefined `WriteImplicit`,
`RemoveImplicit`, `implicitIndexPath`).

**Step 3: Implement**

In `internal/session/session.go`. Note `indexFilename` hashes the path passed
to it, so `implicitIndexPath` can reuse it by passing the per-rand state path
(unique → unique hash, no collision with the worktree's `state.json` hash):

```go
// implicitStatePath is the worktree-local path of an implicit session's
// per-rand state file: <checkout>/.spinclass/state-<rand>.json.
func implicitStatePath(checkout, rand string) string {
	return filepath.Join(checkout, ".spinclass", "state-"+rand+".json")
}

// implicitIndexPath returns the central index entry for an implicit state
// file. Hashing the per-rand local path keeps it unique from the worktree's
// own state.json index entry.
func implicitIndexPath(localStatePath string) string {
	return filepath.Join(indexDir(), indexFilename(localStatePath))
}

// WriteImplicit persists an implicit (main-checkout) session to
// <checkout>/.spinclass/state-<rand>.json plus a central index symlink. Unlike
// Write, it keys on rand (not worktree path) so concurrent main-checkout agents
// never collide. s.WorktreePath must be the checkout root.
func WriteImplicit(s State, rand string) error {
	checkout := s.WorktreePath
	if checkout == "" {
		return errors.New("session.WriteImplicit: WorktreePath required")
	}
	dir := filepath.Join(checkout, ".spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	local := implicitStatePath(checkout, rand)
	if err := os.WriteFile(local, data, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(indexDir(), 0o755); err != nil {
		return err
	}
	link := implicitIndexPath(local)
	tmp := filepath.Join(indexDir(), fmt.Sprintf(".tmp-%d-%d.json", os.Getpid(), time.Now().UnixNano()))
	if err := os.Symlink(local, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// RemoveImplicit deletes an implicit session's per-rand state file and its
// central index entry. Tolerates missing files. Never removes the checkout's
// .spinclass dir wholesale (other agents may have siblings there).
func RemoveImplicit(checkout, rand string) error {
	local := implicitStatePath(checkout, rand)
	if err := os.Remove(local); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(implicitIndexPath(local)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
```

**Step 4: Run to verify it passes**

Run `TestWriteRemoveImplicit` — Expected: PASS.

**Step 5: Commit**

```
feat(session): WriteImplicit/RemoveImplicit for per-rand state files (#118)

Parallel to Write/Remove; keyed by rand so concurrent main-checkout
agents get distinct .spinclass/state-<rand>.json files. Worktree-session
path untouched.
```

---

## Task 3: `session.SweepDeadImplicit` (orphan reaper)

Backstop for missed `SessionEnd`: delete `state-*.json` files in a checkout
whose recorded PID is dead. Reuses the existing liveness check.

**Promotion criteria:** N/A (additive).

**Files:**
- Modify: `internal/session/session.go`
- Test: `internal/session/session_test.go`

**Step 1: Failing test**

First find the existing PID-liveness helper to reuse — search the file for
`Signal(syscall.Signal(0))` or a `func` named like `pidAlive`/`processAlive`
(the package imports `syscall`). Use that helper name in the test and impl. The
test below assumes a helper `pidAlive(int) bool`; adjust to the real name.

```go
func TestSweepDeadImplicit(t *testing.T) {
	checkout := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	spin := filepath.Join(checkout, ".spinclass")
	os.MkdirAll(spin, 0o755)

	// Live session (our own PID) — must survive.
	live := State{Kind: KindImplicit, PID: os.Getpid(), WorktreePath: checkout, Branch: "master"}
	if err := WriteImplicit(live, "live1234"); err != nil {
		t.Fatal(err)
	}
	// Dead session (PID 1 is init; use an impossibly high unused pid instead).
	dead := State{Kind: KindImplicit, PID: 2147483646, WorktreePath: checkout, Branch: "master"}
	if err := WriteImplicit(dead, "dead5678"); err != nil {
		t.Fatal(err)
	}

	if err := SweepDeadImplicit(checkout); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(implicitStatePath(checkout, "live1234")); err != nil {
		t.Fatalf("live session wrongly swept: %v", err)
	}
	if _, err := os.Stat(implicitStatePath(checkout, "dead5678")); !os.IsNotExist(err) {
		t.Fatalf("dead session not swept: %v", err)
	}
}
```

**Step 2: Run to verify it fails**

Run `TestSweepDeadImplicit` — Expected: FAIL (undefined `SweepDeadImplicit`).

**Step 3: Implement**

```go
// SweepDeadImplicit removes implicit state-<rand>.json files in checkout whose
// recorded PID is no longer alive. Best-effort: a leaked file from a missed
// SessionEnd is reaped the next time any agent starts in the checkout. Errors
// on individual files are ignored; the function only returns an error if the
// glob itself fails.
func SweepDeadImplicit(checkout string) error {
	pattern := filepath.Join(checkout, ".spinclass", "state-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, m := range matches {
		data, rerr := os.ReadFile(m)
		if rerr != nil {
			continue
		}
		var s State
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		if s.PID != 0 && !pidAlive(s.PID) { // use the real helper name
			rand := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "state-"), ".json")
			_ = RemoveImplicit(checkout, rand)
		}
	}
	return nil
}
```

**Step 4: Run to verify it passes** — Expected: PASS.

**Step 5: Commit**

```
feat(session): SweepDeadImplicit orphan reaper (#118)

Deletes dead-PID state-<rand>.json files; backstop for missed SessionEnd.
```

---

## Task 4: `[hooks].disable-implicit-sessions` sweatfile knob

The rollback lever. Follow the exact pattern of an existing bool `[hooks]` flag
(e.g. `disable-nix-gc` / `DisableNixGCEnabled`). The codec is tommy-generated;
adding a field requires regenerating it.

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/sweatfile/sweatfile.go` (Hooks struct field + accessor +
  MergeWith handling, mirroring `disable-nix-gc`)
- Regenerate: `internal/sweatfile/sweatfile_tommy.go` via `//go:generate tommy generate`
- Test: `internal/sweatfile/sweatfile_test.go`

**Step 1: Find the model.** Grep `internal/sweatfile/` for `disable-nix-gc`,
`DisableNixGC`, and `DisableNixGCEnabled`. Replicate every site for
`disable-implicit-sessions` / `DisableImplicitSessions` /
`DisableImplicitSessionsEnabled`.

**Step 2: Failing test** (mirror the `disable-nix-gc` merge/accessor test):

```go
func TestDisableImplicitSessionsMergeAndAccessor(t *testing.T) {
	parent := Sweatfile{}
	child := mustParse(t, "[hooks]\ndisable-implicit-sessions = true\n")
	merged := parent.MergeWith(child)
	if !merged.DisableImplicitSessionsEnabled() {
		t.Fatal("expected disable-implicit-sessions=true to survive merge")
	}
	if (Sweatfile{}).DisableImplicitSessionsEnabled() {
		t.Fatal("default must be false (feature on)")
	}
}
```

(Use whatever the existing tests use to parse a sweatfile string — copy the
`disable-nix-gc` test's helper exactly.)

**Step 3: Run to verify it fails** — Expected: FAIL (accessor undefined).

**Step 4: Implement** — add the field/accessor/merge handling, then regenerate
the codec: run `just deps`? No — the codec is regenerated by `tommy generate`.
Run the project's codegen recipe: check `justfile` for a `tommy`/`generate`
recipe (the sweatfile package has `//go:generate tommy generate`). If a recipe
exists (e.g. `just generate` or a sweatfile-codegen recipe), run it; otherwise
run `go generate ./internal/sweatfile/`. Verify `sweatfile_tommy.go` now
references the new field.

**Step 5: Run to verify it passes** — Expected: PASS. Also compile-check the
package with `hamster.go-build` (`./internal/sweatfile`).

**Step 6: Commit**

```
feat(sweatfile): [hooks].disable-implicit-sessions knob (#118)

Rollback lever for implicit sessions. Default false (feature on).
Regenerated tommy codec.
```

---

## Task 5: `SessionStart` hook handler

Add a `SessionStart` case to `hooks.Run`'s event switch
(`internal/hooks/hooks.go:31-39`). It gates on main-checkout-ness, sweeps dead
orphans, then upserts the implicit session.

**Promotion criteria:** N/A (gated by Task 4 knob).

**Files:**
- Modify: `internal/hooks/hooks.go` (switch case + new `runSessionStart`)
- Modify: `internal/hooks/hooks.go:16-23` (add `Source string `json:"source"`` to `hookInput`)
- Test: `internal/hooks/hooks_test.go`

**Background facts (already verified):**
- `hookInput` already has `SessionID` and `CWD`. Add `Source`.
- For a main checkout, `cmd.go`'s `Handle` calls `Run(stdin, stdout, "", "", false)` —
  so `mainRepoRoot`/`sessionWorktree` are empty. The handler must derive
  repo/branch from `input.CWD` itself.
- Helpers available: `git.CommonDir(dir)`, `git.BranchCurrent(dir)`. Find the
  default-branch helper (grep `internal/git` for `DefaultBranch`/`SymbolicRef`).
- `worktree.IsWorktree(cwd)` is true only inside a `.worktrees/` linked worktree.

**Step 1: Failing test**

```go
func TestSessionStartMaterializesImplicit(t *testing.T) {
	// A real git repo on its default branch, NOT a .worktrees worktree.
	repo := initGitRepoOnMaster(t) // use the package's existing repo-init test helper
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "abc123def456",
		"cwd":             repo,
		"source":          "startup",
	})
	var out bytes.Buffer
	if err := Run(bytes.NewReader(input), &out, "", "", false); err != nil {
		t.Fatal(err)
	}

	// A state-<rand>.json file now exists, rand = sha256("abc123def456")[:8].
	rand := implicitRand("abc123def456")
	if _, err := os.Stat(filepath.Join(repo, ".spinclass", "state-"+rand+".json")); err != nil {
		t.Fatalf("implicit session not materialized: %v", err)
	}
}

func TestSessionStartNoopInsideWorktree(t *testing.T) {
	wt := initWorktree(t) // existing helper that makes a .worktrees/<x> linked worktree
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart", "session_id": "zzz", "cwd": wt, "source": "startup",
	})
	var out bytes.Buffer
	if err := Run(bytes.NewReader(input), &out, "", "", false); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(wt, ".spinclass", "state-*.json"))
	if len(matches) != 0 {
		t.Fatalf("should not materialize implicit session inside a worktree, got %v", matches)
	}
}
```

(Reuse the repo/worktree init helpers already in `hooks_test.go` /
`session_test.go`; if none exist, the simplest is `git init` + a commit in a
TempDir, branch renamed to `master`.)

**Step 2: Run to verify it fails** — Expected: FAIL (`SessionStart` falls
through to `runPreToolUse` today; `implicitRand` undefined).

**Step 3: Implement**

Add a small exported-or-package helper for the rand (also used by Task 6 and
tests):

```go
func implicitRand(sessionID string) string {
	h := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("%x", h[:8])
}
```

Switch case in `Run`:

```go
	case "SessionStart":
		return runSessionStart(input)
	case "SessionEnd":
		return runSessionEnd(input)
```

(Place above the `default:`; `SessionStart`/`SessionEnd` produce no stdout
decision, so they return nil and write nothing.)

```go
// runSessionStart materializes an implicit session when cwd is a deliberate
// main checkout (a git repo on its default branch, NOT a .worktrees worktree).
// All failures are swallowed (return nil) — a hook must never block session
// startup. Honors [hooks].disable-implicit-sessions.
func runSessionStart(input hookInput) error {
	cwd := input.CWD
	if cwd == "" || input.SessionID == "" {
		return nil
	}
	// Gate 1: not inside an sc-created worktree (those already have state).
	if worktree.IsWorktree(cwd) {
		return nil
	}
	// Gate 2: rollback knob.
	if home, _ := os.UserHomeDir(); home != "" {
		if res, err := sweatfileio.LoadHierarchy(home, cwd); err == nil &&
			res.Merged.DisableImplicitSessionsEnabled() {
			return nil
		}
	}
	// Gate 3: a git repo whose checkout root == cwd and which is on its
	// default branch. (Derive; bail silently on any error.)
	repoRoot, err := gitToplevel(cwd) // already defined in cmd.go (same package)
	if err != nil || filepath.Clean(repoRoot) != filepath.Clean(cwd) {
		return nil
	}
	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		return nil
	}
	def, err := git.DefaultBranch(cwd) // use the real helper name
	if err != nil || branch != def {
		return nil
	}

	// Orphan sweep before our own write.
	repoName := filepath.Base(repoRoot)
	_ = session.SweepDeadImplicit(cwd)

	rand := implicitRand(input.SessionID)
	key := repoName + "/" + branch + "-" + rand
	s := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getppid(), // the claude process; see note below
		SessionState: session.StateActive,
		RepoPath:     repoRoot,
		WorktreePath: cwd,
		Branch:       branch,
		SessionKey:   key,
		StartedAt:    time.Now(),
		Env:          map[string]string{"SPINCLASS_SESSION_ID": key},
	}
	_ = session.WriteImplicit(s, rand) // swallow; never block startup
	return nil
}
```

**PID note (decide during impl):** the hook subprocess's own PID is ephemeral
(the handler exits immediately). For PID-liveness reaping we want the Claude
agent's PID. `os.Getppid()` is the parent of the handler — likely the Claude
process or a shell wrapper. VERIFY empirically during the bats task (Task 9)
which PID is alive for the session's lifetime; if `getppid` is a transient
shell, fall back to recording the session by `session_id` liveness via a
different mechanism, or accept that the `SessionEnd` delete + `SessionStart`
sweep are the primary reapers and PID is best-effort. Do NOT assert the PID
semantics are correct without observing them.

**Step 4: Run to verify it passes** — Expected: PASS for both tests.

**Step 5: Commit**

```
feat(hooks): SessionStart materializes implicit sessions (#118)

Gates on main-checkout-ness + disable-implicit-sessions knob, sweeps
dead orphans, upserts state-<rand>.json. Swallows all errors.
```

---

## Task 6: `SessionEnd` hook handler

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/hooks/hooks.go` (`runSessionEnd`)
- Modify: `hookInput` — add `Reason string `json:"reason"`` (optional; for logging)
- Test: `internal/hooks/hooks_test.go`

**Step 1: Failing test**

```go
func TestSessionEndRemovesImplicit(t *testing.T) {
	repo := initGitRepoOnMaster(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Materialize first.
	startInput, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart", "session_id": "sid999", "cwd": repo, "source": "startup",
	})
	Run(bytes.NewReader(startInput), &bytes.Buffer{}, "", "", false)
	rand := implicitRand("sid999")
	local := filepath.Join(repo, ".spinclass", "state-"+rand+".json")
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("precondition: %v", err)
	}

	endInput, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionEnd", "session_id": "sid999", "cwd": repo, "reason": "other",
	})
	if err := Run(bytes.NewReader(endInput), &bytes.Buffer{}, "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("implicit session not removed on SessionEnd: %v", err)
	}
}
```

**Step 2: Run to verify it fails** — Expected: FAIL.

**Step 3: Implement**

```go
// runSessionEnd hard-deletes the implicit session for this session_id. Misses
// (crash/kill/timeout) are backstopped by SweepDeadImplicit on next start.
func runSessionEnd(input hookInput) error {
	if input.CWD == "" || input.SessionID == "" {
		return nil
	}
	_ = session.RemoveImplicit(input.CWD, implicitRand(input.SessionID))
	return nil
}
```

**Step 4: Run to verify it passes** — Expected: PASS.

**Step 5: Commit**

```
feat(hooks): SessionEnd removes implicit session (#118)
```

---

## Task 7: Register the two events in the plugin manifest

**Promotion criteria:** N/A.

**Files:**
- Modify: `hooks/hooks.json`

**Step 1:** No unit test (it's a static manifest). Add two blocks mirroring the
existing ones. `SessionStart` gets a modest timeout; `SessionEnd` MUST stay
small (Claude's default budget is 1.5s) — set `timeout: 5` to be safe but the
handler is a single unlink.

```jsonc
    "SessionStart": [
      { "matcher": ".*", "hooks": [
        { "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/handler", "timeout": 10 }
      ] }
    ],
    "SessionEnd": [
      { "matcher": ".*", "hooks": [
        { "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/handler", "timeout": 5 }
      ] }
    ]
```

**Step 2: Verify JSON parses.** Run `jq . hooks/hooks.json` (via the `jq` tool
or `folio.read` + visual check).

**Step 3: Commit**

```
feat(hooks): register SessionStart/SessionEnd in plugin manifest (#118)
```

---

## Task 8: `merge` from an implicit session = hook-then-push (no rebase/ff)

From a main checkout there is nothing to rebase or ff-merge — the work is
already on the default branch. `merge` becomes: run the pre-merge hook against
HEAD, then push. This is a NEW path, not a branch through
`PrepareMerge`/`FinishMerge` (those assume a feature branch ≠ default).

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/merge/merge.go` (add `MergeImplicit`)
- Modify: the merge MCP tool handler + `sc merge` CLI to detect
  `kind == implicit` and route to `MergeImplicit`. Find the call sites: grep
  `cmd/spinclass/` for `merge.PrepareMerge` / `merge.FinishMerge` and the
  session-key resolution that precedes them.
- Test: `internal/merge/merge_test.go` (or a bats test in Task 9 if the merge
  package has no Go-level test harness — CHECK which exists first)

**Step 1: Understand the existing wiring.** Read the merge tool handler
(`cmd/spinclass/commands_mcp_only.go` or `commands_session.go` — grep for
`FinishMerge`). Identify where it loads the session `State`; that's where the
`kind` branch goes. The hook runner is `runPreMergeHookContext` (unexported in
`internal/merge`); `MergeImplicit` reuses it.

**Step 2: Failing test** — prefer a bats integration test (real git + bare
upstream) since merge is git-heavy. If `internal/merge` has Go tests with a
fixture-repo helper, add there; otherwise defer the test to Task 9's bats and
make THIS task's "test" the bats scenario:

bats sketch (`zz-tests_bats/implicit_sessions.bats`):

```bash
@test "merge from implicit main-checkout session runs hook then pushes, no rebase" {
  # set up a bare upstream + a clone on master with a [hooks].pre-merge that touches a marker
  # materialize an implicit session (pipe SessionStart JSON to the handler)
  # commit on master in the checkout
  # run `sc merge` (or the merge MCP tool) for the implicit session
  # assert: marker file created (hook ran), upstream master advanced (push happened),
  #         and NO rebase occurred (single linear commit, no "rebase" in output)
}
```

**Step 3: Implement `MergeImplicit`**

```go
// MergeImplicit runs the merge path for a main-checkout (implicit) session:
// the pre-merge hook against HEAD, then a push of the current (default) branch.
// There is no rebase or ff-merge — the work is already on the default branch.
// The push is surfaced as its own TAP step so it is never silent.
func MergeImplicit(ctx context.Context, tw *tap.Writer, w io.Writer, repoPath, checkout, branch string, gitSync, verbose bool, activity io.Writer) (blobLinks []check.BlobLink, err error) {
	// disable-merge gate (mirror PrepareMerge's check).
	if home, _ := os.UserHomeDir(); home != "" {
		if h, herr := sweatfileio.LoadWorktreeHierarchy(home, repoPath, checkout); herr == nil && h.Merged.DisableMergeEnabled() {
			return nil, failStep(tw, "merge "+branch, fmt.Errorf("merge disabled by sweatfile; use `sc check`"), "")
		}
	}
	// Pin HEAD and run the hook against it (build-worktree isolation applies).
	pinned, shaErr := git.RevParse(checkout, "HEAD")
	if shaErr != nil {
		return nil, failStep(tw, "merge "+branch, shaErr, "")
	}
	hookLinks, hookErr := runPreMergeHookContext(ctx, tw, w, repoPath, checkout, branch, pinned, activity)
	blobLinks = append(blobLinks, hookLinks...)
	if hookErr != nil {
		return blobLinks, hookErr
	}
	// Push the default branch.
	if out, perr := git.Push(checkout); perr != nil { // confirm git.Push signature/args
		return blobLinks, failStep(tw, "push "+branch, perr, out)
	} else if tw != nil {
		tw.Ok("push " + branch)
	}
	return blobLinks, nil
}
```

(Confirm the real `git.Push` signature — grep `internal/git`. If push needs an
explicit remote/branch, pass them. `runPreMergeHookContext` and `failStep` are
package-private in `internal/merge`, so `MergeImplicit` belongs in that package.)

**Step 4: Route to it.** In the merge handler, after loading the `State`:

```go
if st.Kind == session.KindImplicit {
    // implicit: hook-then-push, no PrepareMerge/FinishMerge
    blobLinks, err = merge.MergeImplicit(ctx, tw, &buf, st.RepoPath, st.WorktreePath, st.Branch, gitSync, verbose, activity)
} else {
    // existing PrepareMerge + FinishMerge path, unchanged
}
```

**Step 5: Run** the bats scenario — Expected: PASS (hook marker present,
upstream advanced, no rebase).

**Step 6: Commit**

```
feat(merge): hook-then-push merge path for implicit sessions (#118)

A main-checkout session has nothing to rebase/ff-merge; merge runs the
pre-merge hook against HEAD then pushes the default branch. Push is a
distinct TAP step.
```

---

## Task 9: `close` is state-only for implicit; `sc list` marker; bats coverage

**Promotion criteria:** N/A.

**Files:**
- Modify: the `close` handler (grep `cmd/spinclass/` for `session.Remove` /
  `session.Tombstone` / `git worktree remove`) — branch on `kind == implicit`
  to call `session.RemoveImplicit` and SKIP any worktree removal.
- Modify: `sc list` rendering (`session.ListAll` consumers in
  `cmd/spinclass/commands_query.go`) — show a `main` marker for `kind: implicit`
  rows.
- Test: `zz-tests_bats/implicit_sessions.bats` (extend Task 8's file)

**Step 1: Failing bats tests**

```bash
@test "close on an implicit session removes state but never the checkout" {
  # materialize implicit session in a main checkout with tracked files
  # run `sc close <key>`
  # assert: state-<rand>.json gone; checkout dir + its tracked files still present
}

@test "sc list marks implicit sessions with a main marker" {
  # materialize an implicit session
  # run `sc list`
  # assert: the row carries the implicit/main marker
}
```

**Step 2: Run to verify they fail** (close may try `git worktree remove` on the
checkout → error or wrong behavior; list shows no marker).

**Step 3: Implement** the two branches:
- close: `if st.Kind == session.KindImplicit { return session.RemoveImplicit(st.WorktreePath, randFromKey(st.SessionKey)) }` before any worktree-removal logic. Derive rand from the key suffix (everything after the last `-`) or re-derive; simplest is to store/parse it. (If parsing the key is fragile, add a `Rand` field to `State` in Task 1 — decide during impl; prefer parsing the suffix to avoid schema churn.)
- list: in the row formatter, append a `main` tag when `row.Kind == KindImplicit`.

**Step 4: Run to verify they pass.**

**Step 5: Commit**

```
feat(close,list): implicit sessions close state-only + list main marker (#118)
```

---

## Task 10: Verify chat works from an implicit session

Chat should "just work" once `currentSessionKey()` resolves via the implicit
state file. `currentSessionKey` (`cmd/spinclass/commands_mcp_only.go:860`)
prefers `$SPINCLASS_SESSION_ID`; if the SessionStart handler does NOT export
that into the agent's env (hooks can't set the agent's process env directly —
only `CLAUDE_ENV_FILE` for Bash subcommands), the MCP server process won't see
it. So `currentSessionKey` must gain an implicit-session fallback: when not in
a worktree and no env var, look for `<cwd>/.spinclass/state-*.json` and resolve
the key from it.

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/spinclass/commands_mcp_only.go:860-884` (`currentSessionKey`
  fallback)
- Test: bats scenario (chat-list-sessions / chat-send from a main checkout)

**Step 1: VERIFY the env assumption.** Before coding: does the spinclass MCP
`serve` process running under a main-checkout Claude session see
`$SPINCLASS_SESSION_ID`? It will NOT unless something exports it. The
SessionStart hook runs as a subprocess and cannot mutate the parent agent's
env. So the fallback is REQUIRED. Confirm by reading `currentSessionKey` and
tracing whether anything else sets the var for implicit sessions. Do not assume
— trace it.

**Step 2: Failing bats test**

```bash
@test "chat-list-sessions shows an implicit main-checkout session" {
  # materialize an implicit session in a main checkout
  # invoke the chat-list-sessions MCP tool (or `sc` equivalent) from that cwd
  # assert: the implicit session key appears
}
```

**Step 3: Implement the fallback** in `currentSessionKey`: after the
`$SPINCLASS_SESSION_ID` check and the worktree check, before erroring, glob
`<cwd>/.spinclass/state-*.json`, read the first live (PID-alive) one, return its
`SessionKey`. If multiple live ones exist (multiple agents, one cwd), this is
ambiguous — pick the one whose PID matches the current process tree if
determinable, else return a clear error listing the candidate keys (mirroring
the non-TTY remote-resume error style). DECIDE during impl based on what's
tractable; document the choice in a comment.

**Step 4: Run to verify it passes.**

**Step 5: Commit**

```
feat(chat): resolve implicit session key for main-checkout agents (#118)

currentSessionKey falls back to <cwd>/.spinclass/state-*.json when not in
a worktree and SPINCLASS_SESSION_ID is unset, so chat works from a main
checkout.
```

---

## Task 11: Docs — FDR + CLAUDE.md + sweatfile manpage

**Promotion criteria:** N/A.

**Files:**
- Create: `docs/features/00NN-implicit-sessions.md` (use `eng:fdr` skill; next
  number after the highest existing in `docs/features/`)
- Modify: `CLAUDE.md` (document implicit sessions under Session state / a new
  subsection; note the `kind:implicit`, the lifecycle hooks, the merge=push
  semantics, the rollback knob)
- Modify: `cmd/spinclass/doc/spinclass-sweatfile.5` (document
  `disable-implicit-sessions`)

**Step 1:** Invoke `eng:fdr` to author the feature record from the design doc +
what was actually built (capture any deviations discovered during impl,
especially the PID semantics and the chat env-fallback).

**Step 2:** Update CLAUDE.md and the manpage. No test; prose.

**Step 3: Commit**

```
docs: FDR + CLAUDE.md + manpage for implicit sessions (#118)
```

---

## Final verification

After Task 11, run the full suite once via the merge hook (do NOT pre-run
`just`): call `merge-this-session`. Its `[hooks].pre-merge` runs `just`
(build + test + bats + analyzers) in the isolated build worktree. If it fails,
that is the CI signal — investigate from there.

`Closes #118` belongs in the final merge/commit message so GitHub auto-closes.

---

## Open implementation decisions (resolve in-flight, do not guess)

1. **Which PID to record** for liveness reaping (Task 5) — `os.Getppid()` may
   be a transient shell. Observe empirically; PID-liveness is best-effort
   backstop, not the primary reaper.
2. **rand storage vs key-parse** (Task 9) — parse the `-<rand>` suffix off the
   session key, or add a `Rand` field to `State`. Prefer parsing; add the field
   only if parsing proves fragile.
3. **Multiple live implicit sessions, one cwd, chat resolution** (Task 10) —
   how `currentSessionKey` disambiguates. Match process tree if tractable, else
   clear multi-candidate error.
4. **`git.Push` / `git.DefaultBranch` exact signatures** — confirm by reading
   `internal/git` before writing Tasks 5 and 8; the plan uses placeholder
   names.
