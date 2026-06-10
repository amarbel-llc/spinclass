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

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
	"github.com/amarbel-llc/crap/go-crap/v2/ndjsoncrap"
	"github.com/amarbel-llc/spinclass/internal/embeds"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/madder"
	"github.com/amarbel-llc/spinclass/internal/present"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
	"github.com/amarbel-llc/tap/go/pkgs/ndjson"
	"github.com/amarbel-llc/tap/go/pkgs/reader"
)

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
// hierarchy, and runs the configured [hooks].pre-merge command, emitting
// ndjson-crap records via rep (never nil): a result-family test point for
// the hook stage plus an execution-family Phase carrying the hook's live
// output lines. Returns the resource_link blobs emitted for the hook
// output (one per hook step that produced a madder blob; empty when
// madder is not pinned) and a non-nil error if the hook fails. Each
// BlobLink carries the MIME type matching the format the blob was written
// in (text/plain for raw, application/x-ndjson for tap-ndjson and
// ndjson-crap).
//
// If no pre-merge hook is configured, Run returns (nil, nil) and emits an
// "ok" test point indicating no hook is configured — agents and humans
// should treat "no hook" as a success because there is nothing to check.
func Run(rep *crap.Reporter, wtPath string) ([]BlobLink, error) {
	return RunContext(context.Background(), rep, wtPath, nil)
}

// RunContext is Run bound to ctx, with an optional activity writer that
// receives the pre-merge hook's live output in addition to the normal
// madder/ring/record plumbing. The async job runner passes its job log as
// activity so session-job-status can tail live progress and derive a
// last-activity timestamp; synchronous callers use Run (background ctx, nil
// activity). ctx cancellation kills the hook subprocess.
//
// RunContext owns its result-family stream: it opens a TestStream with a
// plan of one (the hook stage) and finishes it before returning. Callers
// that share a stream across stages (merge) use RunWithReporterContext.
func RunContext(ctx context.Context, rep *crap.Reporter, wtPath string, activity io.Writer) ([]BlobLink, error) {
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

	ts := rep.TestStream(1)

	cmd := hierarchy.Merged.PreMergeHookCommand()
	if cmd == nil || *cmd == "" {
		ts.Ok("no pre-merge hook configured")
		ts.Finish()
		return nil, nil
	}

	// Pin HEAD so the hook verifies the committed tree in an isolated build
	// worktree (see resolveHookDir). RevParse failure degrades to "" → in-place.
	hookSha, _ := git.RevParse(wtPath, "HEAD")
	links, hookErr := RunWithReporterContext(ctx, rep, ts, hierarchy, wtPath, branch, hookSha, activity)
	ts.Finish()
	return links, hookErr
}

// RunWithReporterContext runs the configured pre-merge hook against an
// already-loaded hierarchy, emitting onto a caller-supplied Reporter and
// result-family TestStream. ts is the caller's shared stream when check
// runs inside a merge (one stream numbers all merge stages); the caller
// owns ts.Finish(). ctx threads to the hook subprocess (cancellable);
// activity, when non-nil, is teed the hook's live output alongside the
// normal madder/ring/record destination.
//
// Returns the resource_link blobs emitted for hook output (madder-pinned
// builds only; empty otherwise) and a non-nil error if the hook fails.
// Each BlobLink carries the MIME type matching the format the blob was
// written in.
//
// When no pre-merge hook is configured, RunWithReporterContext returns
// (nil, nil) and emits NO records. This preserves the historical
// merge.runPreMergeHook behavior; standalone callers that want a "no
// hook" report should use Run instead.
func RunWithReporterContext(
	ctx context.Context,
	rep *crap.Reporter,
	ts *crap.TestStream,
	hierarchy sweatfile.Hierarchy,
	wtPath, branch, hookSha string,
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
		ts.NotOk("pre-merge build worktree for "+branch, map[string]any{
			"severity": "fail",
			"message":  prepErr.Error(),
		})
		return nil, prepErr
	}
	defer cleanup()

	desc := "pre-merge hook for " + branch + ": `" + *cmd + "`"
	link, hookErr := runHookPhase(ctx, rep, ts, hierarchy, wtPath, hookDir, *cmd, desc, activity)
	var links []BlobLink
	if link.URI != "" {
		links = []BlobLink{link}
	}
	return links, hookErr
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

