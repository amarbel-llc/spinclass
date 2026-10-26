package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/spinclass/internal/0/hostload"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

const (
	hostLoadDefaultTop       = 10
	hostLoadMaxTop           = 50
	hostLoadDefaultSampleMS  = 500
	hostLoadMaxSampleMS      = 5000
	hostLoadDefaultThreshold = 1.0
)

// hostLoadParams is the parameter set of the `host-load` tool. Pointers tell
// an absent parameter (use the default) from an explicit zero.
type hostLoadParams struct {
	globalArgs
	Top       *int     `json:"top"`
	SampleMS  *int     `json:"sample_ms"`
	Threshold *float64 `json:"threshold"`
}

// hostLoadOptions resolves the defaults and bounds of p.
func hostLoadOptions(p hostLoadParams, sessions []hostload.Session) (hostload.Options, error) {
	top, sampleMS, threshold := hostLoadDefaultTop, hostLoadDefaultSampleMS, hostLoadDefaultThreshold
	if p.Top != nil {
		top = *p.Top
	}
	if p.SampleMS != nil {
		sampleMS = *p.SampleMS
	}
	if p.Threshold != nil {
		threshold = *p.Threshold
	}
	if top < 1 {
		return hostload.Options{}, fmt.Errorf("top must be at least 1, got %d", top)
	}
	if sampleMS < 1 {
		return hostload.Options{}, fmt.Errorf("sample_ms must be at least 1, got %d", sampleMS)
	}
	if threshold < 0 {
		return hostload.Options{}, fmt.Errorf("threshold must not be negative, got %v", threshold)
	}
	return hostload.Options{
		Top:       min(top, hostLoadMaxTop),
		Sample:    time.Duration(min(sampleMS, hostLoadMaxSampleMS)) * time.Millisecond,
		Threshold: threshold,
		Sessions:  sessions,
	}, nil
}

// liveHostLoadSessions lists the sessions a busy process can be attributed to:
// every non-tombstone state that has a worktree.
func liveHostLoadSessions() ([]hostload.Session, error) {
	all, err := session.ListAll(nil)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	var out []hostload.Session
	for i := range all {
		s := &all[i]
		if s.IsTombstone() || s.WorktreePath == "" {
			continue
		}
		out = append(out, hostload.Session{Key: s.Key(), WorktreePath: s.WorktreePath})
	}
	return out, nil
}

// formatHostLoad renders rep as a header line plus one line per process.
func formatHostLoad(rep hostload.Report) string {
	verdict := "ok"
	if rep.OverThreshold {
		verdict = "OVER"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cpus %d  load 1/5/15: %.2f %.2f %.2f  per-cpu %.2f (threshold %g: %s)",
		rep.CPUs, rep.Load1, rep.Load5, rep.Load15, rep.LoadPerCPU, rep.Threshold, verdict)
	for _, p := range rep.Top {
		sess := p.Session
		if sess == "" {
			sess = "-"
		}
		fmt.Fprintf(&b, "\n%d %s %.1f%% %s", p.PID, p.Comm, p.CPUPercent, sess)
	}
	return b.String()
}

// runHostLoad is the shared host-load flow: sample the host, attribute the
// busiest processes to sessions and render text, or JSON with --format json.
func runHostLoad(p hostLoadParams) (string, error) {
	sessions, err := liveHostLoadSessions()
	if err != nil {
		return "", err
	}
	opts, err := hostLoadOptions(p, sessions)
	if err != nil {
		return "", err
	}
	rep, err := hostload.Collect(opts)
	if err != nil {
		return "", err
	}
	if p.Format == "json" {
		data, err := json.Marshal(rep)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return formatHostLoad(rep), nil
}

// handleHostLoad is the `host-load` MCP tool handler. It names every session
// and every process on the host, the sibling enumeration `list` refuses a
// confined caller for (FDR 0035), so it refuses one too; the CLI is the
// operator's path.
func handleHostLoad(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if refusal := refuseConfinedServeCaller("host-load", "sc host-load"); refusal != nil {
		return refusal, nil
	}
	var p hostLoadParams
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	text, err := runHostLoad(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// registerHostLoadCommand registers `host-load` as both MCP tool and CLI
// command (#388).
func registerHostLoadCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "host-load",
		Title: "Host Load",
		Description: command.Description{
			Short: "Report host load before a heavy build, test or merge (#388). Read-only. Returns the CPU count, the 1/5/15-minute load averages, whether the host is above `threshold` (the per-CPU 1-minute load, load1 divided by CPUs), and the busiest processes over a short sample window, each with the spinclass session whose worktree it runs in (matched by process working directory), so a pile-up can be traced to the sessions causing it.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{
			{Name: "top", Type: command.Int, Description: fmt.Sprintf("How many of the busiest processes to list (default %d, max %d)", hostLoadDefaultTop, hostLoadMaxTop)},
			{Name: "sample_ms", Type: command.Int, Description: fmt.Sprintf("Sampling window in milliseconds for per-process CPU (default %d, max %d)", hostLoadDefaultSampleMS, hostLoadMaxSampleMS)},
			{Name: "threshold", Type: command.Float, Description: fmt.Sprintf("Per-CPU 1-minute load above which the host counts as over threshold (default %g)", hostLoadDefaultThreshold)},
		},
		Run:    wrapMCPHandler("host-load", handleHostLoad),
		RunCLI: ungatedCLI(runHostLoad),
	})
}
