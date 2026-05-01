# `[hooks].disable-merge` Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add a sweatfile flag (`[hooks].disable-merge`) that disables `sc merge` and conditionally hides the `merge-this-session` MCP tool, plus a new `sc check` / `check-this-session` surface that runs the existing `[hooks].pre-merge` command independently.

**Architecture:** Three distinct pieces stitched together. (1) A new `*bool` field on `Hooks` parsed by tommy-generated code, with a `DisableMergeEnabled()` accessor and scalar-override merge semantics matching siblings like `disallow-main-worktree`. (2) Two block points: `merge.Run` short-circuits with a TAP error before any git op when the flag is set; `registerMCPOnlyCommands` reads the sweatfile at `serve` startup and skips registering `merge-this-session` when disabled. (3) An additive `check` package that extracts the existing `runPreMergeHook` body into a reusable function, fronted by `sc check` and `check-this-session`.

**Tech Stack:** Go (modules in `internal/`); `command.App` framework from `purse-first/libs/go-mcp`; tommy TOML library; tap-dancer for TAP-14 output; charm/huh for interactive prompts. Tests use the Go std-lib `testing` package; bats for end-to-end CLI flows.

**Rollback:** Default behavior (flag unset/false) is unchanged, so the feature is opt-in. Per-user rollback: delete the line. Project-level rollback: `git revert` the merge commit. Sweatfiles with `disable-merge` after revert parse as unknown-key (tommy is permissive — verify in Task 2 step 4) and behave as if the flag were absent.

---

## Conventions for every task

