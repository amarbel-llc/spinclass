package main

import (
	"context"
	"errors"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// The refusal texts of the session verbs over the bridge are the table
// "Refusal texts" in docs/plans/2026-11-25-bridge-wire-v1.md. Each says what is
// true: a gated verb has no named right a session inside its unit could hold
// (the gate is the transport, not a handle right), so it says so and names who
// may do the thing instead; a verb that needs a trusted setting names the
// setting and who sets it. No text carries a value from the request.
const (
	bridgeRestartSelfRefusal = bridgeRefusalPrefix + "restart-this-session needs the trusted [session-entry].restart hand-off; the operator sets it in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)"
	bridgeCloseSelfRefusal   = bridgeRefusalPrefix + "close-this-session needs the trusted [permissions].allow-self-close = true; the operator sets it in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)"
)

// bridgeSessionRefusals is the refusal of each gated session verb, keyed by the
// tool label its gate passes to refuseConfinedServeCaller.
var bridgeSessionRefusals = map[string]string{
	"cancel-restart":      bridgeRefusalPrefix + "cancel-restart naming another session needs the right \"restart\" on that session, which a session inside its unit may not use whatever it holds; a holder with the right outside the unit, or the operator running `sc cancel-restart` at a terminal outside the unit, can cancel it (cancel-restart with no child cancels this session's own)",
	"list-handles accept": bridgeRefusalPrefix + "list-handles with accept has no right a session inside its unit can be granted; the operator runs `sc list-handles --accept` at a terminal outside the unit (a plain list-handles is allowed)",
}

// settingKind names the trusted setting a refusal is for.
type settingKind int

const (
	settingRestartHandoff settingKind = iota + 1
	settingAllowSelfClose
)

// settingAbsentError is a refusal for want of an operator setting. Its text is
// the one every caller has always had; a bridged caller is given the contract
// text for the setting instead (bridgeSettingRefusal).
type settingAbsentError struct {
	msg  string
	kind settingKind
}

func (e *settingAbsentError) Error() string { return e.msg }

// bridgedCall says the call arrived on the bridge.
func bridgedCall(ctx context.Context) bool {
	o, ok := confinement.OriginFrom(ctx)
	return ok && o.Bridged()
}

// bridgeSettingRefusal is the tool result for err when it is a missing-setting
// refusal of kind and ctx is a bridged call: the contract text. Everything else
// (another error, or a call that is not bridged) is err's own text, unchanged.
func bridgeSettingRefusal(ctx context.Context, err error, kind settingKind, text string) *command.Result {
	var se *settingAbsentError
	if bridgedCall(ctx) && errors.As(err, &se) && se.kind == kind {
		return command.TextErrorResult(text)
	}
	return command.TextErrorResult(err.Error())
}
