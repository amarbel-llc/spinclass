# Merge/Check ndjson-crap + Viewport Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** `sc merge`/`sc check` natively emit ndjson-crap and present a live viewport (spinner + rolling hook-output tail + verdict lines) on a TTY, replacing TAP for these two commands.

**Architecture:** `internal/merge` and `internal/check` drop `*tap.Writer` for `*crap.Reporter` (crap/go-crap/v2). Merge stages are result-family test points (preserving TAP's verdict+diagnostic information model — `presentPlain` echoes failure diagnostics for Test records); the pre-merge hook stage is *additionally* wrapped in an execution-family Phase whose `Output` records carry the hook's live lines (the viewport's rolling tail). Mixed-family streams are explicitly legal in ndjson-crap. Presentation is consumer selection in a new `internal/present` package: `auto` (default) → viewport on TTY / raw ndjson piped; `viewport`/`plain`/`ndjson` force. MCP handlers buffer records and render the plain (verdict-per-line) text for agents; madder BlobLinks are untouched (they travel as Go return values).

**Tech Stack:** `github.com/amarbel-llc/crap/go-crap/v2` (`crap` Reporter, `ndjsoncrap`, `viewport`), `github.com/mattn/go-isatty` (already a dep), bubbletea (already a dep via huh).

**Rollback:** `git revert` of this series. No dual period (approved design decision): change is presentation-only — git operations, session state, blob storage, attestation gate untouched. `--format plain` is the boring-terminal escape.

**Design doc:** `docs/plans/2026-06-10-merge-ndjson-crap-viewport-design.md` (approved).

**Reference reading for any task:** crap's producer API `crap/go-crap/go.mod`-module: package `crap` (`Reporter`, `TestStream` Ok/NotOk/Skip/Finish, `Phase` Command/Output/Done/Fail), package `viewport` (`Present(in, Options{Title,TailLines,Out,IsTTY})`, `presentPlain` semantics), package `ndjsoncrap` (record structs). Read with `hamster doc github.com/amarbel-llc/crap/go-crap/v2/<pkg>` or `hamster mod-read`.

**Build/test commands:** scoped Go tests via the hamster MCP tool (`go-test` with `packages`/`run`); compile checks via `hamster go-build`. Do NOT run full `just`/`just test` mid-series — the merge hook is the CI lane.

---

### Task 1: `internal/present` — format resolution, reporter harness, plain renderer

**Files:**
- Create: `internal/present/present.go`
- Create: `internal/present/present_test.go`

**Step 1: Write the failing tests**

```go
package present

import (
	"strings"
	"testing"

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
)

func TestResolveFormat(t *testing.T) {
	cases := []struct {
		in    string
		isTTY bool
		want  string
		err   bool
	}{
		{"", true, FormatViewport, false},
		{"", false, FormatNdjson, false},
		{"auto", true, FormatViewport, false},
		{"auto", false, FormatNdjson, false},
		{"viewport", false, FormatViewport, false},
		{"plain", true, FormatPlain, false},
		{"ndjson", true, FormatNdjson, false},
		{"tap", true, "", true},   // retired for merge/check
		{"table", true, "", true}, // never valid here
	}
	for _, c := range cases {
		got, err := ResolveFormat(c.in, c.isTTY)
		if c.err && err == nil {
			t.Errorf("ResolveFormat(%q): want error", c.in)
		}
		if !c.err && (err != nil || got != c.want) {
			t.Errorf("ResolveFormat(%q,%v) = %q,%v want %q", c.in, c.isTTY, got, err, c.want)
		}
	}
}

// WithReporter(FormatNdjson) writes records to out verbatim.
func TestWithReporterNdjson(t *testing.T) {
	var out strings.Builder
	err := WithReporter(FormatNdjson, "title", &out, &out, func(rep *crap.Reporter) error {
		ts := rep.TestStream(1)
		ts.Ok("rebase feature")
		ts.Finish()
		return nil
	})
	if err != nil {
		t.Fatalf("WithReporter: %v", err)
	}
	for _, want := range []string{`"type":"test"`, "rebase feature", `"type":"summary"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("ndjson output missing %q:\n%s", want, out.String())
		}
	}
}

