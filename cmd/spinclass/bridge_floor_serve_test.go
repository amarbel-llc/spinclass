package main

// The floor on the outside, on BOTH doors into a unit (J-M1) and a session's
// own cancel of a restart another party queued (S-M1).
//
// The always-ask floor lives only in the hook that runs inside the unit, so a
// process in the unit that speaks MCP to the older serve socket (clown's relay
// port) skips it. The verbs therefore refuse the escalation arguments for any
// caller that classifies as confined, whatever the transport; the bridge is one
// way a call arrives, the older serve route is the other.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// withServeClassifier makes this serve classify its caller as confined or not
// for the test (the gated tools' classifier and the verbs' evidence classifier
// both), restoring the stubs TestMain installs. The fault-state tests of
// bridge_evidence_test.go do not use it: they put the real classifier back.
func withServeClassifier(t *testing.T, confined bool) {
	t.Helper()
	original, originalEvidence := serveCallerConfined, serveCallerEvidence
	t.Cleanup(func() { serveCallerConfined, serveCallerEvidence = original, originalEvidence })
	serveCallerConfined = func() (bool, string) {
		if confined {
			return true, "unit record names the principal"
		}
		return false, "unconfined"
	}
	serveCallerEvidence = func() (confinement.Evidence, string) {
		if confined {
			return confinement.EvidenceConfined, "unit record names the principal"
		}
		return confinement.EvidenceUnconfined, "unconfined"
	}
}

// A call with NO origin (the older serve route: stdio or clown's loopback
// relay) from a caller the serve classifies as confined gets the same refusal
// the bridge gives, before anything is queued.
func TestOlderServeRouteRefusesEveryEscalationForAConfinedCaller(t *testing.T) {
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
			withServeClassifier(t, true)
			beforeB := f.b.fingerprint(t)
			text, isErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(args))
			// The older route has no origin, so it does not claim the caller is
			// inside a unit (B-L1): it names the classifier's reason.
			if !isErr || !strings.Contains(text, "classifies as confined (unit record names the principal)") || strings.Contains(text, "inside its unit") || strings.Contains(text, "--fresh-env") {
				t.Errorf("got isErr=%v %q\nwant the classifier's reason, no 'inside its unit', no --fresh-env", isErr, text)
			}
			if len(*f.handoff) != 0 {
				t.Errorf("the hand-off ran: %+v", *f.handoff)
			}
			if got := f.b.fingerprint(t); got != beforeB {
				t.Errorf("the session's record changed:\nbefore %s\nafter  %s", beforeB, got)
			}
			if rr := f.b.record(t).RestartRequest; rr != nil {
				t.Errorf("a restart was queued: %+v", rr)
			}
		})
	}
}

// A plain restart over the older route still works for a confined caller (the
// floor is for escalation only), and an UNCONFINED caller is untouched: force
// goes on to the verb and the always-ask hook, as before.
func TestOlderServeRouteStillQueuesAPlainRestartAndLeavesUnconfinedForceAlone(t *testing.T) {
	t.Run("confined plain restart", func(t *testing.T) {
		f := newLiarFixture(t)
		withServeClassifier(t, true)
		text, isErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"reason":"rebuild"}`))
		if isErr || !strings.Contains(text, "restart queued for "+f.b.key) {
			t.Fatalf("got isErr=%v %q", isErr, text)
		}
		if len(*f.handoff) != 1 {
			t.Errorf("hand-off calls = %+v, want one", *f.handoff)
		}
	})
	t.Run("unconfined force", func(t *testing.T) {
		f := newLiarFixture(t)
		withServeClassifier(t, false)
		text, isErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"force":true,"reason":"wedged"}`))
		if text == refusalRestartEscalation {
			t.Fatalf("an unconfined caller was refused the escalation: %q", text)
		}
		if isErr {
			t.Fatalf("unconfined force failed: %q", text)
		}
		if rr := f.b.record(t).RestartRequest; rr == nil || rr.When != "now" {
			t.Errorf("restart request = %+v, want a forced one (when=now)", rr)
		}
	})
}

// A caller whose evidence EXISTS and cannot be read is refused force, posture and
// the displacement of another party's request (clarabell's proviso: evidence
// exists, its content is unknown), and the refusal says WHY it counts as
// confined, never that it is inside a unit (B-L1). The evidence-absent cases (no
// serve record, an unresolvable state directory) are the fault-state tests of
// bridge_evidence_test.go: they behave as on master. With the classifier saying
// unconfined, nothing changes from master.
func TestUnreadableEvidenceIsRefusedWithTheClassifiersReasonNotAClaimItIsInsideAUnit(t *testing.T) {
	f := newLiarFixture(t)
	original := serveCallerEvidence
	t.Cleanup(func() { serveCallerEvidence = original })
	serveCallerEvidence = func() (confinement.Evidence, string) {
		return confinement.EvidenceUnreadable, "unit record 1.json does not parse"
	}

	text, isErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"force":true}`))
	if !isErr || !strings.Contains(text, "unit record 1.json does not parse") || strings.Contains(text, "inside its unit") {
		t.Fatalf("got isErr=%v %q, want the classifier's reason and no claim of a unit", isErr, text)
	}
	if len(*f.handoff) != 0 {
		t.Errorf("the hand-off ran: %+v", *f.handoff)
	}

	// The same call over the bridge is the contract's text, unchanged.
	btext, bErr := runHandler(handleRestartThisSession)(bridgedCtxFor(f.b.key), json.RawMessage(`{"force":true}`))
	if !bErr || btext != refusalRestartEscalation {
		t.Errorf("bridged: got isErr=%v %q\nwant %q", bErr, btext, refusalRestartEscalation)
	}

	// Classified unconfined: the same call is not refused by the floor.
	serveCallerEvidence = func() (confinement.Evidence, string) { return confinement.EvidenceUnconfined, "unconfined" }
	utext, uErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"force":true,"reason":"wedged"}`))
	if uErr || strings.Contains(utext, "classifies as confined") {
		t.Errorf("unconfined: got isErr=%v %q, want it queued as on master", uErr, utext)
	}
}