- All commands assume cwd is the worktree root (`.worktrees/bold-baobab`).
- Tests follow existing patterns. For the sweatfile field, mirror `TestParseHooksDisallowMainWorktree` / `TestMergeDisallowMainWorktreeInherit` / `TestMergeDisallowMainWorktreeOverride` in `internal/sweatfile/sweatfile_test.go`.
- Commit after each task with a Conventional-Commits-style message; include `:clown: Designed-with: Clown <https://github.com/amarbel-llc/clown>` trailer (per the agent's signing convention).
- If `git commit` fails with a pivy-agent / GPG error, **stop** and ask the user to unlock pivy-agent. Do not bypass with `--no-verify`.
- Run the full test suite (`just test`) at the end of each task; the task is not "done" until all tests pass.

---

## Task 1: Add the `disable-merge` field to the `Hooks` struct

**Promotion criteria:** N/A — purely additive.

**Files:**
- Modify: `internal/sweatfile/sweatfile.go` (struct + accessor)
- Modify: `internal/sweatfile/hierarchy.go:178-204` (merge logic)
- Test: `internal/sweatfile/sweatfile_test.go`

**Step 1: Write the failing accessor + merge tests**

Append to `internal/sweatfile/sweatfile_test.go`:

```go
func TestParseHooksDisableMerge(t *testing.T) {
	input := `
[hooks]
disable-merge = true
`
	doc, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if !sf.DisableMergeEnabled() {
		t.Error("expected disable-merge to be enabled")
	}
}

func TestParseHooksDisableMergeAbsent(t *testing.T) {
	doc, err := Parse([]byte("[git]\nexcludes = [\".claude/\"]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if sf.DisableMergeEnabled() {
		t.Error("expected disable-merge to be disabled when absent")
	}
}

func TestMergeDisableMergeInherit(t *testing.T) {
	enabled := true
	base := Sweatfile{Hooks: &Hooks{DisableMerge: &enabled}}
	repo := Sweatfile{}
	merged := base.MergeWith(repo)
	if !merged.DisableMergeEnabled() {
		t.Error("expected inherited disable-merge")
	}
}

func TestMergeDisableMergeOverride(t *testing.T) {
	enabled := true
	disabled := false
	base := Sweatfile{Hooks: &Hooks{DisableMerge: &enabled}}
	repo := Sweatfile{Hooks: &Hooks{DisableMerge: &disabled}}
	merged := base.MergeWith(repo)
	if merged.DisableMergeEnabled() {
		t.Error("expected overridden disable-merge to be disabled")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/sweatfile/ -run 'TestParseHooksDisableMerge|TestMergeDisableMerge' -v`
Expected: FAIL — `Hooks` has no `DisableMerge` field; `Sweatfile` has no `DisableMergeEnabled()` method.

**Step 3: Add the struct field**

In `internal/sweatfile/sweatfile.go`, add to the `Hooks` struct (mirror `DisallowMainWorktree`):

```go
type Hooks struct {
	Create               *string `toml:"create"`
	Stop                 *string `toml:"stop"`
	PreMerge             *string `toml:"pre-merge"`
	OnAttach             *string `toml:"on-attach"`
	OnDetach             *string `toml:"on-detach"`
	DisallowMainWorktree *bool   `toml:"disallow-main-worktree"`
	ToolUseLog           *bool   `toml:"tool-use-log"`
	DisableMerge         *bool   `toml:"disable-merge"`
}
```

Add the accessor below `ToolUseLogEnabled`:

```go
func (sf Sweatfile) DisableMergeEnabled() bool {
	return sf.Hooks != nil &&
		sf.Hooks.DisableMerge != nil &&
		*sf.Hooks.DisableMerge
}
```

**Step 4: Add merge logic**

In `internal/sweatfile/hierarchy.go`, append to the `[hooks]` block (after the `ToolUseLog` clause around line 201):

```go
		if other.Hooks.DisableMerge != nil {
			merged.Hooks.DisableMerge = other.Hooks.DisableMerge
		}
```

**Step 5: Regenerate tommy code**

Run: `cd internal/sweatfile && go generate`
Expected: `sweatfile_tommy.go` updated with new `disable-merge` decode/encode for both nested-table and root-level paths (mirroring how `tool-use-log` appears in 4 places per the existing file). If `go generate` fails because `tommy` isn't in PATH, ask the user — do not edit the generated file by hand unless the user says so.

If `tommy` cannot be invoked, document the failure and add the field manually by **copying** the four `tool-use-log` blocks in `sweatfile_tommy.go` and renaming the strings/identifiers to `disable-merge` / `DisableMerge`. Do not invent new patterns.

**Step 6: Run tests to verify they pass**

Run: `go test ./internal/sweatfile/ -run 'TestParseHooksDisableMerge|TestMergeDisableMerge' -v`
Expected: PASS, all 4 tests.

Run the full sweatfile suite:
Run: `go test ./internal/sweatfile/ -v`
Expected: all PASS, no regressions.

**Step 7: Commit**

```bash
git add internal/sweatfile/sweatfile.go internal/sweatfile/hierarchy.go internal/sweatfile/sweatfile_tommy.go internal/sweatfile/sweatfile_test.go
git commit -m "feat(sweatfile): add [hooks].disable-merge field

:clown: Designed-with: Clown <https://github.com/amarbel-llc/clown>"
```

---

## Task 2: Block `sc merge` when `disable-merge=true`

**Promotion criteria:** N/A — additive guard.

**Files:**
- Modify: `internal/merge/merge.go` (early-return in `Resolved`)
- Test: `internal/merge/merge_test.go`

**Step 1: Inspect existing merge tests**

Run: `rg "func Test" internal/merge/merge_test.go`
Look at one existing test (e.g., the rebase or merge-success path) to understand the harness — there's a `mockExecutor`, the helper to set up a temp repo, etc. Reuse that scaffolding.

**Step 2: Write the failing test**

Append to `internal/merge/merge_test.go`:

```go
func TestRunDisabledByMergeFlag(t *testing.T) {
	// Set up a temp repo + worktree with a sweatfile that disables merge.
	repoPath, wtPath, branch := setupRepoWithWorktree(t)

	sweatfilePath := filepath.Join(wtPath, "sweatfile")
	if err := os.WriteFile(sweatfilePath, []byte("[hooks]\ndisable-merge = true\n"), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	var buf bytes.Buffer
	err := Resolved(
		executor.ShellExecutor{},
		&buf,
		nil,
		"tap",
		repoPath,
		wtPath,
		branch,
		"master",
		false, // gitSync
		false, // inSession
		false, // verbose
	)

	if err == nil {
		t.Fatal("expected error when disable-merge is set, got nil")
	}
	if !strings.Contains(err.Error(), "merge disabled") {
		t.Errorf("expected 'merge disabled' in error, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "disable-merge") {
		t.Errorf("expected TAP output to mention 'disable-merge', got: %s", out)
	}
	if !strings.Contains(out, "sc check") {
		t.Errorf("expected TAP hint 'sc check', got: %s", out)
	}

	// Crucially: no rebase, merge, or worktree removal happened.
	// Verify branch HEAD is unchanged from setup.
	headAfter, _ := git.RunCapture(repoPath, "rev-parse", "HEAD")
	headWtAfter, _ := git.RunCapture(wtPath, "rev-parse", "HEAD")
	if headAfter == headWtAfter {
		t.Error("repo HEAD and worktree HEAD should differ when merge is blocked; merge appears to have run")
	}
}
```

> **NOTE:** the exact helper names (`setupRepoWithWorktree`, `git.RunCapture`) are placeholders — adapt to whatever already exists in `merge_test.go`. If no helper exists, factor out the boilerplate from a sibling test rather than building a new one.

**Step 3: Run test to verify it fails**

Run: `go test ./internal/merge/ -run TestRunDisabledByMergeFlag -v`
Expected: FAIL — merge currently proceeds regardless of the flag.

**Step 4: Implement the guard**

In `internal/merge/merge.go`, inside `Resolved` (the function that does the real work), insert this block **after** the repo-stat check (around line 73) but **before** any git operation:

```go
	if home, _ := os.UserHomeDir(); home != "" {
		hierarchy, hErr := sweatfile.LoadWorktreeHierarchy(home, repoPath, wtPath)
		if hErr == nil && hierarchy.Merged.DisableMergeEnabled() {
			source := disableMergeSource(hierarchy)
			msg := fmt.Sprintf(
				"merge disabled by sweatfile (disable-merge=true at %s); use `sc check` to run the pre-merge hook without merging",
				source,
			)
			if tw != nil {
				tw.NotOk("merge "+branch, map[string]string{
					"severity": "fail",
					"message":  msg,
				})
				if ownWriter {
					tw.Plan()
				}
			}
			return errors.New(msg)
		}
	}
```

Add the helper at the bottom of the file:

```go
// disableMergeSource returns the path of the most-specific sweatfile in
// the hierarchy that set DisableMerge to a non-nil value, or "<unknown>"
// if none can be located.
func disableMergeSource(h sweatfile.Hierarchy) string {
	for i := len(h.Sources) - 1; i >= 0; i-- {
		s := h.Sources[i]
		if !s.Found {
			continue
		}
		if s.File.Hooks != nil && s.File.Hooks.DisableMerge != nil {
			return s.Path
		}
	}
	return "<unknown>"
}
```

**Step 5: Run test to verify it passes**

Run: `go test ./internal/merge/ -run TestRunDisabledByMergeFlag -v`
Expected: PASS.

Run the full merge suite:
Run: `go test ./internal/merge/ -v`
Expected: all PASS — existing merge tests should still pass because they don't write `disable-merge=true`.

**Step 6: Commit**

```bash
git add internal/merge/merge.go internal/merge/merge_test.go
git commit -m "feat(merge): block sc merge when [hooks].disable-merge is set

:clown: Designed-with: Clown <https://github.com/amarbel-llc/clown>"
```

---

## Task 3: Extract `runPreMergeHook` into a reusable `check` package

**Promotion criteria:** N/A — pure refactor; behavior must remain identical for `merge`.

**Files:**
- Create: `internal/check/check.go`
- Create: `internal/check/check_test.go`
- Modify: `internal/merge/merge.go:399-437` (replace inline `runPreMergeHook` with a call into `check`)

**Step 1: Read the current `runPreMergeHook` carefully**

Read: `internal/merge/merge.go:399-466` (the function and its `lineWriter` helper).
Note: it loads the hierarchy, checks the hook command, runs it via `hierarchy.Merged.RunPreMergeHook(wtPath, w)`, and threads TAP `OutputBlock` for streaming output. The `lineWriter` is local to the file.

**Step 2: Write a failing test for the new package**

Create `internal/check/check_test.go`:

```go
package check_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/check"
)

func TestRunHookSuccessTAP(t *testing.T) {
	wtPath := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(wtPath, "sweatfile"),
		[]byte("[hooks]\npre-merge = \"true\"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	var buf bytes.Buffer
	err := check.Run(&buf, "tap", wtPath, false /* verbose */)
	if err != nil {
		t.Fatalf("expected hook success, got: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("ok")) {
		t.Errorf("expected TAP 'ok', got: %s", buf.String())
	}
}

func TestRunHookFailureTAP(t *testing.T) {
	wtPath := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(wtPath, "sweatfile"),
		[]byte("[hooks]\npre-merge = \"false\"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	var buf bytes.Buffer
	err := check.Run(&buf, "tap", wtPath, false)
	if err == nil {
		t.Fatal("expected error when hook exits non-zero")
	}
	if !bytes.Contains(buf.Bytes(), []byte("not ok")) {
		t.Errorf("expected TAP 'not ok', got: %s", buf.String())
	}
}

func TestRunNoHookConfigured(t *testing.T) {
	wtPath := t.TempDir()
	// No sweatfile, no hook configured.

	var buf bytes.Buffer
	err := check.Run(&buf, "tap", wtPath, false)
	if err != nil {
		t.Fatalf("expected nil error when no hook, got: %v", err)
	}
}
```

**Step 3: Run tests to verify they fail**

Run: `go test ./internal/check/ -v`
Expected: FAIL — package doesn't exist.

**Step 4: Create the new package**

Create `internal/check/check.go`:

```go
// Package check runs the [hooks].pre-merge command in a worktree
// independently of `sc merge`. It is the agent-CI surface invoked by
// `sc check` and the `check-this-session` MCP tool.
package check

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	tap "github.com/amarbel-llc/bob/packages/tap-dancer/go"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// Run resolves the worktree containing wtPath (or wtPath itself), loads
// the sweatfile hierarchy, and runs the configured [hooks].pre-merge
// command. It writes TAP-14 output (when format == "tap") or passthrough
// output otherwise to w. Returns a non-nil error if the hook fails.
//
// If no pre-merge hook is configured, Run returns nil and emits a single
// `ok` (TAP) or no output (passthrough) — agents and humans should treat
// "no hook" as a success because there is nothing to check.
func Run(w io.Writer, format, wtPath string, verbose bool) error {
	repoPath, err := git.CommonDir(wtPath)
	if err != nil {
		return fmt.Errorf("not a worktree: %s", wtPath)
	}
	branch, err := git.BranchCurrent(wtPath)
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
	}

	home, _ := os.UserHomeDir()
	if home == "" {
		return errors.New("could not resolve home directory")
	}
	hierarchy, err := sweatfile.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil {
		return fmt.Errorf("load sweatfile hierarchy: %w", err)
	}
	cmd := hierarchy.Merged.PreMergeHookCommand()

	var tw *tap.Writer
	ownWriter := false
	if format == "tap" {
		tw = tap.NewWriter(w)
		ownWriter = true
	}

	if cmd == nil || *cmd == "" {
		if tw != nil {
			tw.Ok("no pre-merge hook configured")
			if ownWriter {
				tw.Plan()
			}
		}
		return nil
	}

	desc := "pre-merge hook for " + branch + ": `" + *cmd + "`"

	if tw == nil {
		return hierarchy.Merged.RunPreMergeHook(wtPath, w)
	}

	var hookErr error
	tw.OutputBlock(desc, func(ob *tap.OutputBlockWriter) *tap.Diagnostics {
		lw := &lineWriter{ob: ob}
		hookErr = hierarchy.Merged.RunPreMergeHook(wtPath, lw)
		lw.Flush()
		if hookErr != nil {
			return &tap.Diagnostics{Severity: "fail", Message: hookErr.Error()}
		}
		return nil
	})
	if ownWriter {
		tw.Plan()
	}
	return hookErr
}

// Suppress "unused import" if `worktree` ends up not being needed —
// keep the import only if you use worktree.IsWorktree somewhere above.
var _ = worktree.IsWorktree

// lineWriter splits incoming bytes on '\n' and forwards each complete
// line to an OutputBlockWriter. Partial trailing content is buffered
// until a newline arrives or Flush() is called.
//
// Copied verbatim from internal/merge/merge.go to keep the two packages
// independently buildable. If you find yourself touching both copies,
// consider promoting it to a shared internal helper — but do that as a
// separate refactor, not in this task.
type lineWriter struct {
	ob  *tap.OutputBlockWriter
	buf []byte
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.buf = append(lw.buf, p...)
	for {
		i := bytes.IndexByte(lw.buf, '\n')
		if i < 0 {
			break
		}
		lw.ob.Line(string(lw.buf[:i]))
		lw.buf = lw.buf[i+1:]
	}
	return len(p), nil
}

func (lw *lineWriter) Flush() {
	if len(lw.buf) == 0 {
		return
	}
	lw.ob.Line(string(lw.buf))
	lw.buf = nil
}
```

> **NOTE for the implementer:** the test directory `t.TempDir()` is not a git worktree, so `git.CommonDir` will fail in the tests. Either (a) initialize a temp git repo + worktree in the tests (cleaner), or (b) factor `Run` so `repoPath`/`branch` discovery is skipped when the hook isn't configured, and accept that the hook-runs-the-command tests need a real worktree. Pick (a) and reuse whatever helper `internal/merge/merge_test.go` has, even if you have to copy it into `check_test.go`.

**Step 5: Run tests to verify they pass**

Run: `go test ./internal/check/ -v`
Expected: PASS for all 3 tests.

**Step 6: Replace the inline runner in `merge.go` with a call into `check`**

In `internal/merge/merge.go`, the existing `runPreMergeHook` function is structurally identical to the new `check.Run` — but it's wired into the merge's existing `tap.Writer`, not its own. Two options:

- **(a) Cleaner:** make `check` expose a lower-level helper that takes a `*tap.Writer` so merge can pass its own. New API: `check.RunWithWriter(tw *tap.Writer, w io.Writer, hierarchy sweatfile.Hierarchy, wtPath, branch string, ownWriter bool) error`. Merge calls that; the standalone `Run` calls into `RunWithWriter` after building its own writer.
- **(b) Simpler:** leave merge's `runPreMergeHook` alone for this task and just have `check` duplicate the logic. Promote the shared helper later.

Pick **(a)**. It is the right factoring even if it adds 30 minutes. Update merge's `runPreMergeHook` to delegate.

After updating, run:
Run: `go test ./internal/merge/ ./internal/check/ -v`
Expected: PASS for both packages, no regressions.

**Step 7: Run the integration test**

Run: `go test ./cmd/spinclass/ -run TestServeMergeThisSessionStdioIntegrity -v`
Expected: PASS — the integration test exercises the merge → pre-merge-hook flow, so it implicitly covers the refactored path.

**Step 8: Commit**

```bash
git add internal/check/ internal/merge/merge.go
git commit -m "refactor: extract pre-merge hook runner into internal/check

Pulls runPreMergeHook out of internal/merge so it can be invoked
independently by sc check and check-this-session. Behavior preserved.

:clown: Designed-with: Clown <https://github.com/amarbel-llc/clown>"
```

---

## Task 4: Add `sc check` CLI command

**Promotion criteria:** N/A — additive.

**Files:**
- Modify: `cmd/spinclass/commands_session.go` (add registration after the `merge` command)
- Test: `zz-tests_bats/sweatfile.bats` (or a new bats file)

**Step 1: Write a failing bats test**

Append to `zz-tests_bats/sweatfile.bats` (mirror the `disallow-main-worktree` style if there's a precedent):

```bats
@test "sc check runs pre-merge hook" {
  setup_test_repo
  cd "$wt_path"
  cat >sweatfile <<EOF
[hooks]
pre-merge = "echo CHECK_RAN"
EOF

  run sc check
  assert_success
  assert_output --partial "CHECK_RAN"
}

@test "sc check fails when pre-merge hook fails" {
  setup_test_repo
  cd "$wt_path"
  cat >sweatfile <<EOF
[hooks]
pre-merge = "false"
EOF

  run sc check
  assert_failure
}

@test "sc check is allowed when disable-merge is set" {
  setup_test_repo
  cd "$wt_path"
  cat >sweatfile <<EOF
[hooks]
disable-merge = true
pre-merge = "echo CHECK_OK"
EOF

  run sc check
  assert_success
  assert_output --partial "CHECK_OK"
}
```

> **NOTE:** `setup_test_repo` is a placeholder — copy whatever helper the existing bats tests use. Skip this step entirely if bats tests aren't trivial to extend; the Go-level tests in Task 3 already cover correctness. Bats coverage is for the CLI wiring.

**Step 2: Run bats to verify failure**

Run: `bats zz-tests_bats/sweatfile.bats --filter 'sc check'`
Expected: FAIL — `sc check` is not a registered subcommand.

**Step 3: Register the command**

In `cmd/spinclass/commands_session.go`, after the `merge` command's `app.AddCommand` block, add:

```go
	app.AddCommand(&command.Command{
		Name: "check",
		Description: command.Description{
			Short: "Run the [hooks].pre-merge command without merging",
			Long:  "Runs the configured [hooks].pre-merge command (the agent-CI hook) in the current worktree. Reports ok / not ok and exits non-zero on failure. Available regardless of [hooks].disable-merge.",
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p struct {
				globalArgs
			}
			_ = json.Unmarshal(args, &p)

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return check.Run(os.Stdout, p.FormatOrDefault(), cwd, p.Verbose)
		},
	})
```

Add the import: `"github.com/amarbel-llc/spinclass/internal/check"`.

**Step 4: Build and run bats**

Run: `just build`
Expected: clean build.

Run: `bats zz-tests_bats/sweatfile.bats --filter 'sc check'`
Expected: PASS for all 3 cases.

**Step 5: Commit**

```bash
git add cmd/spinclass/commands_session.go zz-tests_bats/sweatfile.bats
git commit -m "feat(cli): add sc check to run [hooks].pre-merge independently

:clown: Designed-with: Clown <https://github.com/amarbel-llc/clown>"
```

---

## Task 5: Add `check-this-session` MCP tool

**Promotion criteria:** N/A — additive.

**Files:**
- Modify: `cmd/spinclass/commands_mcp_only.go` (add registration alongside `merge-this-session`)
- Test: `cmd/spinclass/serve_integration_test.go`

**Step 1: Read existing serve integration test**

Read `cmd/spinclass/serve_integration_test.go` end-to-end. The test spawns `spinclass serve`, drives JSON-RPC over stdio, and inspects responses. Mirror its structure exactly.

**Step 2: Write a failing integration test**

Append to `cmd/spinclass/serve_integration_test.go`:

```go
// TestServeCheckThisSession exercises the check-this-session MCP tool
// over the stdio transport. The worktree's sweatfile defines a
// pre-merge hook; the tool MUST run it and return a successful result.
func TestServeCheckThisSession(t *testing.T) {
	// Reuse whatever harness TestServeMergeThisSessionStdioIntegrity uses.
	// The sweatfile content should be:
	//   [hooks]
	//   pre-merge = "echo CHECK_OUTPUT"
	//
	// Drive: tools/call check-this-session with {} args.
	// Assert: response is success and includes "CHECK_OUTPUT" somewhere
	// in the textual content.
}
```

> Implementer: flesh this out by copying the merge integration test, swapping `merge-this-session` for `check-this-session`, and replacing the post-merge state assertions with hook-output assertions.

**Step 3: Run test to verify failure**

Run: `go test ./cmd/spinclass/ -run TestServeCheckThisSession -v`
Expected: FAIL — tool not registered.

**Step 4: Register the MCP tool**

In `cmd/spinclass/commands_mcp_only.go`, inside `registerMCPOnlyCommands`, after the existing `merge-this-session` block (and before `update-this-session-description`), add:

```go
	app.AddCommand(&command.Command{
		Name:  "check-this-session",
		Title: "Check This Session",
		Description: command.Description{
			Short: "Run the configured [hooks].pre-merge command in the current worktree without merging. This is the agent-CI surface; safe to call repeatedly. Returns non-zero / error if the hook fails.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(false),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(false),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{},
		Run:    wrapMCPHandler("check-this-session", handleCheckThisSession),
	})
```

Add the handler at the bottom of the file (mirror `handleMergeThisSession`):

```go
func handleCheckThisSession(_ context.Context, _ json.RawMessage, _ command.Prompter) (*command.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return command.TextErrorResult(fmt.Sprintf("could not get working directory: %v", err)), nil
	}

	var buf bytes.Buffer
	if err := check.Run(&buf, "tap", cwd, false); err != nil {
		// Hook failed — surface output AND error to the agent.
		return command.TextErrorResult(buf.String() + "\n" + err.Error()), nil
	}
	return command.TextResult(buf.String()), nil
}
```

Add imports: `"bytes"`, `"github.com/amarbel-llc/spinclass/internal/check"`.

**Step 5: Build and run integration test**

Run: `just build`
Run: `go test ./cmd/spinclass/ -run TestServeCheckThisSession -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add cmd/spinclass/commands_mcp_only.go cmd/spinclass/serve_integration_test.go
git commit -m "feat(mcp): add check-this-session tool

Always registered; runs [hooks].pre-merge in the current worktree.
Pairs with sc check.

:clown: Designed-with: Clown <https://github.com/amarbel-llc/clown>"
```

---

## Task 6: Conditionally hide `merge-this-session` when `disable-merge=true`

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/spinclass/commands_mcp_only.go` (gate the `merge-this-session` registration)
- Test: `cmd/spinclass/serve_integration_test.go`

**Step 1: Verify the cwd assumption**

Before coding: confirm where `serve`'s cwd is when launched by Claude Code from `.mcp.json`. Read `internal/claude/mcp.go` and how `WriteMCPConfig` is invoked — the `.mcp.json` lives in the worktree, so Claude launches the server with cwd=worktree. If that's true, `os.Getwd()` at registration time returns the worktree path and `LoadWorktreeHierarchy` finds the sweatfile.

If the cwd assumption is **false** (you can confirm this by adding a temporary log line and reading `~/.local/state/spinclass/logs/serve-*.log`), abandon conditional-hiding and instead make `merge-this-session`'s handler check the flag and error. Update this task and Task 7 accordingly, and tell the user.

**Step 2: Write a failing integration test**

Append to `cmd/spinclass/serve_integration_test.go`:

```go
// TestServeMergeThisSessionHiddenWhenDisabled spawns serve in a
// worktree whose sweatfile sets [hooks].disable-merge = true, then
// queries tools/list and asserts merge-this-session is absent while
// check-this-session is present.
func TestServeMergeThisSessionHiddenWhenDisabled(t *testing.T) {
	// Setup: worktree with sweatfile containing:
	//   [hooks]
	//   disable-merge = true
	// Drive: tools/list
	// Assert: no tool with name "merge-this-session"
	// Assert: a tool with name "check-this-session" IS present
	//   (regression guard against accidentally gating both)
}
```

**Step 3: Run test to verify failure**

Run: `go test ./cmd/spinclass/ -run TestServeMergeThisSessionHiddenWhenDisabled -v`
Expected: FAIL — tool is currently always registered.

**Step 4: Implement conditional registration**

In `cmd/spinclass/commands_mcp_only.go`, change `registerMCPOnlyCommands` to:

```go
func registerMCPOnlyCommands(app *command.App) {
	if !mergeDisabledForCwd() {
		app.AddCommand(&command.Command{
			Name:  "merge-this-session",
			// ... existing definition unchanged
		})
	}

	app.AddCommand(&command.Command{
		Name: "check-this-session",
		// ... unchanged from Task 5
	})

	app.AddCommand(&command.Command{
		Name: "update-this-session-description",
		// ... unchanged
	})
}

// mergeDisabledForCwd reads the sweatfile hierarchy from the current
// working directory and reports whether [hooks].disable-merge is set.
// Returns false on any error so a misconfigured environment doesn't
// silently strip the merge tool.
func mergeDisabledForCwd() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		// Not a worktree: load the simple hierarchy.
		h, hErr := sweatfile.LoadHierarchy(home, cwd)
		if hErr != nil {
			return false
		}
		return h.Merged.DisableMergeEnabled()
	}
	h, err := sweatfile.LoadWorktreeHierarchy(home, repoPath, cwd)
	if err != nil {
		return false
	}
	return h.Merged.DisableMergeEnabled()
}
```

Add imports for `sweatfile` and `git` if not already present.

**Step 5: Run tests**

Run: `go test ./cmd/spinclass/ -run 'TestServeMergeThisSession|TestServeCheckThisSession' -v`
Expected: all PASS — the existing integration test keeps merge present (no `disable-merge` in its sweatfile); the new test sees it hidden.

**Step 6: Run the full suite**

Run: `just test`
Expected: all PASS.

**Step 7: Commit**

```bash
git add cmd/spinclass/commands_mcp_only.go cmd/spinclass/serve_integration_test.go
git commit -m "feat(mcp): hide merge-this-session when [hooks].disable-merge is set

