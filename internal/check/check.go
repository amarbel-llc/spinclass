// Package check runs the [hooks].pre-merge command in a worktree
// independently of `sc merge`. It is the agent-CI surface invoked by
// `sc check` and the `check-this-session` MCP tool.
package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/amarbel-llc/crap/go-crap/v2/ndjsoncrap"
	"github.com/amarbel-llc/spinclass/internal/embeds"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/madder"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
	"github.com/amarbel-llc/spinclass/internal/tapblock"
	"github.com/amarbel-llc/tap/go/pkgs/ndjson"
	"github.com/amarbel-llc/tap/go/pkgs/reader"
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
	"github.com/amarbel-llc/tap/go/pkgs/yaml_diagnostic"
)

// Directive emitted by the compact (madder-pinned) shape so agents
// reading the response know they don't need to fetch the
// resource_link unless the test point failed.
const compactDirective = "directive: if status is ok, the resource_link need not be followed; only inspect on failure"

// BuildWorktreePrefix is the filename prefix of a transient pre-merge build
// worktree under <repo>/.worktrees/: ".merge-<branch>-<sha>-<pid>". Exported so
// sc clean can recognize and prune orphaned ones (the <pid> is os.Getpid() of
// the creating process; see internal/clean.findOrphanBuildWorktrees and #135).
const BuildWorktreePrefix = ".merge-"

// BlobLink pairs a madder blob URI with the MIME type of its contents.
// Producers know the format used to write the blob (raw stdout vs.
// parsed ndjson) and surface it here so the MCP layer can set
// ResourceLinkContent.mimeType correctly.
type BlobLink struct {
	URI      string
	MimeType string
}

// mimeTypeForFormat maps the sweatfile [hooks].pre-merge-output-format
// enum to the IANA-ish MIME type advertised on the MCP
// ResourceLinkContent block. Anything other than "tap-ndjson" maps to
// "text/plain" (matching the format=raw default).
func mimeTypeForFormat(format string) string {
	if format == "tap-ndjson" || format == "ndjson-crap" {
		return "application/x-ndjson"
	}
	return "text/plain"
}

// Run resolves the worktree containing wtPath, loads the sweatfile
// hierarchy, and runs the configured [hooks].pre-merge command. It writes
// TAP-14 output (when format == "tap") or passthrough output otherwise to
// w. Returns the resource_link blobs emitted for the hook output (one per
// hook step that produced a madder blob; empty when madder is not pinned)
// and a non-nil error if the hook fails. Each BlobLink carries the MIME
// type matching the format the blob was written in (text/plain for raw,
// application/x-ndjson for tap-ndjson).
//
// If no pre-merge hook is configured, Run returns (nil, nil) and (in TAP
// mode) emits an "ok" indicating no hook is configured — agents and
// humans should treat "no hook" as a success because there is nothing to
// check.
//
// The verbose parameter is accepted for API stability but currently
// unused; check itself emits no git output today. It is reserved for
// future use when verbose-mode diagnostics become relevant.
func Run(w io.Writer, format, wtPath string, verbose bool) ([]BlobLink, error) {
	return RunContext(context.Background(), w, format, wtPath, verbose, nil)
}

