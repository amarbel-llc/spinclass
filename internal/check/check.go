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

	tap "github.com/amarbel-llc/bob/packages/tap-dancer/go"
	"github.com/amarbel-llc/spinclass/internal/git"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

// Run resolves the worktree containing wtPath, loads the sweatfile
// hierarchy, and runs the configured [hooks].pre-merge command. It writes
// TAP-14 output (when format == "tap") or passthrough output otherwise to
// w. Returns a non-nil error if the hook fails.
//
// If no pre-merge hook is configured, Run returns nil and (in TAP mode)
// emits an "ok" indicating no hook is configured — agents and humans
// should treat "no hook" as a success because there is nothing to check.
func Run(w io.Writer, format, wtPath string, verbose bool) error {
	repoPath, err := git.CommonDir(wtPath)
	if err != nil {
		return fmt.Errorf("not a worktree: %s", wtPath)
	}
	branch, err := git.BranchCurrent(wtPath)
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return errors.New("could not resolve home directory")
	}
	hierarchy, err := sweatfile.LoadWorktreeHierarchy(home, repoPath, wtPath)
	if err != nil {
		return fmt.Errorf("load sweatfile hierarchy: %w", err)
	}

	var tw *tap.Writer
	ownWriter := false
	if format == "tap" {
		tw = tap.NewWriter(w)
		ownWriter = true
	}

	// In standalone mode, "no hook configured" is itself a meaningful
	// check result and should be reported as an OK with an explanation.
	// RunWithWriter intentionally stays silent on this case because the
	// merge call site predates the standalone surface and must not change
	// its TAP stream.
	cmd := hierarchy.Merged.PreMergeHookCommand()
	if cmd == nil || *cmd == "" {
		if tw != nil {
			tw.Ok("no pre-merge hook configured")
			if ownWriter {
				tw.Plan()
			}
		}
		return nil
	}

	// Delegate to RunWithWriter for the actual hook invocation. We pass
	// ownWriter=false so RunWithWriter never calls Plan() — the standalone
	// path always wants a Plan() emitted regardless of hook success/failure,
	// which RunWithWriter does not provide on its own (it only emits Plan
	// on failure to match the legacy merge call pattern).
	hookErr := RunWithWriter(tw, w, hierarchy, wtPath, branch, false)
	if tw != nil && ownWriter {
		tw.Plan()
	}
	return hookErr
}

// RunWithWriter runs the configured pre-merge hook against an already-
// loaded hierarchy and a caller-supplied tap.Writer. Pass tw=nil for
// passthrough mode. ownWriter controls whether RunWithWriter calls
// tw.Plan() when the hook fails (matching the legacy merge call pattern;
// successful hook runs leave Plan to the caller).
//
// When no pre-merge hook is configured, RunWithWriter returns nil and
// emits NO TAP output. This preserves the historical merge.runPreMergeHook
// behavior; standalone callers that want a "no hook" report should use
// Run instead.
func RunWithWriter(
	tw *tap.Writer,
	w io.Writer,
	hierarchy sweatfile.Hierarchy,
	wtPath, branch string,
	ownWriter bool,
) error {
	cmd := hierarchy.Merged.PreMergeHookCommand()
	if cmd == nil || *cmd == "" {
		return nil
	}

	if tw == nil {
		return hierarchy.Merged.RunPreMergeHook(wtPath, w)
	}

	desc := "pre-merge hook for " + branch + ": `" + *cmd + "`"

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
	if hookErr != nil && ownWriter {
		tw.Plan()
	}
	return hookErr
}

// lineWriter splits incoming bytes on '\n' and forwards each complete
// line to an OutputBlockWriter. Partial trailing content is buffered
// until a newline arrives or Flush() is called.
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
