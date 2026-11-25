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
// (docs/plans/2026-11-25-bridge-wire-v1.md, "Refusal text"); the session
// service turns a tool result that opens with it into JSON-RPC error -32001.
const bridgeRefusalPrefix = "refused: "

// bridgeRefusalText is the refusal for a verb that has no named right a
// session inside its unit could be granted: it is the operator's, at a
// terminal outside the unit.
func bridgeRefusalText(tool, operatorCommand string) string {
	return fmt.Sprintf("%s%s is not available to a session inside its unit; ask the operator (they hold it, by running `%s` at a terminal outside the unit)",
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