:clown: Designed-with: Clown <https://github.com/amarbel-llc/clown>"
```

---

## Task 7: Documentation

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/spinclass/doc/spinclass-sweatfile.5`
- Modify: `CLAUDE.md` (project)

**Step 1: Update the manpage**

Read `cmd/spinclass/doc/spinclass-sweatfile.5` to find the `disallow-main-worktree` documentation. Add a parallel `disable-merge` entry directly after it. Use the same .TP / .B / .RS troff style as siblings.

Suggested copy:

```troff
.TP
.B disable-merge
Boolean. When true, \fBsc merge\fR exits non-zero before any git
operation runs, and the \fBmerge-this-session\fR MCP tool is omitted
from the tool catalog. \fBsc check\fR and \fBcheck-this-session\fR
remain available so the configured \fB[hooks].pre-merge\fR command can
still be invoked as agent CI. Use this in repositories where merges
must go through external review (e.g., protected branches that
require pull requests). Default: false.
```

**Step 2: Update CLAUDE.md (project)**

In `/Users/sfriedenberg/eng/repos/spinclass/.worktrees/bold-baobab/CLAUDE.md`:

1. In the "CLI Commands" table, add a row for `sc check`:
   ```
   `sc check`                       Run [hooks].pre-merge in the current worktree without merging
   ```
