package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

const bridgedSessionKey = "bridged-repo/bridged-branch"

// runHandler flattens a tool handler to its text and error flag.
func runHandler(h func(context.Context, json.RawMessage, command.Prompter) (*command.Result, error)) func(context.Context, json.RawMessage) (string, bool) {
	return func(ctx context.Context, args json.RawMessage) (string, bool) {
		res, err := h(ctx, args, nil)
		if err != nil {
			return err.Error(), true
		}
		return res.Text, res.IsErr
	}
}

func bridgedCtx() context.Context {
	return confinement.WithOrigin(context.Background(), confinement.CallerOrigin{
		Transport:  confinement.TransportBridge,
		SessionKey: bridgedSessionKey,
		Unit:       "clown-agent-test.service",
	})
}

// A call that arrived on the bridge is from inside the session's unit, so it
// is refused every verb a confined caller is refused, even when this serve's
// own classification (its cgroup, the principal and unit records) says
// unconfined. The stdio classifier must not even be consulted.
func TestBridgedCallIsConfinedWhateverTheServeClassifies(t *testing.T) {
	original := serveCallerConfined
	t.Cleanup(func() { serveCallerConfined = original })
	consulted := 0
	serveCallerConfined = func() (bool, string) {
		consulted++
		return false, "serve outside any unit, records say unconfined"
	}
	for _, h := range gatedServeHandlers {
		t.Run(h.tool, func(t *testing.T) {
			res, err := h.handle(bridgedCtx(), json.RawMessage(h.args), nil)
			if err != nil {
				t.Fatalf("%s: %v", h.tool, err)
			}
			if !res.IsErr {
				t.Fatalf("%s: a bridged call must be refused, got %q", h.tool, res.Text)
			}
			if specific, ok := bridgeSessionRefusals[h.tool]; ok {
				// The session verbs have their own table text (the plan's
				// "Refusal texts"); the others share the generic one.
				if res.Text != specific {
					t.Errorf("%s: refusal %q, want the table text %q", h.tool, res.Text, specific)
				}
			} else {
				for _, want := range []string{"refused: ", h.tool, "not available to a session inside its unit", "ask the operator", "`" + h.operatorCommand + "`"} {
					if !strings.Contains(res.Text, want) {
						t.Errorf("%s: refusal %q does not contain %q", h.tool, res.Text, want)
					}
				}
			}
			if !strings.HasPrefix(res.Text, "refused: ") {
				t.Errorf("%s: refusal %q does not open with the contract text", h.tool, res.Text)
			}
		})
	}
	if consulted != 0 {
		t.Errorf("the stdio classifier was consulted %d times for bridged calls", consulted)
	}
}

// Stdio calls (an explicit stdio origin, or none at all) classify exactly as
// before: the serve's own classification decides, and the old refusal text is
// unchanged.
func TestStdioOriginClassifiesAsBefore(t *testing.T) {
	original := serveCallerConfined
	t.Cleanup(func() { serveCallerConfined = original })
	stdio := confinement.WithOrigin(context.Background(), confinement.CallerOrigin{Transport: confinement.TransportStdio})
	for name, ctx := range map[string]context.Context{"no origin": context.Background(), "stdio origin": stdio} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CLOWN_SESSION_ID", "a-principal")
			serveCallerConfined = func() (bool, string) { return true, "unit record names the principal" }
			res, err := handleSessionHealth(ctx, json.RawMessage(`{}`), nil)
			if err != nil || !res.IsErr {
				t.Fatalf("confined stdio caller must be refused: %v %+v", err, res)
			}
			want := "session-health refused: this serve's caller (principal a-principal) classifies as confined (unit record names the principal). A confined session holds no right to session-health; the operator holds it, by running `sc session-health` at a terminal outside the unit."
			if res.Text != want {
				t.Errorf("stdio refusal changed:\n got %q\nwant %q", res.Text, want)
			}
			serveCallerConfined = func() (bool, string) { return false, "unconfined" }
			res, err = handleSessionHealth(ctx, json.RawMessage(`{"child":"nope/none"}`), nil)
			if err != nil || strings.Contains(res.Text, "classifies as confined") || strings.Contains(res.Text, "refused: ") {
				t.Fatalf("unconfined stdio caller must reach the tool: %v %q", err, res.Text)
			}
		})
	}
}

// What a bridged call acts on is the session the listener built the origin
// for, never a name in its arguments.
func TestBridgedWhoamiNamesTheListenersSessionNotTheArguments(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "the-serve-principal")
	stageSessionKey(t, bridgedSessionKey, "the-serve-principal")
	forged := json.RawMessage(`{"principal":"forged-principal","session_key":"forged/key","child":"forged/child"}`)
	res, err := handleWhoami(bridgedCtx(), forged, nil)
	if err != nil || res.IsErr {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	if !strings.Contains(res.Text, "session_key: "+bridgedSessionKey+"\n") {
		t.Errorf("whoami does not name the listener's session key:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "principal: the-serve-principal\n") {
		t.Errorf("whoami does not name the serve's principal:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "forged") {
		t.Errorf("whoami echoed a forged identity:\n%s", res.Text)
	}
}

// Naming another session in a gated verb's arguments changes nothing: the
// call is refused as the bridged session's own, before the argument is read.
func TestBridgedCallNamingAnotherSessionIsRefused(t *testing.T) {
	original := serveCallerConfined
	t.Cleanup(func() { serveCallerConfined = original })
	serveCallerConfined = func() (bool, string) { return false, "unconfined" }
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	stageSessionKey(t, bridgedSessionKey, "the-serve-principal")
	for _, tc := range []struct {
		tool, args string
		run        func(context.Context, json.RawMessage) (string, bool)
	}{
		{"cancel-restart", `{"child":"someone-else/branch","principal":"forged"}`, runHandler(handleCancelRestart)},
		{"list-handles accept", `{"accept":true,"principal":"forged"}`, runHandler(handleListHandles)},
		{"update-this-session-description", `{"description":"x","session_key":"someone-else/branch"}`, runHandler(handleUpdateDescription)},
		{"session-health", `{"child":"someone-else/branch"}`, runHandler(handleSessionHealth)},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			text, isErr := tc.run(bridgedCtx(), json.RawMessage(tc.args))
			if !isErr || !strings.HasPrefix(text, "refused: ") {
				t.Fatalf("got isErr=%v %q, want a contract refusal", isErr, text)
			}
		})
	}
	// Listing without accept stays open to a confined caller, and lists the
	// serve's own principal's handles whatever a body field claims.
	text, isErr := runHandler(handleListHandles)(bridgedCtx(), json.RawMessage(`{"principal":"forged-principal"}`))
	if isErr || text != "no handles" {
		t.Fatalf("plain bridged list-handles: isErr=%v %q", isErr, text)
	}
}