// runHookPhase runs the pre-merge hook wrapped in an execution-family
// Phase (live Output records — the viewport's rolling tail) and closed by
// a result-family test point on ts carrying the failure diagnostic:
//
//   - format=raw (default): hook stdout streams directly into madder
//     (for atomic content-addressable storage, when pinned) and through a
//     15-line ring; on failure the diagnostic's `output` entry carries
//     the ring tail. On success no `output` is emitted — the test point
//     being `ok` is itself the liveness signal and the resource_link
//     remains the authoritative full-output surface.
//
//   - format=tap-ndjson: hook stdout is captured into a buffer, parsed
//     via tap/go/pkgs/{reader,ndjson}, and the *parsed* ndjson stream
//     is written to madder (replacing the raw blob). On success, no
//     `output` is emitted (the parsed records sit behind the
//     resource_link). On failure with at least one parsed record,
//     `output` carries a summary of the failing records. On failure
//     with zero parsed records (degenerate stream), `output` falls
//     back to the raw output ring.
//
//   - format=ndjson-crap: like tap-ndjson, but the hook already emits
//     canonical ndjson-crap; the stream is stored verbatim and parsed
//     via crap's ndjsoncrap reader for the failure summary.
//
// When madder is not pinned (embeds.MadderBin() == "") the hook still
// runs and its lines still stream as Output records — no blob is stored
// and no resource_link line is emitted.
//
// Returns a BlobLink carrying the resource_link URI and the MIME type
// matching the resolved format. If madder produced no blob (not pinned,
// spawn failed, or post-hook write/parse failed), the BlobLink's URI is "".
//
// wtPath is the session worktree (where the madder blob store lives); hookDir is
// where the hook actually runs (an isolated build worktree, or wtPath in the
// legacy path). They differ only when the build-worktree feature is active.
func runHookPhase(ctx context.Context, rep *crap.Reporter, ts *crap.TestStream, hierarchy sweatfile.Hierarchy, wtPath, hookDir, cmd, desc string, activity io.Writer) (BlobLink, error) {
	format := hierarchy.Merged.PreMergeOutputFormatValue()
	madderPinned := embeds.MadderBin() != ""

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

	// Placeholders cover the structured path (madder.Write happens after the
	// hook exits) and the madder-not-pinned path (no blob at all).
	var (
		madderStdin   io.WriteCloser         = nopWriteCloser{io.Discard}
		finishMadder  func() (string, error) = func() (string, error) { return "", nil }
		hookStdoutBuf bytes.Buffer           // populated only for structured formats
	)

	if !structured && madderPinned {
		var err error
		madderStdin, finishMadder, err = madder.Write(wtPath, embeds.MadderBin())
		if err != nil {
			// Madder failed to spawn; degrade to tail-only without a
			// resource_link rather than failing the hook on this account.
			madderStdin = nopWriteCloser{io.Discard}
			finishMadder = func() (string, error) { return "", err }
		}
	}

	ph := rep.Phase(desc)
	ph.Command(cmd)
	lw := present.NewLineWriter(ph)

	var sink io.Writer
	if structured {
		sink = io.MultiWriter(&hookStdoutBuf, ring, lw)
	} else {
		sink = io.MultiWriter(madderStdin, ring, lw)
	}
	if activity != nil {
		sink = io.MultiWriter(sink, activity)
	}

	start := time.Now()
	hookErr := hierarchy.Merged.RunPreMergeHookContext(ctx, hookDir, sink)
	elapsed := time.Since(start)
	lw.Flush()

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
		if madderPinned {
			ms, fm, mErr := madder.Write(wtPath, embeds.MadderBin())
			if mErr != nil {
				madderErr = mErr
			} else if wErr := ndjson.WriteAll(ms, parsed); wErr != nil {
				madderErr = wErr
				_ = ms.Close()
				_, _ = fm() // reap the subprocess even on write failure
			} else {
				_ = ms.Close()
				id, fErr := fm()
				if fErr != nil {
					madderErr = fErr
				}
				blobID = id
			}
		}

	case "ndjson-crap":
		// The hook already emitted canonical ndjson-crap. Parse it (for the
		// failure summary) and store the stream verbatim — no re-encoding.
		crapTests = collectCrapTests(bytes.NewReader(hookStdoutBuf.Bytes()))
		hasParse = len(crapTests) > 0

		if madderPinned {
			ms, fm, mErr := madder.Write(wtPath, embeds.MadderBin())
			if mErr != nil {
				madderErr = mErr
			} else if _, wErr := ms.Write(hookStdoutBuf.Bytes()); wErr != nil {
				madderErr = wErr
				_ = ms.Close()
				_, _ = fm() // reap the subprocess even on write failure
			} else {
				_ = ms.Close()
				id, fErr := fm()
				if fErr != nil {
					madderErr = fErr
				}
				blobID = id
			}
		}

	default:
		_ = madderStdin.Close()
		blobID, madderErr = finishMadder()
	}

	// The blob link rides the wire too (viewport + plain rendering); the
	// BlobLink return value stays the MCP resource_link path.
	var blobURI string
	if blobID != "" {
		blobURI = "madder://blobs/" + blobID
		ph.Output(ndjsoncrap.StreamStdout, "resource_link: "+blobURI+"\n")
	} else if madderErr != nil {
		ph.Output(ndjsoncrap.StreamStderr, "resource_link_error: "+madderErr.Error()+"\n")
	}

	if hookErr == nil {
		ph.Done()
		ts.Ok(desc)
		return BlobLink{URI: blobURI, MimeType: mimeTypeForFormat(format)}, nil
	}

	// Failure-output selection (the diagnostic's `output` entry):
	//  - format=raw                   → ring tail
	//  - format=tap-ndjson, rec       → failure summary (built from parsed)
	//  - format=ndjson-crap, rec      → failure summary (built from crap tests)
	//  - structured, no records       → ring tail (fallback)
	var failureText string
	switch {
	case format == "tap-ndjson" && hasParse:
		failureText = buildFailureSummary(parsed)
	case format == "ndjson-crap" && hasParse:
		failureText = buildFailureSummaryCrap(crapTests)
	default:
		failureText = ring.Tail()
	}

	diagnostic := map[string]any{
		"severity":  "fail",
		"message":   hookErr.Error(),
		"command":   cmd,
		"format":    format,
		"exit_code": exitCodeFromErr(hookErr),
		"elapsed":   elapsed.Round(time.Millisecond).String(),
		"output":    failureText,
	}
	if blobURI != "" {
		diagnostic["resource_link"] = blobURI
	} else if madderErr != nil {
		diagnostic["resource_link_error"] = madderErr.Error()
	}

	ph.Fail(hookErr)
	ts.NotOk(desc, diagnostic)
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