// queueRestartFor writes a queued restart request, by requester, on s's record.
func queueRestartFor(t *testing.T, s stagedSession, requester string) {
	t.Helper()
	st := s.record(t)
	st.RestartRequest = &session.RestartRequest{
		RequestedAt: time.Now().UTC(), Requester: requester, Reason: "no longer trusted", When: "idle",
	}
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
}

// A confined session may cancel only a restart it queued itself. A request the
// operator or a holder queued is not its to erase: the cancel is refused, names
// who asked, leaves the request in place and tells nobody anything false.
func TestAConfinedSessionCannotCancelARestartAnotherPartyQueued(t *testing.T) {
	for name, requester := range map[string]string{
		"operator":   session.OperatorRestartRequester,
		"holder":     "spawner",
		"unnamed":    "",
		"another id": "0b2f6c1e-1111-2222-3333-444455556666",
	} {
		t.Run("bridge/"+name, func(t *testing.T) {
			notified := stubNotifyPrincipal(t)
			f := newLiarFixture(t)
			queueRestartFor(t, f.a, requester)
			text, isErr := runHandler(handleCancelRestart)(bridgedCtxFor(f.a.key), json.RawMessage(`{}`))
			if !isErr || !strings.HasPrefix(text, "refused: ") || !strings.Contains(text, "outside the unit") {
				t.Fatalf("got isErr=%v %q, want a contract refusal naming who can cancel", isErr, text)
			}
			if rr := f.a.record(t).RestartRequest; rr == nil || rr.Requester != requester {
				t.Errorf("the other party's request was touched: %+v", rr)
			}
			if len(*notified) != 0 {
				t.Errorf("notifications on a refused cancel: %+v", *notified)
			}
		})
		t.Run("older route/"+name, func(t *testing.T) {
			stubNotifyPrincipal(t)
			f := newLiarFixture(t)
			withServeClassifier(t, true)
			queueRestartFor(t, f.b, requester)
			text, isErr := runHandler(handleCancelRestart)(context.Background(), json.RawMessage(`{}`))
			if !isErr || !strings.HasPrefix(text, "refused: ") {
				t.Fatalf("got isErr=%v %q, want a refusal", isErr, text)
			}
			if rr := f.b.record(t).RestartRequest; rr == nil || rr.Requester != requester {
				t.Errorf("the other party's request was touched: %+v", rr)
			}
		})
	}
}

// Its own request it may still cancel, on either door; and an unconfined caller
// (the operator's terminal, or a serve that is not in a unit) cancels whatever is
// queued, as before.
func TestAConfinedSessionStillCancelsItsOwnRestartAndAnUnconfinedCallerAnyOne(t *testing.T) {
	t.Run("bridge, own request", func(t *testing.T) {
		stubNotifyPrincipal(t)
		f := newLiarFixture(t)
		queueRestartFor(t, f.a, f.a.principal)
		text, isErr := runHandler(handleCancelRestart)(bridgedCtxFor(f.a.key), json.RawMessage(`{}`))
		if isErr || !strings.Contains(text, f.a.key) {
			t.Fatalf("got isErr=%v %q, want a success naming the session", isErr, text)
		}
		if rr := f.a.record(t).RestartRequest; rr != nil {
			t.Errorf("the session's own request must be cleared: %+v", rr)
		}
	})
	t.Run("older route, own request", func(t *testing.T) {
		stubNotifyPrincipal(t)
		f := newLiarFixture(t)
		withServeClassifier(t, true)
		queueRestartFor(t, f.b, f.b.principal)
		text, isErr := runHandler(handleCancelRestart)(context.Background(), json.RawMessage(`{}`))
		if isErr {
			t.Fatalf("refused: %q", text)
		}
		if rr := f.b.record(t).RestartRequest; rr != nil {
			t.Errorf("the session's own request must be cleared: %+v", rr)
		}
	})
	t.Run("unconfined caller, the operator's request", func(t *testing.T) {
		stubNotifyPrincipal(t)
		f := newLiarFixture(t)
		withServeClassifier(t, false)
		queueRestartFor(t, f.b, session.OperatorRestartRequester)
		if text, isErr := runHandler(handleCancelRestart)(context.Background(), json.RawMessage(`{}`)); isErr {
			t.Fatalf("an unconfined caller was refused: %q", text)
		}
		if rr := f.b.record(t).RestartRequest; rr != nil {
			t.Errorf("an unconfined caller's cancel must clear the request: %+v", rr)
		}
	})
}