// WithReporter(FormatPlain) renders verdict lines, echoing failure diagnostics.
func TestWithReporterPlain(t *testing.T) {
	var out strings.Builder
	_ = WithReporter(FormatPlain, "title", &out, &out, func(rep *crap.Reporter) error {
		ts := rep.TestStream(2)
		ts.Ok("rebase feature")
		ts.NotOk("merge feature", map[string]any{"message": "boom"})
		ts.Finish()
		return nil
	})
	if !strings.Contains(out.String(), "✓ rebase feature") {
		t.Errorf("plain output missing ok verdict:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "✗ merge feature") || !strings.Contains(out.String(), "boom") {
		t.Errorf("plain output missing failure verdict/diagnostic:\n%s", out.String())
	}
}

// RenderPlain converts a buffered record stream to verdict lines.
func TestRenderPlain(t *testing.T) {
	var rec strings.Builder
	rep := crap.NewReporter(&rec, crap.ReporterOptions{})
	ts := rep.TestStream(1)
	ts.NotOk("pre-merge hook", map[string]any{"message": "exit 1"})
	ts.Finish()

	text := RenderPlain(strings.NewReader(rec.String()))
	if !strings.Contains(text, "✗ pre-merge hook") || !strings.Contains(text, "exit 1") {
		t.Errorf("RenderPlain missing failure: %q", text)
	}
}

// LineWriter splits arbitrary chunks into per-line Phase.Output records.
func TestLineWriter(t *testing.T) {
	var rec strings.Builder
	rep := crap.NewReporter(&rec, crap.ReporterOptions{})
	ph := rep.Phase("hook")
	lw := NewLineWriter(ph)
	_, _ = lw.Write([]byte("line one\npartial"))
	_, _ = lw.Write([]byte(" line\n"))
	lw.Flush()
	ph.Done()

	if !strings.Contains(rec.String(), "line one") || !strings.Contains(rec.String(), "partial line") {
		t.Errorf("LineWriter output records missing lines:\n%s", rec.String())
	}
}
```

**Step 2: Run tests, verify they fail to compile**

`hamster go-test packages=./internal/present` — expected: build failure (package does not exist).

**Step 3: Implement `internal/present/present.go`**

```go
// Package present selects and drives the rendering of merge/check
// ndjson-crap streams: a live bubbletea viewport on a TTY, plain
// verdict-per-line text, or the raw records (the wire). The producer side
// is crap.Reporter; this package owns only consumer wiring.
package present

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
	"github.com/amarbel-llc/crap/go-crap/v2/ndjsoncrap"
	"github.com/amarbel-llc/crap/go-crap/v2/viewport"
)

// Resolved format names. "auto" and "" resolve to viewport on a TTY and
// ndjson otherwise (madder sync's convention).
const (
	FormatViewport = "viewport"
	FormatPlain    = "plain"
	FormatNdjson   = "ndjson"
)

// ResolveFormat maps the merge/check --format value to a concrete renderer.
// TAP is retired for these commands; naming it gets a pointed error.
func ResolveFormat(format string, stdoutIsTTY bool) (string, error) {
	switch format {
	case "", "auto":
		if stdoutIsTTY {
			return FormatViewport, nil
		}
		return FormatNdjson, nil
	case FormatViewport, FormatPlain, FormatNdjson:
		return format, nil
	case "tap", "table":
		return "", fmt.Errorf("format %q is not supported for merge/check (TAP was retired here); use auto, viewport, plain, or ndjson", format)
	default:
		return "", fmt.Errorf("unknown format %q (valid: auto, viewport, plain, ndjson)", format)
	}
}

