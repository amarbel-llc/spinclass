package main

// Bridge cut round 1, fixes B-M1, A-M1 and A-M2.
//
// B-M1: the terminal's own form of cancel-restart must not be classified
// confined by default. A-M1: a confined session may not replace a restart or a
// stop another party queued. A-M2: the reason a confined session gives is
// bounded, cleaned and marked before it reaches the record, the hand-off argv or
// a wake. Bytes that must not be written as literals are written as escapes.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

const (
	bidiOverride = "\xe2\x80\xae" // U+202E
	lineSep      = "\xe2\x80\xa8" // U+2028
)

// queueRequestFor writes a queued request of the given kind (restart when kind
// is empty), by requester, on s's record.
func queueRequestFor(t *testing.T, s stagedSession, requester, kind string) {
	t.Helper()
	st := s.record(t)
	st.RestartRequest = &session.RestartRequest{
		RequestedAt: time.Now().UTC(), Requester: requester, Reason: "no longer trusted", When: "now", Kind: kind,
	}
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
}

// B-M1: the CLI process has no serve record, so the serve classifier answers
// "confined" for it whoever it is. The terminal's form must not ask that
// classifier: an operator's tooling cancels another party's restart, as on
// master.
func TestTheTerminalsCancelRestartIsNotClassifiedConfinedByDefault(t *testing.T) {
	stubNotifyPrincipal(t)
	f := newLiarFixture(t)
	withServeClassifier(t, true) // what a CLI process would be told
	queueRestartFor(t, f.b, session.OperatorRestartRequester)
	text, err := runCancelRestart(cancelRestartParams{})
	if err != nil {
		t.Fatalf("the terminal's own cancel was refused: %v", err)
	}
	if !strings.Contains(text, f.b.key) {
		t.Errorf("text %q does not name the session", text)
	}
	if rr := f.b.record(t).RestartRequest; rr != nil {
		t.Errorf("the request must be cleared: %+v", rr)
	}
}

// A-M1: the displacement is refused on every door, for a restart and for a stop,
// whoever queued the request; the record is as it was and nothing ran.
func TestAConfinedSessionCannotReplaceARequestAnotherPartyQueued(t *testing.T) {
	cases := map[string]struct{ requester, kind, who string }{
		"operator restart": {session.OperatorRestartRequester, "", "the operator"},
		"holder stop":      {"spawner", session.RequestKindStop, "another principal that holds this session"},
		"unnamed restart":  {"", "", "a requester the record does not name"},
	}
	for name, tc := range cases {
		check := func(t *testing.T, f liarFixture, s stagedSession, text string, isErr bool) {
			t.Helper()
			if !isErr || !strings.HasPrefix(text, "refused: ") || !strings.Contains(text, tc.who) || !strings.Contains(text, "ask them or wait") {
				t.Fatalf("got isErr=%v %q, want a refusal naming %q", isErr, text, tc.who)
			}
			rr := s.record(t).RestartRequest
			if rr == nil || rr.Requester != tc.requester || rr.Kind != tc.kind || rr.When != "now" || rr.Reason != "no longer trusted" {
				t.Errorf("the other party's request was touched: %+v", rr)
			}
			if len(*f.handoff) != 0 {
				t.Errorf("the hand-off ran: %+v", *f.handoff)
			}
		}
		t.Run("bridge restart/"+name, func(t *testing.T) {
			f := newLiarFixture(t)
			queueRequestFor(t, f.a, tc.requester, tc.kind)
			text, isErr := runHandler(handleRestartThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(`{"reason":"mine"}`))
			check(t, f, f.a, text, isErr)
		})
		t.Run("older route restart/"+name, func(t *testing.T) {
			f := newLiarFixture(t)
			withServeClassifier(t, true)
			queueRequestFor(t, f.b, tc.requester, tc.kind)
			text, isErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"reason":"mine"}`))
			check(t, f, f.b, text, isErr)
		})
		t.Run("older route stop/"+name, func(t *testing.T) {
			f := newLiarFixture(t)
			withServeClassifier(t, true)
			queueRequestFor(t, f.b, tc.requester, tc.kind)
			text, isErr := runHandler(handleStopThisSession)(context.Background(), json.RawMessage(`{"reason":"mine"}`))
			check(t, f, f.b, text, isErr)
		})
	}
}

// What A-M1 does not change: a confined session replaces its OWN request, and an
// unconfined caller overwrites whatever is queued, as on master.
func TestAConfinedSessionStillReplacesItsOwnRequestAndAnUnconfinedCallerAnyOne(t *testing.T) {
	t.Run("confined, own request", func(t *testing.T) {
		f := newLiarFixture(t)
		withServeClassifier(t, true)
		queueRequestFor(t, f.b, f.b.principal, "")
		text, isErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"reason":"again"}`))
		if isErr {
			t.Fatalf("refused: %q", text)
		}
		if rr := f.b.record(t).RestartRequest; rr == nil || !strings.HasSuffix(rr.Reason, "again") {
			t.Errorf("request = %+v, want the new one", rr)
		}
	})
	t.Run("unconfined, the operator's request", func(t *testing.T) {
		f := newLiarFixture(t)
		withServeClassifier(t, false)
		queueRequestFor(t, f.b, session.OperatorRestartRequester, "")
		text, isErr := runHandler(handleRestartThisSession)(context.Background(), json.RawMessage(`{"reason":"mine"}`))
		if isErr {
			t.Fatalf("an unconfined caller was refused: %q", text)
		}
		rr := f.b.record(t).RestartRequest
		if rr == nil || rr.Requester != f.b.principal || rr.Reason != "mine" {
			t.Errorf("request = %+v, want the session's own, unmarked", rr)
		}
	})
}

