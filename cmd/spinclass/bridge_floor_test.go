package main

// The floor on the outside (bridge batch F2). The always-ask floor lives only in
// the hook that runs inside the unit, so a call that arrives on the bridge never
// rests on it: an escalation argument (force, fresh_env, posture) is refused by
// the verb itself, in the contract's text.

import (
	"encoding/json"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
)

// The text of an escalation refused over the bridge: who can do it, and how.
const refusalRestartEscalation = "refused: restart-this-session with force, fresh_env or posture is not available to a session inside its unit; a holder with the right \"restart\" on this session, or the operator at a terminal outside the unit, runs `sc restart-child-session <session>` with --force or --posture (--force is always asked of the human); no command offers fresh_env"

func TestBridgedRestartRefusesEveryEscalation(t *testing.T) {
	for _, args := range []string{
		`{"force":true}`,
		`{"fresh_env":true}`,
		`{"posture":"attended"}`,
		`{"posture":"unattended"}`,
		`{"posture":"keep"}`,
		`{"force":true,"reason":"ATTACKER-bytes"}`,
	} {
		t.Run(args, func(t *testing.T) {
			f := newLiarFixture(t)
			beforeA := f.a.fingerprint(t)
			text, isErr := runHandler(handleRestartThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(args))
			if !isErr || text != refusalRestartEscalation {
				t.Errorf("got isErr=%v %q\nwant   %q", isErr, text, refusalRestartEscalation)
			}
			if len(*f.handoff) != 0 {
				t.Errorf("the hand-off ran: %+v", *f.handoff)
			}
			if got := f.a.fingerprint(t); got != beforeA {
				t.Errorf("A's record changed:\nbefore %s\nafter  %s", beforeA, got)
			}
			if rr := f.a.record(t).RestartRequest; rr != nil {
				t.Errorf("a restart was queued on A: %+v", rr)
			}
		})
	}
}

// A plain restart, with and without a reason, still succeeds positively.
func TestBridgedPlainRestartStillSucceeds(t *testing.T) {
	for _, args := range []string{`{}`, `{"reason":"rebuild"}`, `{"force":false}`} {
		f := newLiarFixture(t)
		text, isErr := runHandler(handleRestartThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(args))
		if isErr || !strings.Contains(text, "restart queued for "+f.a.key) {
			t.Errorf("%s: got isErr=%v %q", args, isErr, text)
		}
		if len(*f.handoff) != 1 {
			t.Errorf("%s: hand-off calls = %+v, want one", args, *f.handoff)
		}
	}
}

// The same arguments over stdio (no origin) are what they were: a forced restart
// is queued (the always-ask floor is the hook's).
func TestStdioRestartWithForceIsUnchanged(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, wtPath := restartSelfFixture(t, true)
	writeRunningJob(t, wtPath)

	text, isErr := callRestartThis(t, `{"force":true,"reason":"wedged"}`)
	if isErr || !strings.Contains(text, "FORCED") {
		t.Fatalf("want a forced restart queued, got isErr=%v %q", isErr, text)
	}
	if req := readKid(t, repoPath).RestartRequest; req == nil || req.When != "now" {
		t.Errorf("restart request = %+v, want when=now", req)
	}
	if len(*calls) != 1 {
		t.Errorf("hand-off calls = %+v, want one", *calls)
	}
}

// close-this-session takes no force argument; over the bridge a force argument
// of any value is a refusal naming who can discard work.
func TestBridgedCloseForceIsRefused(t *testing.T) {
	for _, args := range []string{`{"force":true}`, `{"force":false}`, `{"force":"true"}`} {
		f := newLiarFixture(t)
		text, isErr := runHandler(handleCloseThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(args))
		if !isErr || !strings.HasPrefix(text, "refused: ") || !strings.Contains(text, "outside the unit") || !strings.Contains(text, "sc close") {
			t.Errorf("%s: got isErr=%v %q, want a refusal naming the operator's terminal", args, isErr, text)
		}
		if f.a.record(t).CloseRequest != nil {
			t.Errorf("%s: a close was queued", args)
		}
	}
}

// The wire: the escalation is -32001 and counted as refused, with no hand-off
// configured at all (the refusal never depends on a setting).
func TestE2EBridgedRestartEscalationIsRefusedOnTheWire(t *testing.T) {
	b := startE2E(t, e2eOpts{})
	p := b.open("session")
	p.initialize(protocol.ProtocolVersionV1)
	for _, args := range []map[string]any{{"force": true}, {"fresh_env": true}, {"posture": "attended"}} {
		rep := p.call("tools/call", map[string]any{"name": "restart-this-session", "arguments": args})
		if rep.Error == nil || rep.Error.Code != -32001 || rep.Error.Message != refusalRestartEscalation {
			t.Errorf("%v: got %+v, want -32001 %q", args, rep.Error, refusalRestartEscalation)
		}
	}
	b.waitMetric("spinclass.bridge.session.tool.restart_this_session.refused:1|c")
}