// WithReporter builds a crap.Reporter wired to the resolved renderer, runs
// fn with it, and tears the renderer down. stdout receives ndjson/plain
// output; tty receives the live viewport (callers pass os.Stderr so
// `sc merge > records.ndjson` keeps a live viewport). fn's error is
// returned; a renderer error is joined only if fn succeeded.
func WithReporter(format, title string, stdout, tty io.Writer, fn func(rep *crap.Reporter) error) error {
	opts := crap.ReporterOptions{Title: title, Source: "spinclass"}

	if format == FormatNdjson {
		rep := crap.NewReporter(stdout, opts)
		return fn(rep)
	}

	pr, pw := io.Pipe()
	rep := crap.NewReporter(pw, opts)
	done := make(chan error, 1)
	out := stdout
	isTTY := false
	if format == FormatViewport {
		out = tty
		isTTY = true
	}
	go func() {
		done <- viewport.Present(pr, viewport.Options{Title: title, Out: out, IsTTY: isTTY})
	}()
	err := fn(rep)
	_ = pw.Close()
	renderErr := <-done
	if err != nil {
		return err
	}
	return renderErr
}

// RenderPlain renders a buffered ndjson-crap stream as plain verdict lines
// — the agent-facing text for MCP results and async job ResultText.
func RenderPlain(records io.Reader) string {
	var b strings.Builder
	_ = viewport.Present(records, viewport.Options{Out: &b, IsTTY: false})
	return strings.TrimRight(b.String(), "\n")
}

// phaseOutput is the subset of crap.Phase LineWriter needs (test seam).
type phaseOutput interface {
	Output(stream, data string)
}

// LineWriter adapts an io.Writer (the hook's live output sink) into
// per-line Phase.Output records. Partial lines are buffered until their
// newline arrives; Flush emits any trailing partial line.
type LineWriter struct {
	ph  phaseOutput
	buf bytes.Buffer
}

func NewLineWriter(ph phaseOutput) *LineWriter { return &LineWriter{ph: ph} }

func (l *LineWriter) Write(p []byte) (int, error) {
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			// no full line yet; keep the partial
			l.buf.Reset()
			l.buf.WriteString(line)
			break
		}
		l.ph.Output(ndjsoncrap.StreamStdout, line)
	}
	return len(p), nil
}