// RunContext is Run bound to ctx, with an optional activity writer that
// receives the pre-merge hook's live output in addition to the normal
// madder/ring/TAP plumbing. The async job runner passes its job log as
// activity so session-job-status can tail live progress and derive a
// last-activity timestamp; synchronous callers use Run (background ctx, nil
// activity). ctx cancellation kills the hook subprocess.
func RunContext(ctx context.Context, w io.Writer, format, wtPath string, verbose bool, activity io.Writer) ([]BlobLink, error) {
	repoPath, err := git.CommonDir(wtPath)
	if err != nil {
		return nil, fmt.Errorf("not a worktree: %s", wtPath)
	}
	branch, err := git.BranchCurrent(wtPath)
	if err != nil {
		return nil, fmt.Errorf("could not determine current branch: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, errors.New("could not resolve home directory")
	}
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil {
		return nil, fmt.Errorf("load sweatfile hierarchy: %w", err)
	}

	var tw *tap.Writer
	ownWriter := false
	if format == "tap" {
		tw = tap.NewWriter(w)
		ownWriter = true
		if embeds.MadderBin() != "" {
			tw.Comment(compactDirective)
		}
	}

	cmd := hierarchy.Merged.PreMergeHookCommand()
	if cmd == nil || *cmd == "" {
		if tw != nil {
			tw.Ok("no pre-merge hook configured")
			if ownWriter {
				tw.Plan()
			}
		}
		return nil, nil
	}

	// Pin HEAD so the hook verifies the committed tree in an isolated build
	// worktree (see resolveHookDir). RevParse failure degrades to "" → in-place.
	hookSha, _ := git.RevParse(wtPath, "HEAD")
	// ownWriter=false: Run owns Plan emission for the standalone path.
	links, hookErr := RunWithWriterContext(ctx, tw, w, hierarchy, wtPath, branch, hookSha, false, activity)
	if tw != nil && ownWriter {
		tw.Plan()
	}
	return links, hookErr
}

// RunWithWriter runs the configured pre-merge hook against an already-
// loaded hierarchy and a caller-supplied tap.Writer. Pass tw=nil for
// passthrough mode. ownWriter controls whether RunWithWriter calls
// tw.Plan() when the hook fails (matching the legacy merge call pattern;
// successful hook runs leave Plan to the caller).
//
// Returns the resource_link blobs emitted for hook output (compact path
// only; empty otherwise) and a non-nil error if the hook fails. Each
// BlobLink carries the MIME type matching the format the blob was
// written in.
//
// When no pre-merge hook is configured, RunWithWriter returns (nil, nil)
// and emits NO TAP output. This preserves the historical
// merge.runPreMergeHook behavior; standalone callers that want a "no
// hook" report should use Run instead.
func RunWithWriter(
	tw *tap.Writer,
	w io.Writer,
	hierarchy sweatfile.Hierarchy,
	wtPath, branch, hookSha string,
	ownWriter bool,
) ([]BlobLink, error) {
	return RunWithWriterContext(context.Background(), tw, w, hierarchy, wtPath, branch, hookSha, ownWriter, nil)
}

// RunWithWriterContext is RunWithWriter bound to ctx with an optional activity
// writer (see RunContext). ctx threads to the hook subprocess (cancellable);
// activity, when non-nil, is teed the hook's live output alongside the normal
// madder/ring/TAP destination.
func RunWithWriterContext(
	ctx context.Context,
	tw *tap.Writer,
	w io.Writer,
	hierarchy sweatfile.Hierarchy,
	wtPath, branch, hookSha string,
	ownWriter bool,
	activity io.Writer,
) ([]BlobLink, error) {
	cmd := hierarchy.Merged.PreMergeHookCommand()
	if cmd == nil || *cmd == "" {
		return nil, nil
	}

	// Run the hook in an isolated detached worktree pinned to hookSha (default)
	// or in place in wtPath (legacy / disabled / no sha). madder still targets
	// wtPath — only the hook's working directory relocates.
	hookDir, cleanup, prepErr := resolveHookDir(hierarchy, wtPath, branch, hookSha)
	if prepErr != nil {
		if tw != nil {
			tw.NotOk("pre-merge build worktree for "+branch, map[string]string{
				"severity": "fail",
				"message":  prepErr.Error(),
			})
			if ownWriter {
				tw.Plan()
			}
		}
		return nil, prepErr
	}
	defer cleanup()

	if tw == nil {
		return nil, hierarchy.Merged.RunPreMergeHookContext(ctx, hookDir, teeWriter(w, activity))
	}

	desc := "pre-merge hook for " + branch + ": `" + *cmd + "`"

	if embeds.MadderBin() != "" {
		link, hookErr := runHookCompactContext(ctx, tw, hierarchy, wtPath, hookDir, *cmd, desc, activity)
		if hookErr != nil && ownWriter {
			tw.Plan()
		}
		var links []BlobLink
		if link.URI != "" {
			links = []BlobLink{link}
		}
		return links, hookErr
	}

	var hookErr error
	tw.OutputBlock(desc, func(ob *tap.OutputBlockWriter) *yaml_diagnostic.YAMLDiagnostic {
		lw := tapblock.NewLineWriter(ob)
		hookErr = hierarchy.Merged.RunPreMergeHookContext(ctx, hookDir, teeWriter(lw, activity))
		lw.Flush()
		if hookErr != nil {
			return &yaml_diagnostic.YAMLDiagnostic{Severity: "fail", Message: hookErr.Error()}
		}
		return nil
	})
	if hookErr != nil && ownWriter {
		tw.Plan()
	}
	return nil, hookErr
}

// teeWriter returns primary when activity is nil, else a MultiWriter that also
// streams to activity (the async job log).
func teeWriter(primary, activity io.Writer) io.Writer {
	if activity == nil {
		return primary
	}
	return io.MultiWriter(primary, activity)
}

// resolveHookDir decides where the pre-merge hook runs and returns a cleanup
// func the caller must defer.
//
// Default behavior: create a transient detached worktree pinned to hookSha as a
// hidden sibling under the repo's .worktrees/ — derived from the repo root via
// git.CommonDir(wtPath), so it is correct for BOTH worktree sessions
// (wtPath = <repo>/.worktrees/<branch>) AND implicit main-checkout sessions
// (wtPath = <repo>). The hook then verifies the exact committed tree being
// merged while the session worktree stays free for concurrent edits. The branch
// itself stays checked out in wtPath, so the build worktree is detached-HEAD (a
// hook that reads the current branch name sees "HEAD" — see
// spinclass-sweatfile(5)). The .worktrees/ parent is created if absent (a fresh
// main checkout that never ran `sc start` has none).
//
// A stale physical dir from an interrupted prior run at the exact buildPath is
// force-removed before the add: such a dir has no git admin entry, so
// `git worktree prune` (run inside WorktreeAddDetached) cannot clear it and the
// add would otherwise fail "already exists" and wedge all future merges.
//
// Legacy / opt-out: when [hooks].disable-merge-build-worktree is true, or hookSha
// is empty (RevParse failed), the hook runs in place in wtPath and cleanup is a
// no-op. A worktree-add failure is returned as an error (the gate refuses rather
// than silently degrading to an in-place run).
func resolveHookDir(hierarchy sweatfile.Hierarchy, wtPath, branch, hookSha string) (hookDir string, cleanup func(), err error) {
	noop := func() {}
	if hookSha == "" || hierarchy.Merged.MergeBuildWorktreeDisabled() {
		return wtPath, noop, nil
	}

	repoRoot, err := git.CommonDir(wtPath)
	if err != nil {
		return "", noop, fmt.Errorf("resolve repo root for build worktree: %w", err)
	}
	buildParent := filepath.Join(repoRoot, ".worktrees")
	if err := os.MkdirAll(buildParent, 0o755); err != nil {
		return "", noop, fmt.Errorf("create build worktree parent %s: %w", buildParent, err)
	}

	short := hookSha
	if len(short) > 12 {
		short = short[:12]
	}
	name := BuildWorktreePrefix + fmt.Sprintf("%s-%s-%d", sanitizeBranchForPath(branch), short, os.Getpid())
	buildPath := filepath.Join(buildParent, name)

	// Clear a stale physical dir from an interrupted prior run. This is the
	// complement to WorktreeAddDetached's internal `git worktree prune` (which
	// clears stale ADMIN ENTRIES, not unregistered physical dirs); both guard the
	// same "already exists" add failure from opposite directions. RemoveAll is a
	// no-op on a nonexistent path; a failure (e.g. a permission problem on the
	// stale tree) is propagated rather than swallowed — otherwise buildPath
	// survives and the add below fails with a confusing "already exists" that
	// hides the real cause, violating this function's no-silent-degradation
	// principle.
	if err := os.RemoveAll(buildPath); err != nil {
		return "", noop, fmt.Errorf("remove stale build worktree dir %s: %w", buildPath, err)
	}
	if err := git.WorktreeAddDetached(wtPath, buildPath, hookSha); err != nil {
		return "", noop, fmt.Errorf("create pre-merge build worktree at %s: %w", buildPath, err)
	}
	return buildPath, func() { _ = git.WorktreeForceRemove(wtPath, buildPath) }, nil
}

// sanitizeBranchForPath maps a branch name to a safe path segment: slashes (from
// branches like "feature/foo") and other separators become hyphens.
func sanitizeBranchForPath(branch string) string {
	return strings.NewReplacer("/", "-", string(filepath.Separator), "-").Replace(branch)
}

// runHookCompactContext runs the pre-merge hook and emits a single TAP test
// point with YAMLish diagnostics carrying command/format/resource_link/
// exit_code/elapsed plus a visibility field selected by the configured
// format:
//
//   - format=raw (default): hook stdout streams directly into madder
//     (for atomic content-addressable storage) and through a 15-line
//     ring; on failure the response carries `tail:` as a visibility
//     diagnostic. On success no `tail:` is emitted — the test point
//     being `ok` is itself the liveness signal and the resource_link
//     remains the authoritative full-output surface.
//
//   - format=tap-ndjson: hook stdout is captured into a buffer, parsed
//     via tap/go/pkgs/{reader,ndjson}, and the *parsed* ndjson stream
//     is written to madder (replacing the raw blob). On success, no
//     visibility field is emitted (the parsed records sit behind the
//     resource_link). On failure with at least one parsed record,
//     `failure:` is emitted summarising the failing records. On failure
//     with zero parsed records (degenerate stream), the response falls
//     back to `tail:` carrying the raw output ring.
//
// Returns a BlobLink carrying the resource_link URI and the MIME type
// matching the resolved format. If madder produced no blob (spawn
// failed, or post-hook write/parse failed), the BlobLink's URI is "".
//
// wtPath is the session worktree (where the madder blob store lives); hookDir is
// where the hook actually runs (an isolated build worktree, or wtPath in the
// legacy path). They differ only when the build-worktree feature is active.
func runHookCompactContext(ctx context.Context, tw *tap.Writer, hierarchy sweatfile.Hierarchy, wtPath, hookDir, cmd, desc string, activity io.Writer) (BlobLink, error) {
	format := hierarchy.Merged.PreMergeOutputFormatValue()

	// ring is the fallback visibility for failures the parser can't surface.
	ring := newTailRingWriter(15)

	// Structured formats buffer hook stdout and store structured output to
	// madder after the hook exits; "raw" streams straight through.
	//   - tap-ndjson : hook emits TAP-14 text, spinclass converts via tap's
	//                  aggregator and stores the ndjson.
	//   - ndjson-crap: hook emits canonical ndjson-crap directly (the crap
	//                  wire format); spinclass stores it verbatim and parses
	//                  it via crap's ndjsoncrap reader for the failure summary.
	structured := format == "tap-ndjson" || format == "ndjson-crap"

	var (
		madderStdin   io.WriteCloser
		finishMadder  func() (string, error)
		hookStdoutBuf bytes.Buffer // populated only for structured formats
	)

	if structured {
		// Buffer hook stdout fully; we write to madder below, not the raw
		// stream. madderStdin and finishMadder are placeholders here so the
		// shared variable shape compiles for both paths — the real
		// madder.Write happens after the hook exits (see the post-hook
		// switch below).
		madderStdin = nopWriteCloser{io.Discard}
		finishMadder = func() (string, error) { return "", nil }
	} else {
		var err error
		madderStdin, finishMadder, err = madder.Write(wtPath, embeds.MadderBin())
		if err != nil {
			// Madder failed to spawn; degrade to tail-only without a
			// resource_link rather than failing the hook on this account.
			madderStdin = nopWriteCloser{io.Discard}
			finishMadder = func() (string, error) { return "", err }
		}
	}

	var sink io.Writer
	if structured {
		sink = io.MultiWriter(&hookStdoutBuf, ring)
	} else {
		sink = io.MultiWriter(madderStdin, ring)
	}
	if activity != nil {
		sink = io.MultiWriter(sink, activity)
	}

	start := time.Now()
	hookErr := hierarchy.Merged.RunPreMergeHookContext(ctx, hookDir, sink)
	elapsed := time.Since(start)

	var (
		blobID    string
		madderErr error
		parsed    ndjson.Output     // tap-ndjson path
		crapTests []ndjsoncrap.Test // ndjson-crap path
		hasParse  bool
	)

	switch format {
	case "tap-ndjson":
		rd := reader.NewReader(&hookStdoutBuf)
		agg := ndjson.NewAggregator()
		for {
			ev, e := rd.Next()
			if e != nil {
				break
			}
			agg.Consume(ev)
		}
		parsed = agg.Finalize(rd.Diagnostics(), nil)
		hasParse = len(parsed.Records) > 0

		// Write parsed ndjson (not the raw stdout) to madder.
		ms, fm, mErr := madder.Write(wtPath, embeds.MadderBin())
		if mErr != nil {
			madderErr = mErr
		} else if wErr := ndjson.WriteAll(ms, parsed); wErr != nil {
			madderErr = wErr
			_ = ms.Close()
		} else {
			_ = ms.Close()
			id, fErr := fm()
			if fErr != nil {
				madderErr = fErr
			}
			blobID = id
		}

	case "ndjson-crap":
		// The hook already emitted canonical ndjson-crap. Parse it (for the
		// failure summary) and store the stream verbatim — no re-encoding.
		crapTests = collectCrapTests(bytes.NewReader(hookStdoutBuf.Bytes()))
		hasParse = len(crapTests) > 0

		ms, fm, mErr := madder.Write(wtPath, embeds.MadderBin())
		if mErr != nil {
			madderErr = mErr
		} else if _, wErr := ms.Write(hookStdoutBuf.Bytes()); wErr != nil {
			madderErr = wErr
			_ = ms.Close()
		} else {
			_ = ms.Close()
			id, fErr := fm()
			if fErr != nil {
				madderErr = fErr
			}
			blobID = id
		}

	default:
		_ = madderStdin.Close()
		blobID, madderErr = finishMadder()
	}

	extras := map[string]any{
		"command":   cmd,
		"format":    format,
		"exit_code": exitCodeFromErr(hookErr),
		"elapsed":   elapsed.Round(time.Millisecond).String(),
	}

	var blobURI string
	if blobID != "" {
		blobURI = "madder://blobs/" + blobID
		extras["resource_link"] = blobURI
	} else if madderErr != nil {
		extras["resource_link_error"] = madderErr.Error()
	}

	// Visibility field selection:
	//  - format=raw, success          → neither tail nor failure
	//  - format=raw, failure          → tail
	//  - format=tap-ndjson, success   → neither tail nor failure
	//  - format=tap-ndjson, fail+rec  → failure (built from parsed)
	//  - format=tap-ndjson, fail+!rec → tail (fallback)
	if !structured {
		if hookErr != nil {
			extras["tail"] = ring.Tail()
		}
	} else if hookErr != nil {
		switch {
		case format == "tap-ndjson" && hasParse:
			extras["failure"] = buildFailureSummary(parsed)
		case format == "ndjson-crap" && hasParse:
			extras["failure"] = buildFailureSummaryCrap(crapTests)
		default:
			extras["tail"] = ring.Tail()
		}
	}

	if hookErr != nil {
		// tap.NotOk takes map[string]string only — stringify Extras
		// so failure responses still carry the same shape.
		flat := map[string]string{
			"severity": "fail",
			"message":  hookErr.Error(),
		}
		for k, v := range extras {
			flat[k] = fmt.Sprintf("%v", v)
		}
		tw.NotOk(desc, flat)
	} else {
		tw.OkDiag(desc, &yaml_diagnostic.YAMLDiagnostic{Extras: extras})
	}
	return BlobLink{URI: blobURI, MimeType: mimeTypeForFormat(format)}, hookErr
}

// buildFailureSummary renders failing TestRecords as a multi-line string
// suitable for embedding in YAMLDiagnostic.Extras. Mirrors
// ndjson.WriteSplit's "genuine failure" definition (!OK && Directive==nil).
// One line per failing record:
//
//	#<N> <description>: <diagnostic.message>
//
// If a record has an Output block, append it as an indented continuation.
func buildFailureSummary(out ndjson.Output) string {
	var b strings.Builder
	for _, r := range out.Records {
		if r.OK || r.Directive != nil {
			continue
		}
		fmt.Fprintf(&b, "#%d %s", r.N, r.Description)
		if msg, ok := r.Diagnostic["message"].(string); ok && msg != "" {
			fmt.Fprintf(&b, ": %s", msg)
		}
		b.WriteByte('\n')
		if r.Output != nil && *r.Output != "" {
			for _, line := range strings.Split(strings.TrimRight(*r.Output, "\n"), "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// collectCrapTests decodes an ndjson-crap stream and returns its top-level
// test records (the records the failure summary reports on). Non-test
// records (plan/summary/header/execution-family) are ignored; decoding stops
// at EOF or the first undecodable line.
func collectCrapTests(r io.Reader) []ndjsoncrap.Test {
	rd := ndjsoncrap.NewReader(r)
	var tests []ndjsoncrap.Test
	for {
		rec, err := rd.Next()
		if err != nil {
			break
		}
		if t, ok := rec.(ndjsoncrap.Test); ok {
			tests = append(tests, t)
		}
	}
	return tests
}

// buildFailureSummaryCrap is the ndjson-crap analogue of buildFailureSummary:
// one line per genuinely-failing top-level test record (!OK && no directive),
// with its diagnostic message and any captured output indented beneath.
func buildFailureSummaryCrap(tests []ndjsoncrap.Test) string {
	var b strings.Builder
	for _, r := range tests {
		if r.OK || r.Directive != nil {
			continue
		}
		fmt.Fprintf(&b, "#%d %s", r.N, r.Description)
		if msg, ok := r.Diagnostic["message"].(string); ok && msg != "" {
			fmt.Fprintf(&b, ": %s", msg)
		}
		b.WriteByte('\n')
		if r.Output != nil && *r.Output != "" {
			for _, line := range strings.Split(strings.TrimRight(*r.Output, "\n"), "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// exitCodeFromErr extracts a process exit code from an *exec.ExitError.
// Returns 0 for success, -1 when err is non-nil but not an ExitError.
func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// tailRingWriter retains the last N complete lines written to it.
// Bytes written without a trailing newline are buffered until the
// next newline arrives (or Tail() flushes the partial line).
type tailRingWriter struct {
	cap   int
	lines []string
	buf   []byte
}

func newTailRingWriter(capacity int) *tailRingWriter {
	return &tailRingWriter{cap: capacity}
}

func (r *tailRingWriter) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	for {
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			break
		}
		r.appendLine(string(r.buf[:i]))
		r.buf = r.buf[i+1:]
	}
	return len(p), nil
}

func (r *tailRingWriter) appendLine(line string) {
	r.lines = append(r.lines, line)
	if len(r.lines) > r.cap {
		r.lines = r.lines[len(r.lines)-r.cap:]
	}
}

// Tail returns the joined ring contents plus any unterminated trailing
// bytes as a final line. Safe to call repeatedly; non-destructive.
func (r *tailRingWriter) Tail() string {
	lines := r.lines
	if len(r.buf) > 0 {
		// Append trailing partial line for visibility, but don't
		// retain it across future writes.
		lines = append(append([]string(nil), lines...), string(r.buf))
		if len(lines) > r.cap {
			lines = lines[len(lines)-r.cap:]
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
