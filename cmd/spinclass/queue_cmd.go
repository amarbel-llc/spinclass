package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/spinclass/internal/alfa/hostqueue"
)

// Host queue management (spinclass#483, slice 1: status, hold, release; design
// in docs/plans/2026-11-12-483-host-queue-management.md). `sc queue-status` is
// read-only and also an MCP tool (refused to a confined caller, like
// host-load). `sc queue-hold` and `sc queue-release` are CLI-only and refuse
// unless stdin and stdout are terminals: v1 management is the operator's own
// act. Every act goes through hostqueue.Authorize and is journalled.

// queueLedger is the ledger the management verbs act on; a seam for tests.
var queueLedger = hostqueue.DefaultLedger

type queueStatusParams struct {
	Format string `json:"format"`
}

type queueHoldParams struct {
	Reason string `json:"reason"`
}

// queueStatusView is queue-status's JSON.
type queueStatusView struct {
	Queue       *hostqueue.View `json:"queue,omitempty"`
	QueueBypass []QueueBypass   `json:"queue_bypass,omitempty"`
}

func runQueueStatus(p queueStatusParams) (string, error) {
	view := hostQueueView()
	bypass := queueBypasses()
	if p.Format == "json" {
		data, err := json.Marshal(queueStatusView{Queue: view, QueueBypass: bypass})
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	var text string
	if view == nil {
		text = "host queue: empty (no gate running, none waiting, no hold)"
	} else {
		text = strings.TrimPrefix(formatHostQueue(view, time.Now()), "\n")
	}
	return text + formatQueueBypass(bypass), nil
}

func handleQueueStatus(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if refusal := refuseConfinedServeCaller("queue-status", "sc queue-status"); refusal != nil {
		return refusal, nil
	}
	var p queueStatusParams
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	text, err := runQueueStatus(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

// queueOperator is the actor of a CLI management verb: the operator at a
// terminal, or an error when stdin and stdout are not both terminals.
func queueOperator(verb string) (hostqueue.Actor, error) {
	if !operatorHandleIsTerminal() {
		return hostqueue.Actor{}, fmt.Errorf("%s: stdin and stdout must both be terminals; managing the host queue is the operator's act and cannot be piped or driven by a program", verb)
	}
	return hostqueue.Actor{Kind: hostqueue.ActorTTY}, nil
}

func runQueueHold(p queueHoldParams) error {
	actor, err := queueOperator("queue-hold")
	if err != nil {
		return err
	}
	reason := strings.TrimSpace(p.Reason)
	if reason == "" {
		return errors.New("queue-hold: --reason is required: it is what every waiting gate is told")
	}
	ledger, err := queueLedger()
	if err != nil {
		return fmt.Errorf("queue-hold: %w", err)
	}
	out, err := ledger.PlaceHold(actor, reason, operatorHandleNow())
	if err != nil {
		return fmt.Errorf("queue-hold: %w", err)
	}
	fmt.Fprintf(operatorHandleStdout(), "host queue held: %s (running gates continue; nothing new is admitted until queue-release)\n", reason)
	if out.Warning != "" {
		fmt.Fprintln(operatorHandleStderr(), "queue-hold: "+out.Warning)
	}
	return nil
}

type queuePriorityParams struct {
	Session string `json:"session"`
	Level   string `json:"level"`
}

// queuePriorityWait is how long queue-priority waits for the waiter to apply the
// change: one poll interval plus a margin.
var queuePriorityWait = 15 * time.Second

func runQueuePriority(p queuePriorityParams) error {
	actor, err := queueOperator("queue-priority")
	if err != nil {
		return err
	}
	if strings.TrimSpace(p.Session) == "" {
		return errors.New("queue-priority: name the waiting session (as `sc queue-status` prints it)")
	}
	ledger, err := queueLedger()
	if err != nil {
		return fmt.Errorf("queue-priority: %w", err)
	}
	res, err := ledger.RequestPriority(actor, strings.TrimSpace(p.Session), p.Level, operatorHandleNow(), queuePriorityWait)
	if err != nil {
		return fmt.Errorf("queue-priority: %w", err)
	}
	if !res.Applied {
		return fmt.Errorf("queue-priority: the gate of %s did not take %s within %s (it may have been admitted: too late); its priority is unchanged (%s)", p.Session, res.New, queuePriorityWait, res.Old)
	}
	fmt.Fprintf(operatorHandleStdout(), "%s: priority %s -> %s (place %d)\n", p.Session, res.Old, res.New, res.Seq)
	return nil
}

type queueWaiterParams struct {
	Session string `json:"session"`
}

// runQueueWaiterVerb is the shared body of the per-waiter verbs that take only a
// session: the operator-terminal guard, the ledger, then the request.
func runQueueWaiterVerb(cmd, session string, request func(l hostqueue.Ledger, a hostqueue.Actor, session string) (hostqueue.WaiterResult, error), report func(res hostqueue.WaiterResult, session string) (string, error)) error {
	actor, err := queueOperator(cmd)
	if err != nil {
		return err
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return fmt.Errorf("%s: name the waiting session (as `sc queue-status` prints it)", cmd)
	}
	ledger, err := queueLedger()
	if err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}
	res, err := request(ledger, actor, session)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}
	msg, err := report(res, session)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}
	fmt.Fprintln(operatorHandleStdout(), msg)
	return nil
}

