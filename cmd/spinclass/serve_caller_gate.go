package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// bridgeRefusalPrefix opens every refusal the bridge contract defines
// (spinclass-bridge(7), "Refusals"); the session
// service turns a tool result that opens with it into JSON-RPC error -32001.
const bridgeRefusalPrefix = "refused: "

// bridgeRefusalText is the refusal for a verb that has no named right a
// session inside its unit could be granted: it is run by the session's holder or
// the operator, at a terminal outside the unit (a session inside its unit has
// no channel to ask anyone). The session verbs have their own text
// (bridgeSessionRefusals); the rest share this one.
func bridgeRefusalText(tool, operatorCommand string) string {
	if text, ok := bridgeSessionRefusals[tool]; ok {
		return text
	}
	return fmt.Sprintf("%s%s is not available to a session inside its unit; tell the session's holder or the operator, who run `%s` at a terminal outside the unit",
		bridgeRefusalPrefix, tool, operatorCommand)
}

// serveCallerConfined classifies this serve's caller the way the grit tools do
// (ClassifyServeCaller) and fails closed: an unresolvable state directory or
// principal counts as confined. It is a variable so tests can stand in for the
// frontend state.
//
// What it classifies is this serve's own principal (CLOWN_SESSION_ID), which
// is the worker's when the worker's tools reach its own frontend serve. It
// cannot see a process that dials some other session's serve over loopback:
// that serve classifies itself, as unconfined. Only per-session serve sockets
// (F12, clown's) close that (FDR 0035, "Serve handlers and the caller").
var serveCallerConfined = classifyServeCaller

func classifyServeCaller() (bool, string) {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return true, fmt.Sprintf("state directory unresolved: %v", err)
	}
	return confinement.ClassifyServeCaller(confinement.Host{}, stateDir, os.Getenv("CLOWN_SESSION_ID"), os.Getpid())
}

// ungatedCLI adapts a tool body to RunCLI, the terminal path, which is not the
// serve and so skips refuseConfinedServeCaller: a terminal has no serve
// principal and would be refused as unclassifiable.
func ungatedCLI[P any](run func(P) (string, error)) func(context.Context, json.RawMessage) error {
	return func(_ context.Context, args json.RawMessage) error {
		var p P
		if len(args) > 0 {
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("invalid arguments: %w", err)
			}
		}
		text, err := run(p)
		if err != nil {
			return err
		}
		fmt.Println(text)
		return nil
	}
}

// confinedCall says the caller of this call classifies as confined, on any
// transport: a call that arrived on the bridge, or a call with no origin
// (stdio, or clown's loopback relay to the older serve socket) from a serve
// whose principal a unit record names. It fails closed like serveCallerConfined.
//
// The always-ask floor lives only in the hook that runs inside the unit, so a
// verb whose escalation arguments (force, fresh_env, posture) must never rest on
// it asks this instead of bridgedCall: the older serve route is a second way a
// process inside the unit reaches the same handler and skips that hook.
func confinedCall(ctx context.Context) bool {
	confined, _ := confinedCallWhy(ctx)
	return confined
}

// confinedCallWhy is confinedCall with the classifier's reason, for a refusal
// that must say why a call without an origin counted as confined (B-L1): an
// unconfined session whose serve cannot be classified (no serve record, two live
// serves of the principal, an unresolvable state directory) is refused by the
// same rule as the gated tools on master, and has to be told the true cause, not
// that it is inside a unit.
func confinedCallWhy(ctx context.Context) (bool, string) {
	return confinement.ClassifyOrigin(ctx, serveCallerConfined)
}

// restartEscalationRefusal is the refusal of force, fresh_env or posture for a
// confined caller. Over the bridge it is the contract's text. On the older serve
// route (no origin) it names the classifier's reason, the principal and the real
// commands, in the form of refuseConfinedServeCaller; it does not claim the
// caller is inside a unit, because the classifier fails closed on evidence it
// cannot read, and `--fresh-env` is not a flag of any command.
func restartEscalationRefusal(ctx context.Context, why string) string {
	if bridgedCall(ctx) {
		return bridgeRestartEscalationRefusal
	}
	principal := os.Getenv("CLOWN_SESSION_ID")
	if principal == "" {
		principal = "(none)"
	}
	return fmt.Sprintf(
		"restart-this-session with force or posture refused: this serve's caller (principal %s) classifies as confined (%s). A confined session holds no right to it; a holder with the right \"restart\" on this session, or the operator, runs `sc restart-child-session <session>` with --force or --posture at a terminal outside the unit (--force is always asked of the human).",
		principal, why)
}

// refuseConfinedServeCaller returns the refusal for tool when this serve's
// caller classifies as confined, or nil to proceed. Call it before reading any
// parameter. The refusal says what the caller holds (no right to the tool) and
// who holds it: the operator, through operatorCommand at a terminal outside
// the unit (EPIC 0001 principle 9).
//
// ctx carries the call's origin when a transport established one: a call that
// arrived on the bridge is confined whatever this serve's own classification
// says, and is refused in the bridge contract's text (bridgeRefusalText). A
// ctx with no origin (every stdio call) classifies as before, byte for byte.
func refuseConfinedServeCaller(ctx context.Context, tool, operatorCommand string) *command.Result {
	confined, why := confinement.ClassifyOrigin(ctx, serveCallerConfined)
	if !confined {
		return nil
	}
	if o, ok := confinement.OriginFrom(ctx); ok && o.Bridged() {
		return command.TextErrorResult(bridgeRefusalText(tool, operatorCommand))
	}
	principal := os.Getenv("CLOWN_SESSION_ID")
	if principal == "" {
		principal = "(none)"
	}
	return command.TextErrorResult(fmt.Sprintf(
		"%s refused: this serve's caller (principal %s) classifies as confined (%s). A confined session holds no right to %s; the operator holds it, by running `%s` at a terminal outside the unit.",
		tool, principal, why, tool, operatorCommand))
}
