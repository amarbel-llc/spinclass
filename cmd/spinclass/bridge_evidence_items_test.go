package main

// Round-2 items on the floor of the session verbs: the terminal forms (a), a
// stopped session (c), what a confined caller may clear or stamp (d), stop with
// when=now (e), and the recovery text (f). The quoting of the reason (b) is
// pinned in bridge_r1fix_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

func withCLIInsideUnit(t *testing.T, inside bool) {
	t.Helper()
	original := cliInsideUnit
	t.Cleanup(func() { cliInsideUnit = original })
	cliInsideUnit = func() bool { return inside }
}

// (a) The terminal's own forms are confined exactly when their process runs inside
// an agent unit (the one host-side fact they have), and unchanged outside one.
func TestTerminalFormsAreConfinedOnlyInsideAnAgentUnit(t *testing.T) {
	for _, inside := range []bool{true, false} {
		t.Run("cancel-restart", func(t *testing.T) {
			stubNotifyPrincipal(t)
			f := newLiarFixture(t)
			withCLIInsideUnit(t, inside)
			queueRestartFor(t, f.b, "9c1d7e44-0000-4000-8000-000000000001")
			_, err := runCancelRestart(cancelRestartParams{})
			if inside != (err != nil) {
				t.Errorf("inside=%v: err=%v", inside, err)
			}
			if rr := f.b.record(t).RestartRequest; inside != (rr != nil) {
				t.Errorf("inside=%v: request left = %+v", inside, rr)
			}
		})
		t.Run("restart-this-session", func(t *testing.T) {
			stubNotifyPrincipal(t)
			f := newLiarFixture(t)
			withCLIInsideUnit(t, inside)
			queueRestartFor(t, f.b, "9c1d7e44-0000-4000-8000-000000000001")
			_, err := runRestartThisSession(restartThisSessionParams{Reason: "mine"})
			var refusal *policyRefusalError
			if inside != errors.As(err, &refusal) {
				t.Errorf("inside=%v: err=%v", inside, err)
			}
		})
		t.Run("stop-this-session now", func(t *testing.T) {
			stubNotifyPrincipal(t)
			f := newLiarFixture(t)
			withCLIInsideUnit(t, inside)
			_, err := runStopThisSession(stopThisSessionParams{When: "now"})
			if inside != (err != nil && strings.Contains(err.Error(), "stop-now")) {
				t.Errorf("inside=%v: err=%v", inside, err)
			}
			_ = f
		})
	}
}

// (c) A confined caller is refused a restart of a STOPPED session; an unconfined one is not.
func TestAConfinedCallerCannotRestartAStoppedSession(t *testing.T) {
	for _, confined := range []bool{true, false} {
		f := newLiarFixture(t)
		withServeClassifier(t, confined)
		st := f.b.record(t)
		now := time.Now().UTC()
		st.StoppedAt, st.StoppedBy, st.StoppedReason = &now, session.OperatorRestartRequester, "operator stop"
		if err := session.Write(*st); err != nil {
			t.Fatal(err)
		}
		text, isErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"reason":"back"}`))
		refused := isErr && strings.Contains(text, "cannot act on a stopped session")
		if refused != confined {
			t.Errorf("confined=%v: isErr=%v %q", confined, isErr, text)
		}
		if confined && (!strings.Contains(text, "the operator") || !strings.Contains(text, "sc start-stopped")) {
			t.Errorf("the refusal does not name who can start it: %q", text)
		}
		if confined && f.b.record(t).RestartRequest != nil {
			t.Errorf("a request was queued on a stopped session")
		}
	}
}

// (d) A failed hand-off clears only the request its caller queued, when that
// caller is confined: another party's request that replaced it meanwhile stays.
func TestAFailedHandoffDoesNotClearAnotherPartysRequestForAConfinedCaller(t *testing.T) {
	for _, tool := range []struct {
		name string
		run  func() (string, bool)
	}{
		{"restart", func() (string, bool) {
			return runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"reason":"mine"}`))
		}},
		{"stop", func() (string, bool) {
			return runHandler(handleStopThisSession)(context.Background(), json.RawMessage(`{"reason":"mine"}`))
		}},
	} {
		for _, confined := range []bool{true, false} {
			t.Run(tool.name, func(t *testing.T) {
				f := newLiarFixture(t)
				withServeClassifier(t, confined)
				original := restartHandoffFn
				t.Cleanup(func() { restartHandoffFn = original })
				restartHandoffFn = func(string, []string) (string, error) {
					// Between the record and the hand-off's failure another party queues its own.
					queueRestartFor(t, f.b, session.OperatorRestartRequester)
					return "", errors.New("hand-off down")
				}
				tool.run()
				rr := f.b.record(t).RestartRequest
				if confined && (rr == nil || rr.Requester != session.OperatorRestartRequester) {
					t.Errorf("a confined caller cleared another party's request: %+v", rr)
				}
				if !confined && rr != nil {
					t.Errorf("an unconfined caller's failed hand-off must clear as before: %+v", rr)
				}
			})
		}
	}
}

// (d) A confined caller may not stamp cancelled a restart the preflight already
// approved for another party; an unconfined caller can (and gets "too late").
func TestAConfinedCallerCannotStampAnotherPartysInFlightRestartCancelled(t *testing.T) {
	for _, confined := range []bool{true, false} {
		stubNotifyPrincipal(t)
		f := newLiarFixture(t)
		withServeClassifier(t, confined)
		st := f.b.record(t)
		now := time.Now().UTC()
		st.RestartingAt, st.RestartingBy, st.RestartingReason = &now, session.OperatorRestartRequester, "operator restart"
		if err := session.Write(*st); err != nil {
			t.Fatal(err)
		}
		text, isErr := runHandler(handleCancelRestart)(context.Background(), json.RawMessage(`{}`))
		stamped := f.b.record(t).RestartCancelledAt != nil
		if confined && (stamped || !isErr || !strings.HasPrefix(text, "refused: ")) {
			t.Errorf("confined: stamped=%v isErr=%v %q", stamped, isErr, text)
		}
		if !confined && !stamped {
			t.Errorf("unconfined: the cancel was not stamped (%q)", text)
		}
	}
}

// (e) A confined caller's stop with when=now is refused and names who can do it.
func TestAConfinedStopNowIsRefused(t *testing.T) {
	for _, confined := range []bool{true, false} {
		f := newLiarFixture(t)
		withServeClassifier(t, confined)
		text, isErr := runHandler(handleStopThisSession)(context.Background(), json.RawMessage(`{"when":"now"}`))
		refused := strings.Contains(text, "when=now is not available")
		if refused != confined {
			t.Errorf("confined=%v: isErr=%v %q", confined, isErr, text)
		}
		if confined && (!strings.Contains(text, `"stop-now"`) || !strings.Contains(text, "sc stop <session> --now")) {
			t.Errorf("the refusal does not name who can: %q", text)
		}
		if confined && f.b.record(t).RestartRequest != nil {
			t.Errorf("a stop was queued")
		}
	}
}

// (f) The recovery text offers only commands that exist.
func TestTheEscalationRefusalNamesNoFlagThatDoesNotExist(t *testing.T) {
	if strings.Contains(bridgeRestartEscalationRefusal, "--fresh-env") {
		t.Errorf("the refusal names --fresh-env: %q", bridgeRestartEscalationRefusal)
	}
}
