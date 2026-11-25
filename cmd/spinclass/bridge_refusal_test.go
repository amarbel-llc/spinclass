package main

// The refusal text of each session verb over the bridge. The literals below
// are the table "Refusal texts" in docs/plans/2026-11-25-bridge-wire-v1.md,
// copied here on purpose: a change to the code's text that does not change
// the table (and this file) fails.

import (
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
)

const (
	refusalCancelRestart = "refused: cancel-restart naming another session needs the right \"restart\" on that session, which a session inside its unit may not use whatever it holds; a holder with the right outside the unit, or the operator running `sc cancel-restart` at a terminal outside the unit, can cancel it (cancel-restart with no child cancels this session's own)"
	refusalListAccept    = "refused: list-handles with accept has no right a session inside its unit can be granted; the operator runs `sc list-handles --accept` at a terminal outside the unit (a plain list-handles is allowed)"
	refusalRestartSelf   = "refused: restart-this-session needs the trusted [session-entry].restart hand-off; the operator sets it in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)"
	refusalCloseSelf     = "refused: close-this-session needs the trusted [permissions].allow-self-close = true; the operator sets it in the global sweatfile or one above the repo (a repo or worktree sweatfile is ignored)"
)

// Each verb that is refused over the bridge is refused with -32001 and exactly
// its table text, and the text carries no byte of the request's arguments. The
// verbs a session may use on itself are not refused at all. It fails if a text
// drifts from the table, if a refusal comes back as a tool result instead of
// -32001, or if an argument value is echoed.
func TestE2ERefusalTextOfEachSessionVerb(t *testing.T) {
	// The fixture's session is a real record with no restart hand-off and no
	// allow-self-close.
	b := startE2E(t, e2eOpts{})
	p := b.open("session")
	p.initialize(protocol.ProtocolVersionV1)

	const attacker = "ATTACKER-bytes-7f3a"
	refused := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"cancel-restart", map[string]any{"child": attacker}, refusalCancelRestart},
		{"list-handles", map[string]any{"accept": true, "principal": attacker}, refusalListAccept},
		{"restart-this-session", map[string]any{"reason": attacker}, refusalRestartSelf},
		{"close-this-session", map[string]any{"note": attacker}, refusalCloseSelf},
	}
	for _, c := range refused {
		t.Run(c.tool, func(t *testing.T) {
			rep := p.call("tools/call", map[string]any{"name": c.tool, "arguments": c.args})
			if rep.Error == nil {
				t.Fatalf("want a refusal, got result %s", rep.Result)
			}
			if rep.Error.Code != -32001 || rep.Error.Message != c.want {
				t.Errorf("got code %d %q\nwant -32001 %q", rep.Error.Code, rep.Error.Message, c.want)
			}
			if strings.Contains(rep.Error.Message, attacker) || strings.ContainsAny(rep.Error.Message, "\r\n") {
				t.Errorf("the refusal echoes an argument or spans lines: %q", rep.Error.Message)
			}
		})
	}

	// The verbs a session may use on itself are allowed over the bridge.
	for tool, args := range map[string]map[string]any{
		"whoami":         {"principal": attacker},
		"list-handles":   {},
		"cancel-restart": {},
	} {
		rep := p.call("tools/call", map[string]any{"name": tool, "arguments": args})
		if rep.Error != nil {
			t.Errorf("%s on this session was refused: %+v", tool, rep.Error)
		}
	}
}

// Outside the bridge the setting-absent texts are what they were: no
// "refused: " prefix, and the same words.
func TestSettingAbsentTextsAreUnchangedOffTheBridge(t *testing.T) {
	selfCloseFixture(t, false)
	text, isErr := callCloseThis(t, `{}`)
	if !isErr || strings.HasPrefix(text, "refused: ") || !strings.Contains(text, "is not allowed to close itself: an operator must set `allow-self-close = true` under [permissions]") {
		t.Errorf("close-this-session off the bridge: %v %q", isErr, text)
	}
	text, isErr = callRestartThis(t, `{}`)
	if !isErr || strings.HasPrefix(text, "refused: ") || !strings.Contains(text, "cannot be restarted: no restart hand-off is configured; an operator must set `restart` under [session-entry]") {
		t.Errorf("restart-this-session off the bridge: %v %q", isErr, text)
	}
}