// rawByte reports a control byte, a bidirectional override or a line separator
// in s: anything that must reach no terminal, argv or wake as itself.
func rawByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return strings.Contains(s, bidiOverride) || strings.Contains(s, lineSep)
}

// A-M2: what the agent wrote is data. Nothing raw reaches the record, the
// hand-off argv, or (the wakes are built from the record) a holder.
func TestAConfinedReasonIsBoundedCleanedAndMarkedWhereverItLeaves(t *testing.T) {
	hostile := "x\x1b[31mred\nnext\x00z" + bidiOverride + "evil" + lineSep + strings.Repeat("a", 300)
	args, _ := json.Marshal(map[string]string{"reason": hostile})
	type door struct {
		name   string
		bridge bool
		call   func(ctx context.Context) (string, bool)
	}
	for _, d := range []door{
		{"bridge restart", true, func(ctx context.Context) (string, bool) {
			return runHandler(handleRestartThisSession)(ctx, args)
		}},
		{"older route restart", false, func(ctx context.Context) (string, bool) {
			return runHandler(handleRestartThisSession)(ctx, args)
		}},
		{"older route stop", false, func(ctx context.Context) (string, bool) {
			return runHandler(handleStopThisSession)(ctx, args)
		}},
	} {
		t.Run(d.name, func(t *testing.T) {
			f := newLiarFixture(t)
			ctx, target := context.Background(), f.b
			if d.bridge {
				ctx, target = bridgedCtxFor(f.a.key), f.a
			} else {
				withServeClassifier(t, true)
			}
			text, isErr := d.call(ctx)
			if isErr {
				t.Fatalf("refused: %q", text)
			}
			rr := target.record(t).RestartRequest
			if rr == nil {
				t.Fatal("nothing was queued")
			}
			if !strings.HasPrefix(rr.Reason, confinedReasonMark) {
				t.Errorf("reason %q is not marked", rr.Reason)
			}
			if rawByte(rr.Reason) || len(rr.Reason) > len(confinedReasonMark)+maxConfinedReasonBytes*4+3 {
				t.Errorf("the record holds a raw or unbounded reason (%d bytes): %q", len(rr.Reason), rr.Reason)
			}
			if len(*f.handoff) != 1 {
				t.Fatalf("hand-off calls = %+v, want one", *f.handoff)
			}
			for _, a := range (*f.handoff)[0].argv {
				if rawByte(a) {
					t.Errorf("a raw byte reached the hand-off argv: %q", a)
				}
			}
			if rawByte(text) {
				t.Errorf("a raw byte reached the answer: %q", text)
			}
		})
	}
}

// An unconfined caller's reason is left exactly as given.
func TestAnUnconfinedReasonIsLeftAsGiven(t *testing.T) {
	f := newLiarFixture(t)
	withServeClassifier(t, false)
	reason := "line one\nline two " + strings.Repeat("b", 300)
	args, _ := json.Marshal(map[string]string{"reason": reason})
	if text, isErr := runHandler(handleRestartThisSession)(context.Background(), args); isErr {
		t.Fatalf("refused: %q", text)
	}
	if rr := f.b.record(t).RestartRequest; rr == nil || rr.Reason != reason {
		t.Errorf("reason = %+v, want it untouched", rr)
	}
}

func TestCleanConfinedReasonBoundsMarksAndEscapes(t *testing.T) {
	long := cleanConfinedReason(strings.Repeat("a", 300))
	if want := confinedReasonMark + strings.Repeat("a", maxConfinedReasonBytes) + "..."; long != want {
		t.Errorf("a 300-byte reason = %q, want %d bytes then an ellipsis", long, maxConfinedReasonBytes)
	}
	// A multi-byte rune straddling the bound is dropped whole, never split.
	straddle := cleanConfinedReason(strings.Repeat("a", maxConfinedReasonBytes-1) + "\xc3\xa9" + "tail")
	if want := confinedReasonMark + strings.Repeat("a", maxConfinedReasonBytes-1) + "..."; straddle != want {
		t.Errorf("straddling rune: %q", straddle)
	}
	if got := cleanConfinedReason("ok"); got != confinedReasonMark+"ok" {
		t.Errorf("plain reason = %q", got)
	}
	escaped := cleanConfinedReason("a\x1bb\nc\x00d\xffe" + bidiOverride + lineSep)
	wants := []string{`\x1b`, `\n`, `\x00`, `\xff`, backslash + "u202e", backslash + "u2028"}
	for _, want := range wants {
		if !strings.Contains(escaped, want) {
			t.Errorf("%q does not show %s as an escape", escaped, want)
		}
	}
	if rawByte(escaped) {
		t.Errorf("a raw byte is left in %q", escaped)
	}
}