func runQueueCancel(p queueWaiterParams) error {
	return runQueueWaiterVerb("queue-cancel", p.Session,
		func(l hostqueue.Ledger, a hostqueue.Actor, s string) (hostqueue.WaiterResult, error) {
			return l.RequestCancel(a, s, operatorHandleNow(), queuePriorityWait)
		},
		func(res hostqueue.WaiterResult, s string) (string, error) {
			switch {
			case res.TooLate:
				return "", fmt.Errorf("too late: the gate of %s was admitted before it could take the cancel and is running; nothing was cancelled", s)
			case !res.Applied:
				return "", fmt.Errorf("the gate of %s did not take the cancel within %s; it is still waiting", s, queuePriorityWait)
			}
			return fmt.Sprintf("%s: wait cancelled (place %d); that gate fails and its attestation stays buffered", s, res.Seq), nil
		})
}

func runQueueHoldWaiter(p queueWaiterParams, hold bool) error {
	cmd := "queue-hold-waiter"
	if !hold {
		cmd = "queue-release-waiter"
	}
	return runQueueWaiterVerb(cmd, p.Session,
		func(l hostqueue.Ledger, a hostqueue.Actor, s string) (hostqueue.WaiterResult, error) {
			return l.RequestHoldWaiter(a, s, hold, operatorHandleNow(), queuePriorityWait)
		},
		func(res hostqueue.WaiterResult, s string) (string, error) {
			if !res.Applied {
				return "", fmt.Errorf("the gate of %s did not take it within %s (it may have been admitted); its hold is unchanged (%s)", s, queuePriorityWait, res.Old)
			}
			return fmt.Sprintf("%s: %s -> %s (place %d)", s, res.Old, res.New, res.Seq), nil
		})
}

func runQueueRelease() error {
	actor, err := queueOperator("queue-release")
	if err != nil {
		return err
	}
	ledger, err := queueLedger()
	if err != nil {
		return fmt.Errorf("queue-release: %w", err)
	}
	out, err := ledger.LiftHold(actor, operatorHandleNow())
	if err != nil {
		return fmt.Errorf("queue-release: %w", err)
	}
	if !out.Changed {
		fmt.Fprintln(operatorHandleStdout(), "host queue was not held; nothing to release")
		return nil
	}
	fmt.Fprintln(operatorHandleStdout(), "host queue released")
	if out.Warning != "" {
		fmt.Fprintln(operatorHandleStderr(), "queue-release: "+out.Warning)
	}
	return nil
}