2. After the table (or near the merge-related discussion), add a paragraph noting that `merge-this-session` is conditionally registered and that agents in repos with `disable-merge=true` should fall back to `check-this-session`.

Do NOT modify the user's `~/.claude/CLAUDE.md` — that's per-user.

**Step 3: Verify manpage renders**

Run: `man -l cmd/spinclass/doc/spinclass-sweatfile.5 | grep -A 6 disable-merge`
Expected: the new entry appears with proper formatting.

**Step 4: Commit**

```bash
git add cmd/spinclass/doc/spinclass-sweatfile.5 CLAUDE.md
git commit -m "docs: document [hooks].disable-merge and sc check

:clown: Designed-with: Clown <https://github.com/amarbel-llc/clown>"
```

---

## Task 8: Final verification

**Step 1: Full test suite**

Run: `just test`
Expected: all PASS.

**Step 2: Lint**

Run: `just lint`
Expected: no findings.

**Step 3: Format**

Run: `just fmt`
Expected: no changes (everything already formatted).

**Step 4: Build**

Run: `just build`
Expected: clean nix build.

**Step 5: Manual smoke test**

In this worktree, add to `sweatfile`:
```toml
[hooks]
disable-merge = true
pre-merge = "echo SMOKE_OK"
```

Run: `./result/bin/sc check`
Expected: TAP output containing `SMOKE_OK`.

Run: `./result/bin/sc merge`
Expected: TAP fail message naming `disable-merge=true at <path>` and hinting at `sc check`. Exit non-zero.

Revert the sweatfile change before merging.

**Step 6: Merge the session**

Use `mcp__spinclass__merge-this-session` (this worktree does NOT have `disable-merge=true`).

If the session's own sweatfile *did* have it set during smoke testing, ensure it's reverted first — otherwise the merge tool is hidden from the very session about to merge.

---

## Notes for the implementer

- The biggest unknown is Task 6 step 1: the cwd of `spinclass serve` at process start. Verify before coding.
- The `tommy generate` step in Task 1 is the second biggest unknown. If `tommy` isn't reachable, the implementer must hand-edit `sweatfile_tommy.go`. That file has 4 sites per `*bool` field — copy each one carefully.
- Each task ends with a commit. If pivy-agent is locked, stop and ask the user.
- Don't rename `[hooks].pre-merge` to `[hooks].check`. Out of scope.
- Don't try to make `disable-merge` per-branch or per-environment. Out of scope.
