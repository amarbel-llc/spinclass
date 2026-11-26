package main

import (
	"context"
	"encoding/json"
	"errors"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// The refusal texts of the session verbs over the bridge are the table
// "Refusals" in spinclass-bridge(7). Each says what is
// true: a gated verb has no named right a session inside its unit could hold
// (the gate is the transport, not a handle right), so it says so and names who
// may do the thing instead; a verb that needs a trusted setting names the
// setting and who sets it. No text carries a value from the request.
const (
	bridgeRestartSelfRefusal = bridgeRefusalPrefix + "restart-this-session needs the trusted [session-entry].restart hand-off; the operator sets it in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)"
	bridgeCloseSelfRefusal   = bridgeRefusalPrefix + "close-this-session needs the trusted [permissions].allow-self-close = true; the operator sets it in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)"
)

// The always-ask floor lives only in the hook that runs inside the unit, so a
// forced or widening act never rests on it over the bridge: the verb refuses the
// escalation arguments itself and names who can do the thing (a holder with the
// right, or the operator, both outside the unit).
const (
	bridgeRestartEscalationRefusal = bridgeRefusalPrefix + "restart-this-session with force, fresh_env or posture is not available to a session inside its unit; a holder with the right \"restart\" on this session, or the operator at a terminal outside the unit, runs `sc restart-child-session <session>` with --force or --posture (--force is always asked of the human); no command offers fresh_env"
	bridgeCloseForceRefusal        = bridgeRefusalPrefix + "close-this-session takes no force argument and never discards work; the operator at a terminal outside the unit discards work with `sc close --force <session>`"
)

// argPresent says whether the JSON object args names key at all, whatever its
// value; arguments that do not parse name nothing (the verb's own parse reports
// them).
func argPresent(args json.RawMessage, key string) bool {
	var raw map[string]json.RawMessage
	if json.Unmarshal(args, &raw) != nil {
		return false
	}
	_, ok := raw[key]
	return ok
}

// bridgeSessionRefusals is the refusal of each gated session verb, keyed by the
// tool label its gate passes to refuseConfinedServeCaller.
var bridgeSessionRefusals = map[string]string{
	"cancel-restart":      bridgeRefusalPrefix + "cancel-restart naming another session needs the right \"restart\" on that session, which a session inside its unit may not use whatever it holds; a holder with the right, or the operator, runs `sc cancel-restart --child <session>` at a terminal outside the unit (cancel-restart with no child cancels this session's own)",
	"list-handles accept": bridgeRefusalPrefix + "list-handles with accept has no right a session inside its unit can be granted; the operator runs `sc list-handles --accept` at a terminal outside the unit (a plain list-handles is allowed)",
}

// policyRefusalError is a RULE saying no (a missing operator setting, a running
// job, a posture that may not change), as opposed to an error (a hand-off that
// would not exec, an I/O failure). msg is the text every caller has always had; a
// bridged caller is given bridge instead, in the contract's form: the refused:
// prefix, what is true, and who can lift the rule, never a path the session
// itself has (bridgeRefusalResult).
type policyRefusalError struct {
	msg    string
	bridge string
}

func (e *policyRefusalError) Error() string { return e.msg }

// policyObstacle is a rule that says a verb may not act: text for every caller,
// bridge for a bridged one ("refused: " included). An empty bridge is no rule (an
// I/O failure) and stays a plain error.
type policyObstacle struct {
	text, bridge string
}

// err is the obstacle as an error.
func (o *policyObstacle) err() error {
	if o.bridge == "" {
		return errors.New(o.text)
	}
	return &policyRefusalError{msg: o.text, bridge: o.bridge}
}

// bridgedCall says the call arrived on the bridge.
func bridgedCall(ctx context.Context) bool {
	o, ok := confinement.OriginFrom(ctx)
	return ok && o.Bridged()
}

// bridgeRefusalResult is the tool result for err: for a bridged call, a policy
// refusal is its contract text (the session service turns it into -32001 and
// counts it refused); every other error, and every error of a call that did not
// arrive on the bridge, is err's own text, unchanged.
func bridgeRefusalResult(ctx context.Context, err error) *command.Result {
	var pe *policyRefusalError
	if bridgedCall(ctx) && errors.As(err, &pe) {
		return command.TextErrorResult(pe.bridge)
	}
	return command.TextErrorResult(err.Error())
}

// bridgeGuardTarget is guardHandleTarget for the session a bridged call acts on:
// an implicit or closed session is a rule saying no.
func bridgeGuardTarget(st *session.State, verb string) error {
	err := guardHandleTarget(st, verb)
	if err == nil {
		return nil
	}
	return &policyRefusalError{
		msg:    err.Error(),
		bridge: bridgeRefusalPrefix + "this session is a main-checkout (implicit) session or is closed, so there is nothing to " + verb + "; the operator at a terminal outside the unit can see its state with `sc list --closed`",
	}
}