// registerQueueCommands registers the queue management commands.
func registerQueueCommands(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "queue-status",
		Title: "Host Queue Status",
		Description: command.Description{
			Short: "Show the host admission queue (#424, #483): gates running, waiters in order with their place and reason, the operator's hold, and live sessions whose serve binary has no queue code (QUEUE BYPASS). Read-only.",
		},
		Annotations: &protocol.ToolAnnotations{
			ReadOnlyHint:    protocol.BoolPtr(true),
			DestructiveHint: protocol.BoolPtr(false),
			IdempotentHint:  protocol.BoolPtr(true),
			OpenWorldHint:   protocol.BoolPtr(false),
		},
		Params: []command.Param{
			{Name: "format", Type: command.String, Description: "json for a machine-readable view; default is text"},
		},
		Run:    wrapMCPHandler("queue-status", handleQueueStatus),
		RunCLI: ungatedCLI(runQueueStatus),
	})
	app.AddCommand(&command.Command{
		Name:  "queue-hold",
		Title: "Hold the host queue",
		Description: command.Description{
			Short: "Hold the host admission queue as the operator at a terminal (#483): nothing new is admitted until queue-release; running gates continue.",
			Long: "Writes the operator's hold with --reason, which every waiting gate is shown. Journalled to $XDG_STATE_HOME/spinclass/host-queue/journal.ndjson " +
				"with actor tty (as strong as isatty, not an authenticated act). The hold is the brake, so it still takes effect, with a warning, if the journal cannot be written. " +
				"It refuses unless stdin and stdout are both terminals, so an agent cannot drive it. CLI-only.",
		},
		Params: []command.Param{
			{Name: "reason", Type: command.String, Required: true, Description: "Why the queue is held; shown to every waiter"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p queueHoldParams
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("queue-hold: invalid arguments: %w", err)
			}
			return runQueueHold(p)
		},
	})
	app.AddCommand(&command.Command{
		Name:  "queue-priority",
		Title: "Set a waiting gate's priority",
		Description: command.Description{
			Short: "Set the priority (low, normal, high) of a gate waiting in the host admission queue, as the operator at a terminal (#483). CLI-only.",
			Long: "Order is priority first, then place; a waiter also ages one level per 30 minutes waited (restarting when its priority is set). " +
				"A priority never bypasses the hold, the gate cap, memory, pressure, disk, load or settle. The change is sent to the waiter, which applies it " +
				"at its next look (at most one poll), and journalled with actor tty before it is sent. It refuses unless stdin and stdout are both terminals.",
		},
		Params: []command.Param{
			{Name: "session", Type: command.String, Required: true, Description: "The waiting session, as queue-status prints it (<repo>/<branch>)"},
			{Name: "level", Type: command.String, Required: true, Description: "low, normal or high"},
		},
		RunCLI: func(_ context.Context, args json.RawMessage) error {
			var p queuePriorityParams
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("queue-priority: invalid arguments: %w", err)
			}
			return runQueuePriority(p)
		},
	})
	waiterParam := []command.Param{
		{Name: "session", Type: command.String, Required: true, Description: "The waiting session, as queue-status prints it (<repo>/<branch>)"},
	}
	waiterCmd := func(name, title, short, long string, run func(queueWaiterParams) error) {
		app.AddCommand(&command.Command{
			Name: name, Title: title,
			Description: command.Description{Short: short, Long: long + " It refuses unless stdin and stdout are both terminals. The change is journalled (fail closed) and sent to the waiter, which applies it at its next look (at most one poll). CLI-only."},
			Params:      waiterParam,
			RunCLI: func(_ context.Context, args json.RawMessage) error {
				var p queueWaiterParams
				if err := json.Unmarshal(args, &p); err != nil {
					return fmt.Errorf("%s: invalid arguments: %w", name, err)
				}
				return run(p)
			},
		})
	}
	waiterCmd("queue-cancel", "Cancel a waiting gate",
		"End a waiting gate's wait in the host admission queue, as the operator at a terminal (#483). That gate fails; its attestation stays buffered.",
		"A cancel that loses the race (the gate was admitted inside its poll) is ignored and reported as too late.",
		runQueueCancel)
	waiterCmd("queue-hold-waiter", "Hold one waiting gate",
		"Hold one waiting gate in the host admission queue, as the operator at a terminal (#483): it keeps its place but is skipped, and its max-wait clock and aging pause.",
		"A hold expires after max-wait (never with max-wait \"0\"); the expiry is journalled.",
		func(p queueWaiterParams) error { return runQueueHoldWaiter(p, true) })
	waiterCmd("queue-release-waiter", "Release one held gate",
		"Lift the hold on one waiting gate in the host admission queue, as the operator at a terminal (#483).",
		"The held time does not count against max-wait and does not age the gate.",
		func(p queueWaiterParams) error { return runQueueHoldWaiter(p, false) })
	app.AddCommand(&command.Command{
		Name:  "queue-release",
		Title: "Release the host queue",
		Description: command.Description{
			Short: "Lift the operator's hold on the host admission queue, as the operator at a terminal (#483). CLI-only.",
			Long: "Removes the hold; waiting gates are admitted again as the queue allows. Journalled with actor tty. " +
				"Refuses unless stdin and stdout are both terminals.",
		},
		RunCLI: func(_ context.Context, _ json.RawMessage) error { return runQueueRelease() },
	})
}