// Flush emits any buffered partial line as a final output record.
func (l *LineWriter) Flush() {
	if l.buf.Len() > 0 {
		l.ph.Output(ndjsoncrap.StreamStdout, l.buf.String()+"\n")
		l.buf.Reset()
	}
}
```

NOTE for implementer: verify exact names `crap.ReporterOptions`, `viewport.Options`, `ndjsoncrap.StreamStdout` against the module (`hamster doc github.com/amarbel-llc/crap/go-crap/v2/crap`). Adjust if the API differs — the tests are the contract.

**Step 4: Run tests, verify pass**

`hamster go-test packages=./internal/present verbose=true` — expected: all PASS. The plain-rendering assertions (`✓ `/`✗ ` prefixes) pin crap's presentPlain glyphs; if they differ, update the test to the actual glyphs and note the actual prefix — Task 6 (firstFailureLine) depends on it.

**Step 5: Commit**

`git add internal/present && git commit -m "feat(present): ndjson-crap renderer selection for merge/check"`

---

### Task 2: `internal/check` — emit via crap.Reporter

**Files:**
- Modify: `internal/check/check.go` (Run/RunContext/RunWithWriterContext signatures; runHookCompactContext; delete the OutputBlock fallback path)
- Modify: `internal/check/check_test.go`, `internal/check/buildfailure_test.go` (call sites + assertions)

**Step 1: New signatures (breaking, package-internal callers updated in Tasks 3–6):**

```go
func Run(rep *crap.Reporter, wtPath string) ([]BlobLink, error)
func RunContext(ctx context.Context, rep *crap.Reporter, wtPath string, activity io.Writer) ([]BlobLink, error)
func RunWithReporterContext(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, hierarchy sweatfile.Hierarchy, wtPath, branch, hookSha string, activity io.Writer) ([]BlobLink, error)
```

Drop: `w io.Writer` (legacy passthrough), `format string` (presentation is the caller's concern now), `verbose bool` (reserved-unused), the `tw == nil` passthrough branch, and the non-madder `OutputBlock` branch. `rep` is never nil. `ts` is the caller's result-family stream when check runs inside a merge (one shared stream numbers all stages); `Run`/`RunContext` create their own `TestStream(1)` + `Finish()`.

**Step 2: Rework `runHookCompactContext` → `runHookPhase`**

Keep ALL existing logic for: madder blob storage, tap-ndjson/ndjson-crap buffering + failure summaries, the ring tail, elapsed/exit-code capture, the inactivity watchdog (untouched — it lives in sweatfile.RunPreMergeHookContext). Change only the emission:

- Open `ph := rep.Phase(desc)`; `ph.Command(cmd)`.
- Wrap the existing `sink` with `present.NewLineWriter(ph)` in the MultiWriter chain (live Output records), `Flush()` after the hook exits.
- madder blob id, when present, also emits `ph.Output(ndjsoncrap.StreamStdout, "resource_link: "+link.URI+"\n")` so the wire and viewport carry it (BlobLink return values stay the MCP path).
- On success: `ph.Done()`; `ts.Ok(desc)`.
- On failure: failure summary (`buildFailureSummary`/`buildFailureSummaryCrap`) or ring tail becomes the `output` entry of the diagnostic; `ph.Fail(hookErr)`; `ts.NotOk(desc, map[string]any{"severity": "fail", "message": hookErr.Error(), "command": cmd, "format": format, "exit_code": exitCode, "elapsed": elapsed.String(), "output": failureText})`.

The hook output *format* (`[hooks].pre-merge-output-format`) is still read from the hierarchy inside this function — it governs parsing/storage, not presentation.

**Step 3: Update check tests**

`check_test.go`'s shape tests currently assert TAP text (`"not ok"`, `"format: tap-ndjson"`, `"failure:"`, `"tail:"`). Rewrite them to drive `Run` with a Reporter over a `bytes.Buffer` and assert on the records: decode with `ndjsoncrap.NewReader`, assert there is a failing `Test` record whose `Diagnostic["format"] == "tap-ndjson"` and whose `Diagnostic["output"]` contains the expected failure summary; assert the blob-link round-trip unchanged. The `TestRunHookCompactShape_NdjsonCrap*` pair (from PR #122) migrates the same way.

**Step 4: Compile + run**

`hamster go-build packages=./internal/check` then `hamster go-test packages=./internal/check verbose=true`. internal/merge and cmd/spinclass will NOT compile yet — that is expected; do not run ./... builds in this task.

**Step 5: Commit**

`git commit -m "refactor(check): emit ndjson-crap via crap.Reporter, drop TAP"`

---

### Task 3: `internal/merge` — stages as test points, drop passthrough

**Files:**
- Modify: `internal/merge/merge.go`
- Modify: any `internal/merge/*_test.go` call sites

**Step 1: New signatures:**

```go
func Run(execr executor.Executor, format string, target string, gitSync bool) error
func Resolved(execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync, inSession bool) ([]check.BlobLink, error)
func ResolvedContext(ctx context.Context, execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync, inSession bool, activity io.Writer) ([]check.BlobLink, error)
func PrepareMerge(ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch string, gitSync bool) (pinnedSha string, err error)
func FinishMerge(ctx context.Context, execr executor.Executor, rep *crap.Reporter, ts *crap.TestStream, repoPath, wtPath, branch, defaultBranch, pinnedSha string, gitSync, inSession bool, activity io.Writer) ([]check.BlobLink, error)
func MergeImplicit(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, repoPath, checkout, branch string, activity io.Writer) ([]check.BlobLink, error)
```

Conversion recipe, applied stage by stage (pull/rebase/merge/remove-worktree/delete-branch/push):

- `tw.Ok(label)` and `tw.OkDiag(label, …)` → `ts.Ok(label)` (success output drops from the stream; it remains in git. Verbose-only ok-output was the only consumer.)
- `failStep(tw, label, err, out)` → keep `failStep` but retarget: `func failStep(ts *crap.TestStream, label string, err error, output string) error` emitting `ts.NotOk(label, diag)` with the same `{severity, message, output}` map (now `map[string]any`).
- Delete every `tw == nil` / `log.Info` / `RunPassthrough` passthrough branch — `ts` is never nil. Delete the `verbose` parameter (always emit the diag output on failure).
- Delete `NewMergeWriter` (the directive comment moves to the MCP layer if still wanted — buildHookResult already explains resource_links; just delete it).
- `runPreMergeHookContext` forwards to `check.RunWithReporterContext(ctx, rep, ts, hierarchy, …)`; its `tw == nil` branch is deleted.
- `merge.Run`: resolve format via `present.ResolveFormat(format, isatty.IsTerminal(os.Stdout.Fd()))`, then wrap the whole resolved merge in `present.WithReporter(resolved, "merge "+branch, os.Stdout, os.Stderr, func(rep *crap.Reporter) error { ts := rep.TestStream(0); defer ts.Finish(); … })`. Same for the MergeImplicit branch at the top of Run.
- huh prompts (`chooseWorktree`, `promptDefaultBranch`, `ResolveDefaultBranch`) must run BEFORE `WithReporter` (no TUI nesting) — they already happen before `Resolved` in `Run`; keep that ordering.

**Step 2: Compile + run package tests**

`hamster go-build packages=./internal/merge` then `hamster go-test packages=./internal/merge`. Update test call sites mechanically (Reporter over a buffer; assert on decoded records where tests asserted TAP text).

**Step 3: Commit**

`git commit -m "refactor(merge): stages as ndjson-crap test points, drop TAP/passthrough"`

---

### Task 4: CLI wiring — `sc merge`, `sc check`

**Files:**
- Modify: `cmd/spinclass/commands_session.go:62-106` (merge + check commands)

**Step 1:** merge command: pass `p.Format` (raw, NOT `FormatOrDefault()` — "" means auto now) to `merge.Run(executor.ShellExecutor{}, p.Format, p.Target, p.GitSync)`. Update the command's Description to name the new formats.

check command: replace the `check.Run(os.Stdout, p.FormatOrDefault(), cwd, p.Verbose)` body with:

```go
resolved, rerr := present.ResolveFormat(p.Format, isatty.IsTerminal(os.Stdout.Fd()))
if rerr != nil {
	return rerr
}
return present.WithReporter(resolved, "check", os.Stdout, os.Stderr, func(rep *crap.Reporter) error {
	_, err := check.Run(rep, cwd)
	return err
})
```

**Step 2:** `hamster go-build packages=./cmd/spinclass` — expect remaining MCP-handler compile errors; fix ONLY the two CLI commands here, leave handlers for Task 5 if the package still fails to build, do Tasks 4+5 in one commit instead (note it in the commit message).

**Step 3: Commit** `git commit -m "feat(cli): merge/check formats auto|viewport|plain|ndjson"`

---

### Task 5: MCP sync handlers

**Files:**
- Modify: `cmd/spinclass/commands_mcp_only.go` — `handleMergeThisSession` (~line 300-350), `handleCheckThisSession` (~line 355-377)

**Step 1:** Pattern for both (and the implicit branch):

```go
var buf bytes.Buffer
rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "merge " + gs.branch, Source: "spinclass"})
ts := rep.TestStream(0)
blobLinks, mergeErr := merge.Resolved(executor.ShellExecutor{}, rep, ts, gs.repoPath, cwd, gs.branch, defaultBranch, params.GitSync, true)
ts.Finish()
text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
return buildHookResult(text, blobLinks, mergeErr), nil
```

`handleCheckThisSession` mirrors with `check.Run(rep, cwd)`; keep the existing `hookErr != nil && text == ""` fallback. `buildHookResult` itself is unchanged (text + resource_link blocks).

**Step 2:** `hamster go-build packages=./cmd/spinclass`, then `hamster go-test packages=./cmd/spinclass run=TestResolveGatedSession` (sanity).

**Step 3: Commit** `git commit -m "refactor(mcp): merge/check results render plain ndjson-crap verdicts"`

---

### Task 6: Async handlers + job runner + wake message

**Files:**
- Modify: `cmd/spinclass/commands_mcp_only.go` — `handleMergeThisSessionAsync` (~line 379-450), `handleCheckThisSessionAsync` (~line 452-478)
- Modify: `internal/job/runner.go:143-151` (`firstFailureLine`)
- Modify: `internal/job/wake_test.go:122-139`
- Modify: `cmd/spinclass/serve_integration_test.go` (TAP-text assertions on merge/check tool results, if any — grep `"not ok"` / `"ok 1"`)

**Step 1:** Async merge: the shared `buf`+`rep`+`ts` replace the shared `tw`+`buf`. `PrepareMerge(ts, …)` runs synchronously; on prepErr render plain and return. The job closure runs `FinishMerge(ctx, …, rep, ts, …)`, then `ts.Finish()`, then returns `present.RenderPlain(bytes.NewReader(buf.Bytes())), mergeErr != nil`. Async check mirrors. The job's `w io.Writer` (job.log activity) is unchanged — it still receives raw hook output.

**Step 2:** `firstFailureLine`: match the plain rendering's failure prefix (`✗ ` — confirm against Task 1's pinned glyph) instead of `"not ok"`. Update its doc comment and `wake_test.go`'s fixture/assertion (`"check failed: ✗ pre-merge hook"`).

**Step 3:** `hamster go-test packages=./internal/job ./cmd/spinclass` — expected PASS. Then `hamster go-build` (whole module must compile now) and `hamster go-vet`.

**Step 4: Commit** `git commit -m "refactor(async): job results render plain verdicts; wake matches ✗ lines"`

---

### Task 7: bats migration

**Files:**
- Modify: `zz-tests_bats/hooks.bats` (lines ~125-170: `format:`/`failure:`/`tail:` YAML assertions)
- Modify: `zz-tests_bats/implicit_sessions.bats`, `zz-tests_bats/session.bats`, `zz-tests_bats/lifecycle.bats`, `zz-tests_bats/sweatfile.bats` — grep each for `not ok`, `ok 1`, `--partial "ok`, TAP-shape assertions on `sc merge`/`sc check` output ONLY (other commands still speak TAP — leave them).

**Step 1:** bats runs are piped (no TTY) so merge/check default to **ndjson**. Rewrite assertions against the wire, e.g.:

- success: `assert_output --partial '"description":"pre-merge hook'` + `'"ok":true'`
- failure: `'"ok":false'` + the diagnostic: `assert_output --partial '"format":"tap-ndjson"'`, `--partial 'expected 7 got 9'`
- where a test wants readability, run `sc check --format plain` and assert `✓ `/`✗ ` lines instead.

**Step 2:** Run the bats lane: `nix build .#bats-default -L` (or `just test-bats`). Expected: PASS. This is the slow step — run once after all edits.

**Step 3: Commit** `git commit -m "test(bats): merge/check assertions onto ndjson-crap wire"`

---

### Task 8: Documentation + FDR

**Files:**
- Modify: `CLAUDE.md` — "TAP-14 everywhere" pattern gets the merge/check carve-out (these emit ndjson-crap; formats auto|viewport|plain|ndjson); dependency list already names go-crap/v2 (extend: "+ merge/check stage emission and viewport presentation").
- Modify: `cmd/spinclass/commands_mcp_only.go` — `buildMergeThisSessionDescription` / `buildCheckThisSessionDescription` and the async tool descriptions: stop saying "TAP payload"; say "plain verdict lines (✓/✗) with resource_link blocks".
- Create: `docs/features/00XX-merge-ndjson-crap-viewport.md` via the **eng:fdr** skill (next free number) — capture format vocabulary, tuning levers from the design doc (TailLines, auto-piped default, MCP rendering), known limitation (success-stage output not in stream), and the named follow-up (splice ndjson-crap hook records as nested phases).

**Step 1:** Make the edits; `hamster go-test packages=./cmd/spinclass run=TestBuildMergeThisSessionDescription` (update those description tests).

**Step 2: Commit** `git commit -m "docs: merge/check ndjson-crap formats, FDR"`

---

### Task 9: Sweep + verify

**Step 1:** `rg "tap" internal/merge internal/check` — remaining imports must be only the tap-ndjson hook-format parsing in check (reader/ndjson aggregator), which stays. Delete dead imports/helpers (`NewMergeWriter`, the OutputBlock tapblock use in check if now unreferenced — check `internal/tapblock` consumers; if check was the only one, leave the package but note it in the FDR as candidate for removal).

**Step 2:** `hamster go-build` + `hamster go-vet` + `just lint-fmt`.

**Step 3:** Full verification happens in the merge gate (`merge-this-session` runs `just`) — do NOT pre-run it. Commit any sweep diffs: `git commit -m "chore: sweep dead TAP plumbing from merge/check"`.
