# Remote Sessions (`host:` routing) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** `sc list` and tab completion include sessions from sweatfile-declared remote hosts, and `sc resume host:id` reattaches over SSH.

**Architecture:** Prefix routing at the CLI boundary (approved design: `docs/plans/2026-06-06-remote-sessions-design.md`). A new `internal/remote` package owns target parsing, remote config resolution, the SSH list query + completion cache, and attach-argv construction. The sweatfile gains a `[[remotes]]` array of tables (dedup-by-name merge). `sc list` gains `--format json` as both the local machine format and the remote wire format.

**Tech Stack:** Go; tommy-generated TOML codec (regen via `just gen-tommy`); stub-binary test pattern (fake `ssh` on PATH, as in `internal/clown`'s tests).

**Rollback:** N/A — purely additive; no `[[remotes]]` configured = today's behavior. Removing config entries disables everything.

**Conventions for the implementer (read first):**
- TDD per @eng:test-driven-development — every step pair below is RED then GREEN; run the named test and SEE it fail before implementing.
- Tests: `hamster.go-test` MCP tool with `packages` + `run` + `cwd=<worktree>`; never bare `go test` via Bash.
- After any `Sweatfile` struct change you MUST run `just gen-tommy` (regenerates `internal/sweatfile/sweatfile_tommy.go`); see issue #50 for the known manual-patch tax — if the generated file needs hand-patching, STOP and surface it.
- `git add` every new file before any `nix build` (nix only sees tracked files).
- Commit after each task with the message given; do NOT run `just` before merge (the merge hook is the CI lane).

---

### Task 1: `[[remotes]]` sweatfile config + merge + validate

**Files:**
- Modify: `internal/sweatfile/sweatfile.go` (struct after `StartCommand`, ~line 64; `Sweatfile` struct fields ~line 75-85)
- Modify: the merge function that handles `StartCommands` dedup-by-name (find with `rg "StartCommands" internal/sweatfile/merge*.go` — mirror it exactly for `Remotes`)
- Modify: `internal/validate/` (find the start-command validation to mirror: `rg "exec-start" internal/validate/`)
- Test: `internal/sweatfile/remotes_test.go` (new), extend the existing merge/validate test files alongside their current patterns

**Step 1: Write the failing struct/parse test**

```go
// internal/sweatfile/remotes_test.go
package sweatfile

import "testing"

func TestRemotesParse(t *testing.T) {
	sf := parseTestSweatfile(t, `
[[remotes]]
name = "devbox"
ssh = "sasha@devbox.lan"
attach = ["ssh", "-t", "{ssh}", "spinclass", "resume", "{id}"]
`) // use the package's existing test parse helper; rg "func parse" internal/sweatfile/*_test.go
	if len(sf.Remotes) != 1 {
		t.Fatalf("remotes: got %d, want 1", len(sf.Remotes))
	}
	r := sf.Remotes[0]
	if r.Name != "devbox" || r.SSH != "sasha@devbox.lan" || len(r.Attach) != 6 {
		t.Fatalf("remote: %+v", r)
	}
}

func TestRemotesMergeDedupByName(t *testing.T) {
	// Mirror the existing TestStartCommandsMerge* shape: parent declares
	// devbox, child re-declares devbox (override) and adds lab; merged
	// result has child's devbox + lab. A child name-only entry (no ssh,
	// no attach) removes an inherited remote.
}
```

**Step 2: Run to verify failure**

`hamster.go-test packages=./internal/sweatfile/... run=TestRemotes` → FAIL (no `Remotes` field).

**Step 3: Implement**

```go
// internal/sweatfile/sweatfile.go — after StartCommand
// Remote declares a host whose spinclass sessions appear in sc list and
// completion under a "<name>:" prefix and can be reattached via sc resume.
// See docs/plans/2026-06-06-remote-sessions-design.md.
type Remote struct {
	Name   string   `toml:"name"`
	SSH    string   `toml:"ssh"`    // ssh destination; empty = Name
	Attach []string `toml:"attach"` // argv template; {ssh}/{id} substituted; empty = default
}
```

Add `Remotes []Remote \`toml:"remotes"\`` to `Sweatfile`; add a `Dest()` method (`SSH` or fallback `Name`); copy the `StartCommands` dedup-by-name merge verbatim for `Remotes` (name-only entry = removal, matching `[[mcps]]`).

**Step 4: Regenerate the tommy codec**

Run: `just gen-tommy` (just-us-agents run-recipe). Expected: `sweatfile_tommy.go` regenerated, tests compile. If the generated code needs manual patching, STOP and report (issue #50).

**Step 5: Run tests** → PASS. Also run the whole package: `hamster.go-test packages=./internal/sweatfile/...`.

**Step 6: Validate rules + test**

RED: a validate test rejecting `name = "bad:name"`, `name = "bad/name"`, and `attach = []`-with-other-fields-present... (empty attach array = use default, so only reject attach entries that are present but contain an empty string element). Mirror the existing start-command validation tests. GREEN: implement in the validate package next to the start-command rules.

**Step 7: Commit**

`feat(sweatfile): [[remotes]] config for host:-prefixed remote sessions`

---

### Task 2: `internal/remote` — target grammar + attach argv

**Files:**
- Create: `internal/remote/remote.go`, `internal/remote/remote_test.go`

**Step 1: RED tests**

```go
package remote

// ParseTarget("devbox:crisp-catalpa") => ("devbox", "crisp-catalpa", true)
// ParseTarget("crisp-catalpa")        => ("", "", false)
// ParseTarget("a/b:c")                => ("", "", false)  // prefix may not contain /
// ParseTarget(":x") / ("x:")          => ("", "", false)
//
// AttachArgv(Remote{Name:"devbox"}, "crisp-catalpa") =>
//   ["ssh","-t","devbox","spinclass","resume","crisp-catalpa"]   // default template, Dest()=Name
// AttachArgv with explicit Attach template substitutes {ssh} and {id}
// in every element (literal replacement, no shell).
```

Write them as table tests asserting exact argv slices.

**Step 2:** Run → FAIL (package missing). **Step 3:** Implement: regexp `^([^:/]+):(.+)$` for ParseTarget; `AttachArgv` does `strings.ReplaceAll` per element over the template, defaulting to the design's ssh argv. The `Remote` type here ALIASES `sweatfile.Remote` (use it directly; do not duplicate the struct). **Step 4:** PASS. **Step 5:** Commit `feat(remote): host: target grammar + attach argv construction`.

---

### Task 3: `sc list --format json`

**Files:**
- Modify: `cmd/spinclass/commands_query.go` (the `list` command, ~line 26; find its render func)
- Test: alongside existing list tests (`rg "list" cmd/spinclass/*_test.go` and `internal/...` — put the JSON render test wherever list rendering is currently tested; if rendering is untested, test the new JSON marshal path at its narrowest seam)

**Step 1: RED** — a test asserting the JSON output shape for a fixed `[]session.State`:

```json
[{"id":"crisp-catalpa","session_key":"spinclass/crisp-catalpa","state":"active","description":"...","repo":"spinclass"}]
```

One row per non-abandoned session; stable field names (this is the remote wire format — treat as a contract, document the struct with json tags in one exported type, e.g. `session.ListRow`).

**Step 2:** FAIL. **Step 3:** Implement: extend the list command's `--format` handling with `json` (the global `--format` currently accepts tap/table; wire `json` through the same switch). **Step 4:** PASS. **Step 5:** Commit `feat(list): --format json — machine-readable rows (remote wire format)`.

---

### Task 4: `internal/remote` — host query + completion cache

**Files:**
- Modify: `internal/remote/remote.go` (+ `query.go`, `cache.go` if cleaner)
- Test: `internal/remote/query_test.go` with a stub `ssh` ON PATH (write a temp dir script `ssh` that records argv and prints canned `ListRow` JSON; PREPEND the dir to PATH via `t.Setenv("PATH", dir+":"+os.Getenv("PATH"))`). Mirror `internal/clown/clown_test.go`'s stub pattern.

**Step 1: RED tests**

- `QueryHost(ctx, r)` runs `ssh <Dest()> spinclass list --format json`, parses rows, returns them; per-call timeout (3s default, a package const documented as a tuning lever).
- Stub exits 1 / prints garbage → error returned (not a panic), no partial rows.
- `WriteCache(name, rows)` + `ReadCache(name)` round-trip under `$XDG_STATE_HOME/spinclass/remotes/<name>.json` (t.Setenv XDG_STATE_HOME; mirror `internal/chat`'s xdgStateBase pattern — copy the 6-line helper, do not import chat).
- `ReadAllCaches(remotes)` returns rows tagged by remote name; missing cache files are silently empty.

**Step 2:** FAIL. **Step 3:** Implement (`exec.CommandContext`, `context.WithTimeout`; atomic cache write = temp+rename, mirroring `chat.Send`). **Step 4:** PASS. **Step 5:** Commit `feat(remote): ssh list query + completion cache`.

---

### Task 5: list integration — parallel fan-out + prefixed rows

**Files:**
- Modify: `cmd/spinclass/commands_query.go` (list handler: after local rows, fan out)
- Test: handler-level test with stub `ssh` (same pattern), 2 configured remotes, one healthy one timing-out: output contains `devbox:` rows, a `lab: unreachable` diagnostic row, exit code 0; cache file written for the healthy host.

Implementation notes: load the merged sweatfile for cwd (the existing `mergedSweatfileForCwd` helper in cmd/spinclass — rg it); `sync.WaitGroup` over remotes, each result into a slice slot (no mutex needed with per-index writes); render after local rows; never fail the command for a host error. Honor `--format json` by including remote rows with a `"remote":"devbox"` field.

Commit: `feat(list): include sweatfile-declared remote sessions (parallel, per-host isolation)`.

---

### Task 6: completion integration — cache-only

**Files:**
- Modify: `cmd/spinclass/commands_session.go:212` (`completeWorktreeTargets`)
- Test: extend the cmd test pattern — seed a cache file, assert the completion map contains `devbox:crisp-catalpa` with a label carrying state+description, and that NO `ssh` stub is invoked (completion must never network — assert the stub's record file does not exist).

Commit: `feat(complete): host:-prefixed remote sessions from the list cache`.

---

### Task 7: resume routing + close/merge rejection

**Files:**
- Modify: `cmd/spinclass/commands_session.go` (`runResume` ~line 316: before `session.FindByID`, parse the target; on a remote match, build AttachArgv and exec); the close handler and `sc merge` target resolution get an explicit `host:` rejection ("remote targets support resume only (v1)").
- Test: resume-routing test with a stub `ssh` asserting the exec'd argv (use the executor seam if runResume execs via `executor`; otherwise test the argv-builder + a thin runRemoteResume func — check how `attachSession` execs and mirror the test seam used by `mockExecutor` in internal/shop tests). Close/merge rejection: handler tests asserting the error text.

Implementation note: exec must replace the process with full TTY passthrough — use the same mechanism `executor.SessionExecutor` uses (rg `syscall.Exec|cmd.Run` in internal/executor) rather than inventing a new one.

Commit: `feat(resume): route host:-prefixed targets over the remote attach template`.

---

### Task 8: docs + FDR

- Update `CLAUDE.md`: CLI table (`sc list`/`resume` mention `host:`), Sweatfile config section (`[[remotes]]`), and a short remote-sessions paragraph.
- Write FDR `docs/features/0011-remote-sessions.md` via @eng:fdr (status `experimental`; tuning levers from the design doc: 3s timeout, unbounded cache staleness, unbounded fan-out, non-abandoned scope; limitation: resume-only v1, remote needs same-or-newer spinclass for `--format json`).
- Commit: `docs: remote-sessions FDR + CLAUDE.md`.

---

### Task 9: pre-merge cycle + merge

Run the repo's required pre-merge skills (simplify, review, eng:loose-ends, eng:doc-drift), attest via `nothing-but-the-truth`, then `merge-this-session-async` with git_sync and wait for the clown wake. The hook IS the CI lane — do not run `just` beforehand.
