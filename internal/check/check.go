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
	"os/exec"
	"strings"
	"time"

	"github.com/amarbel-llc/spinclass/internal/embeds"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/madder"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/tapblock"
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
	"github.com/amarbel-llc/tap/go/pkgs/yaml_diagnostic"
)

// Directive emitted by the compact (madder-pinned) shape so agents
// reading the response know they don't need to fetch the
// resource_link unless the test point failed.
const compactDirective = "directive: if status is ok, the resource_link need not be followed; only inspect on failure"

// Run resolves the worktree containing wtPath, loads the sweatfile
// hierarchy, and runs the configured [hooks].pre-merge command. It writes
// TAP-14 output (when format == "tap") or passthrough output otherwise to
// w. Returns the resource_link URIs emitted for the hook output (one per
// hook step that produced a madder blob; empty when madder is not pinned)
// and a non-nil error if the hook fails.
//
// If no pre-merge hook is configured, Run returns (nil, nil) and (in TAP
// mode) emits an "ok" indicating no hook is configured — agents and
// humans should treat "no hook" as a success because there is nothing to
// check.
//
// The verbose parameter is accepted for API stability but currently
// unused; check itself emits no git output today. It is reserved for
// future use when verbose-mode diagnostics become relevant.
func Run(w io.Writer, format, wtPath string, verbose bool) ([]string, error) {
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
	hierarchy, err := sweatfile.LoadWorktreeHierarchy(home, repoPath, wtPath)
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

	// ownWriter=false: Run owns Plan emission for the standalone path.
	blobURIs, hookErr := RunWithWriter(tw, w, hierarchy, wtPath, branch, false)
	if tw != nil && ownWriter {
		tw.Plan()
	}
	return blobURIs, hookErr
}

// RunWithWriter runs the configured pre-merge hook against an already-
// loaded hierarchy and a caller-supplied tap.Writer. Pass tw=nil for
// passthrough mode. ownWriter controls whether RunWithWriter calls
// tw.Plan() when the hook fails (matching the legacy merge call pattern;
// successful hook runs leave Plan to the caller).
//
// Returns the resource_link URIs emitted for hook output (compact path
// only; empty otherwise) and a non-nil error if the hook fails.
//
// When no pre-merge hook is configured, RunWithWriter returns (nil, nil)
// and emits NO TAP output. This preserves the historical
// merge.runPreMergeHook behavior; standalone callers that want a "no
// hook" report should use Run instead.
func RunWithWriter(
	tw *tap.Writer,
	w io.Writer,
	hierarchy sweatfile.Hierarchy,
	wtPath, branch string,
	ownWriter bool,
) ([]string, error) {
	cmd := hierarchy.Merged.PreMergeHookCommand()
	if cmd == nil || *cmd == "" {
		return nil, nil
	}

	if tw == nil {
		return nil, hierarchy.Merged.RunPreMergeHook(wtPath, w)
	}

	desc := "pre-merge hook for " + branch + ": `" + *cmd + "`"

	if embeds.MadderBin() != "" {
		blobURI, hookErr := runHookCompact(tw, hierarchy, wtPath, *cmd, desc)
		if hookErr != nil && ownWriter {
			tw.Plan()
		}
		var blobURIs []string
		if blobURI != "" {
			blobURIs = []string{blobURI}
		}
		return blobURIs, hookErr
	}

	var hookErr error
	tw.OutputBlock(desc, func(ob *tap.OutputBlockWriter) *yaml_diagnostic.YAMLDiagnostic {
		lw := tapblock.NewLineWriter(ob)
		hookErr = hierarchy.Merged.RunPreMergeHook(wtPath, lw)
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

// runHookCompact runs the pre-merge hook with bytes tee'd into both
// (a) madder's stdin (for atomic content-addressable storage and a
// resource_link URI), and (b) an in-memory tail ring so the last 15
// lines surface in-band. Emits a single TAP test point with YAMLish
// diagnostics carrying command/tail/resource_link/exit_code/elapsed.
//
// Returns the resource_link URI when madder produced a blob, or "" when
// it didn't (madder spawn failed, or post-hook write/parse failed).
func runHookCompact(tw *tap.Writer, hierarchy sweatfile.Hierarchy, wtPath, cmd, desc string) (string, error) {
	madderStdin, finishMadder, err := madder.Write(wtPath, embeds.MadderBin())
	if err != nil {
		// Madder failed to spawn; degrade to tail-only without a
		// resource_link rather than failing the hook on this account.
		madderStdin = nopWriteCloser{io.Discard}
		finishMadder = func() (string, error) { return "", err }
	}
	ring := newTailRingWriter(15)
	sink := io.MultiWriter(madderStdin, ring)

	start := time.Now()
	hookErr := hierarchy.Merged.RunPreMergeHook(wtPath, sink)
	elapsed := time.Since(start)
	_ = madderStdin.Close()
	blobID, madderErr := finishMadder()

	extras := map[string]any{
		"command":   cmd,
		"tail":      ring.Tail(),
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
	return blobURI, hookErr
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
