package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/servelog"
)

// restart-hung-child is the operator-ruled (2026-10-26) bounded forced restart of
// a child that is stuck: a busy turn that never ends cannot take the unforced
// restart (it waits for a turn end) and the `force` form is always-ask. It is
// allowed only with the restart right on the child, only while the child's
// verdict is busy-too-long NOW, and at most once per child per day; every use is
// logged and woken to the holders.

// hungRestartCooldown is the minimum gap between forced restarts of one child.
const hungRestartCooldown = 24 * time.Hour

// healthDepsFn is a seam so tests supply the status and the clock.
var healthDepsFn = productionHealthDeps

type restartHungParams struct {
	Child  string `json:"child"`
	Reason string `json:"reason"`
}

var errHungRestartCooldown = errors.New("forced restart cooldown")

func runRestartHung(p restartHungParams) (string, error) {
	if p.Child == "" {
		return "", errors.New("child is required")
	}
	child, callerPrincipal, err := authorizeRestartChild("restart-hung-child", p.Child)
	if err != nil {
		return "", err
	}
	childPrincipal, err := ownerPrincipal(child, "", "restart",
		"The operator can restart it from a terminal outside the session.")
	if err != nil {
		return "", err
	}
	return restartHungWith(child, callerPrincipal, childPrincipal, p.Reason, healthDepsFn())
}

func restartHungWith(child *session.State, caller, childPrincipal, reason string, d healthDeps) (string, error) {
	v, why := sessionHealth(child, d)
	if v != hungstatus.VerdictBusyTooLong {
		return "", fmt.Errorf("%s is not hung (%s: %s); a forced restart needs a busy-too-long verdict", child.Key(), v, why)
	}

	// Claim the day under the session lock before the hand-off, so two callers
	// cannot both restart the child; roll it back if the hand-off fails.
	now := d.Now
	var previous *time.Time
	err := session.Update(child.RepoPath, child.Branch, func(cur *session.State) error {
		if cur.ForcedRestartAt != nil && now.Sub(*cur.ForcedRestartAt) < hungRestartCooldown {
			return fmt.Errorf("%w: %s was force-restarted at %s; at most once per child per %s",
				errHungRestartCooldown, child.Key(), cur.ForcedRestartAt.UTC().Format(time.RFC3339), hungRestartCooldown)
		}
		previous = cur.ForcedRestartAt
		at := now
		cur.ForcedRestartAt = &at
		return nil
	})
	if err != nil {
		return "", err
	}

	if reason == "" {
		reason = why
	}
	text, err := queueRestart(child, caller, childPrincipal, "hung: "+reason, true)
	if err != nil {
		if rerr := session.Update(child.RepoPath, child.Branch, func(cur *session.State) error {
			cur.ForcedRestartAt = previous
			return nil
		}); rerr != nil {
			servelog.Errorf("restart-hung rollback-failed key=%s err=%v", child.Key(), rerr)
		}
		return "", err
	}
	servelog.Infof("restart-hung forced key=%s by=%s why=%q", child.Key(), caller, why)
	msg := fmt.Sprintf("%s was force-restarted as hung by %s (%s)", child.Key(), caller, why)
	wakeRestartAudience(child, "restart-hung", child.RestartWakeTargets(caller, childPrincipal), msg)
	return text, nil
}

// handleRestartHungChild is the `restart-hung-child` MCP tool handler. Its
// authority is a handle read from agent-writable session state, so a confined
// caller is refused first.
func handleRestartHungChild(_ context.Context, args json.RawMessage, _ command.Prompter) (*command.Result, error) {
	if refusal := refuseConfinedServeCaller("restart-hung-child", "sc restart-hung-child"); refusal != nil {
		return refusal, nil
	}
	var p restartHungParams
	if err := json.Unmarshal(args, &p); err != nil {
		return command.TextErrorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	text, err := runRestartHung(p)
	if err != nil {
		return command.TextErrorResult(err.Error()), nil
	}
	return command.TextResult(text), nil
}

func restartHungChildParamList() []command.Param {
	return []command.Param{
		{
			Name:        "child",
			Type:        command.String,
			Required:    true,
			Description: "The held session to force-restart, as its <repo>/<branch> session key or worktree directory name. Needs the `restart` right on it.",
		},
		{
			Name:        "reason",
			Type:        command.String,
			Description: "Why; passed to the frontend and shown to the holders. Defaults to the verdict's reason.",
		},
	}
}

func restartHungChildAnnotations() *protocol.ToolAnnotations {
	return &protocol.ToolAnnotations{
		ReadOnlyHint:    protocol.BoolPtr(false),
		DestructiveHint: protocol.BoolPtr(true),
		IdempotentHint:  protocol.BoolPtr(false),
		OpenWorldHint:   protocol.BoolPtr(false),
	}
}
